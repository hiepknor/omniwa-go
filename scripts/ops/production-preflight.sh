#!/usr/bin/env bash

set -euo pipefail

fail() {
  echo "production preflight failed: $1" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

file_mode() {
  if stat -f '%Lp' "$1" >/dev/null 2>&1; then
    stat -f '%Lp' "$1"
  else
    stat -c '%a' "$1"
  fi
}

metric_value() {
  local metrics="$1" expression="$2"
  awk -v expression="$expression" '$1 == expression {print $2; found=1} END {if (!found) exit 1}' <<<"$metrics"
}

is_non_negative_number() {
  awk -v value="$1" 'BEGIN {exit !(value ~ /^[0-9]+([.][0-9]+)?([eE][+-]?[0-9]+)?$/)}'
}

api_url="${OMNIWA_PREFLIGHT_API_URL:-}"
api_key_file="${OMNIWA_PREFLIGHT_API_KEY_FILE:-}"
expected_revision="${OMNIWA_PREFLIGHT_EXPECTED_REVISION:-}"
metrics_url="${OMNIWA_PREFLIGHT_METRICS_URL:-${api_url%/}/metrics}"
max_pending_age="${OMNIWA_PREFLIGHT_MAX_PENDING_AGE_SECONDS:-300}"
max_dead_letters="${OMNIWA_PREFLIGHT_MAX_DEAD_LETTERS:-0}"
media_probe="${OMNIWA_PREFLIGHT_MEDIA_PROBE:-}"

[[ "$api_url" =~ ^https?://[^/?#]+(:[0-9]+)?(/[^?#]*)?$ ]] || fail "OMNIWA_PREFLIGHT_API_URL must be an HTTP(S) URL without credentials, query, or fragment"
[[ "$metrics_url" =~ ^https?://[^/?#]+(:[0-9]+)?(/[^?#]*)?$ ]] || fail "OMNIWA_PREFLIGHT_METRICS_URL must be an HTTP(S) URL without credentials, query, or fragment"
[[ "$api_url" != *@* && "$api_url" != *[$'\t\r\n ']* ]] || fail "OMNIWA_PREFLIGHT_API_URL must not contain credentials or whitespace"
[[ "$metrics_url" != *@* && "$metrics_url" != *[$'\t\r\n ']* ]] || fail "OMNIWA_PREFLIGHT_METRICS_URL must not contain credentials or whitespace"
[[ "$expected_revision" =~ ^[0-9a-f]{40}$ ]] || fail "OMNIWA_PREFLIGHT_EXPECTED_REVISION must be a full lowercase Git revision"
[[ -f "$api_key_file" && -r "$api_key_file" ]] || fail "OMNIWA_PREFLIGHT_API_KEY_FILE must be a readable regular file"
[[ "$(file_mode "$api_key_file")" == "600" ]] || fail "API key file permissions must be 0600"
[[ -s "$api_key_file" ]] || fail "API key file must not be empty"
is_non_negative_number "$max_pending_age" || fail "OMNIWA_PREFLIGHT_MAX_PENDING_AGE_SECONDS must be non-negative"
is_non_negative_number "$max_dead_letters" || fail "OMNIWA_PREFLIGHT_MAX_DEAD_LETTERS must be non-negative"

require_command curl
require_command jq

temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT
chmod 0700 "$temporary"
curl_config="$temporary/curl.conf"
api_key="$(<"$api_key_file")"
header_quote='"'
header_backslash=$'\\'
[[ -n "$api_key" && "$api_key" != *[$'\r\n']* && "$api_key" != *"$header_quote"* && "$api_key" != *"$header_backslash"* ]] || fail "API key contains unsupported header characters"
printf 'header = "apikey: %s"\n' "$api_key" >"$curl_config"
chmod 0600 "$curl_config"
unset api_key

curl_request=(curl --config "$curl_config" --fail --silent --show-error --max-time 10 --connect-timeout 5 --proto '=https,http' --max-redirs 0)

live="$("${curl_request[@]}" "${api_url%/}/server/live")" || fail "liveness endpoint is unavailable"
jq -e '.status == "ok"' <<<"$live" >/dev/null || fail "liveness response is invalid"

ready="$("${curl_request[@]}" "${api_url%/}/server/ready")" || fail "active process is not ready"
jq -e '.status == "ready"' <<<"$ready" >/dev/null || fail "readiness response is invalid"

capabilities="$("${curl_request[@]}" "${api_url%/}/server/capabilities")" || fail "capabilities endpoint is unavailable"
runtime_revision="$(jq -er '.data.revision' <<<"$capabilities")" || fail "runtime revision is missing"
[[ "$runtime_revision" == "$expected_revision" ]] || fail "runtime revision does not match the approved deployment revision"

metrics="$("${curl_request[@]}" "$metrics_url")" || fail "authenticated metrics endpoint is unavailable"
pending="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="pending"}')" || fail "pending outbox metric is missing"
processing="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="processing"}')" || fail "processing outbox metric is missing"
dead_letters="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="dead_letter"}')" || fail "dead-letter outbox metric is missing"
oldest_pending="$(metric_value "$metrics" 'omniwa_external_event_outbox_oldest_pending_age_seconds')" || fail "oldest pending outbox metric is missing"
for value in "$pending" "$processing" "$dead_letters" "$oldest_pending"; do
  is_non_negative_number "$value" || fail "outbox metrics must contain finite non-negative numbers"
done
awk -v actual="$dead_letters" -v maximum="$max_dead_letters" 'BEGIN {exit !(actual <= maximum)}' || fail "dead-letter count exceeds the approved threshold"
awk -v actual="$oldest_pending" -v maximum="$max_pending_age" 'BEGIN {exit !(actual <= maximum)}' || fail "oldest pending delivery exceeds the approved threshold"

if [[ -n "$media_probe" ]]; then
  [[ "$media_probe" == /* && -f "$media_probe" && -x "$media_probe" ]] || fail "OMNIWA_PREFLIGHT_MEDIA_PROBE must be an absolute executable file"
  probe_mode="$(file_mode "$media_probe")"
  (( (8#$probe_mode & 0022) == 0 )) || fail "media probe must not be group- or world-writable"
  "$media_probe" >/dev/null || fail "media synthetic probe failed"
fi

echo "production preflight passed revision=$expected_revision pending=$pending processing=$processing dead_letters=$dead_letters oldest_pending_seconds=$oldest_pending media_probe=$([[ -n "$media_probe" ]] && echo configured || echo skipped)"
