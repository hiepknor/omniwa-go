#!/usr/bin/env bash

set -Eeuo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="${OMNIWA_DRILL_COMPOSE_FILE:-$repository_root/docker/docker-compose.yml}"
project_directory="${OMNIWA_DRILL_PROJECT_DIRECTORY:-$repository_root/docker}"
active_service="${OMNIWA_DRILL_ACTIVE_SERVICE:-omniwa-go}"
standby_service="${OMNIWA_DRILL_STANDBY_SERVICE:-omniwa-standby}"
migration_service="${OMNIWA_DRILL_MIGRATION_SERVICE:-omniwa-migrate}"
active_url="${OMNIWA_DRILL_ACTIVE_URL:-http://127.0.0.1:4000}"
standby_url="${OMNIWA_DRILL_STANDBY_URL:-http://127.0.0.1:4001}"
evidence_file="${OMNIWA_DRILL_EVIDENCE_FILE:-}"
api_key_file="${OMNIWA_DRILL_API_KEY_FILE:-}"
expected_revision="${OMNIWA_DRILL_EXPECTED_REVISION:-}"
traffic_probe="${OMNIWA_DRILL_TRAFFIC_DRAIN_PROBE:-}"
promotion_probe="${OMNIWA_DRILL_POST_PROMOTION_PROBE:-}"
rto_limit="${OMNIWA_DRILL_RTO_SECONDS:-300}"
outbox_age_limit="${OMNIWA_DRILL_OUTBOX_MAX_AGE_SECONDS:-300}"
outbox_drain_limit="${OMNIWA_DRILL_OUTBOX_DRAIN_SECONDS:-120}"
poll_interval="${OMNIWA_DRILL_POLL_SECONDS:-2}"

approval_phrase="STOP_ACTIVE_AND_RUN_CONTROLLED_FAILOVER"
result_status="failed"
failure_code="preflight_failed"
recovery_required=false
outage_started_epoch=0
rto_seconds=0
baseline_outbox='{"pending":0,"processing":0,"deadLetter":0,"oldestPendingAgeSeconds":0}'
post_outbox='{"pending":0,"processing":0,"deadLetter":0,"oldestPendingAgeSeconds":0}'
active_revision_before=""
active_revision_after=""
traffic_probe_sha256=""
promotion_probe_sha256=""
started_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

fail() {
  failure_code="$1"
  record "$1" "failed"
  echo "cold-standby drill failed: $1" >&2
  exit 1
}

require_positive_integer() {
  local name="$1" value="$2" maximum="$3"
  [[ "$value" =~ ^[1-9][0-9]*$ ]] || fail "invalid_${name}"
  (( value <= maximum )) || fail "invalid_${name}"
}

require_base_url() {
  local value="$1" name="$2"
  [[ "$value" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+$ ]] || fail "invalid_${name}_url"
}

require_probe() {
  local value="$1" name="$2"
  [[ "$value" == /* && -f "$value" && -x "$value" ]] || fail "invalid_${name}_probe"
}

sha256_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
  else
    shasum -a 256 "$1" | awk '{print $1}'
  fi
}

record() {
  local name="$1" status="$2"
  jq -cn --arg name "$name" --arg status "$status" --arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    '{name:$name,status:$status,at:$at}' >>"$checkpoint_file"
}

finalize() {
  local exit_code=$?
  trap - EXIT
  trap - HUP INT TERM
  local ended_at evidence_temp
  ended_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  evidence_temp="$(mktemp "$evidence_directory/.omniwa-failover-evidence.XXXXXX")"
  chmod 0600 "$evidence_temp"
  jq -n \
    --arg schema_version "1" \
    --arg status "$result_status" \
    --arg failure_code "$failure_code" \
    --arg started_at "$started_at" \
    --arg ended_at "$ended_at" \
    --arg active_revision_before "$active_revision_before" \
    --arg active_revision_after "$active_revision_after" \
    --arg expected_revision "$expected_revision" \
    --arg traffic_probe_sha256 "$traffic_probe_sha256" \
    --arg promotion_probe_sha256 "$promotion_probe_sha256" \
    --argjson recovery_required "$recovery_required" \
    --argjson rto_limit_seconds "$rto_limit" \
    --argjson rto_seconds "$rto_seconds" \
    --argjson outbox_age_limit_seconds "$outbox_age_limit" \
    --argjson outbox_drain_limit_seconds "$outbox_drain_limit" \
    --argjson baseline_outbox "$baseline_outbox" \
    --argjson post_outbox "$post_outbox" \
    --slurpfile checkpoints "$checkpoint_file" \
    '{schemaVersion:($schema_version|tonumber),status:$status,failureCode:(if $failure_code=="" then null else $failure_code end),recoveryRequired:$recovery_required,startedAt:$started_at,endedAt:$ended_at,rto:{limitSeconds:$rto_limit_seconds,observedSeconds:$rto_seconds},revision:{expected:$expected_revision,before:$active_revision_before,after:$active_revision_after},outbox:{maxPendingAgeSeconds:$outbox_age_limit_seconds,drainLimitSeconds:$outbox_drain_limit_seconds,baseline:$baseline_outbox,after:$post_outbox},probes:{trafficDrainSHA256:$traffic_probe_sha256,postPromotionSHA256:$promotion_probe_sha256},checkpoints:$checkpoints}' \
    >"$evidence_temp"
  if ! ln "$evidence_temp" "$evidence_file"; then
    rm -f "$evidence_temp" "$checkpoint_file" "$curl_header_file"
    echo "cold-standby drill could not publish evidence without overwriting an existing file" >&2
    exit 1
  fi
  rm -f "$evidence_temp"
  rm -f "$checkpoint_file" "$curl_header_file"
  exit "$exit_code"
}

handle_interrupt() {
  local signal_name="$1" exit_code="$2"
  trap - HUP INT TERM
  failure_code="interrupted_${signal_name}"
  record "$failure_code" "failed" || true
  echo "cold-standby drill interrupted: $signal_name" >&2
  exit "$exit_code"
}

metric_value() {
  local metrics="$1" metric="$2"
  awk -v expected="$metric" '$1 == expected {print $2; found=1} END {if (!found) exit 1}' <<<"$metrics"
}

read_outbox() {
  local metrics pending processing dead_letter oldest
  metrics="$("${curl_request[@]}" --header "@$curl_header_file" --fail --silent --show-error "$active_url/metrics")" || return 1
  pending="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="pending"}')" || return 1
  processing="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="processing"}')" || return 1
  dead_letter="$(metric_value "$metrics" 'omniwa_external_event_outbox_rows{status="dead_letter"}')" || return 1
  oldest="$(metric_value "$metrics" 'omniwa_external_event_outbox_oldest_pending_age_seconds')" || return 1
  jq -cn --argjson pending "$pending" --argjson processing "$processing" \
    --argjson dead_letter "$dead_letter" --argjson oldest_pending_age_seconds "$oldest" \
    '{pending:$pending,processing:$processing,deadLetter:$dead_letter,oldestPendingAgeSeconds:$oldest_pending_age_seconds}'
}

runtime_revision() {
  "${curl_request[@]}" --header "@$curl_header_file" --fail --silent --show-error "$active_url/server/capabilities" |
    jq -er '.data.revision | select(type=="string")'
}

compose=(docker compose --project-directory "$project_directory" --file "$compose_file")

[[ "${1:-}" == "--execute" && $# -eq 1 ]] || {
  echo "usage: OMNIWA_DRILL_APPROVAL=$approval_phrase ... $0 --execute" >&2
  exit 2
}
[[ "${OMNIWA_DRILL_APPROVAL:-}" == "$approval_phrase" ]] || {
  echo "cold-standby drill approval phrase is missing" >&2
  exit 2
}
[[ "$evidence_file" == /* && ! -e "$evidence_file" && -d "$(dirname "$evidence_file")" ]] || {
  echo "OMNIWA_DRILL_EVIDENCE_FILE must be a new absolute path in an existing directory" >&2
  exit 2
}
for dependency in docker curl jq awk grep date head ln mktemp sleep; do
  command -v "$dependency" >/dev/null 2>&1 || {
    echo "cold-standby drill dependency is missing: $dependency" >&2
    exit 2
  }
done
if ! command -v sha256sum >/dev/null 2>&1 && ! command -v shasum >/dev/null 2>&1; then
  echo "cold-standby drill SHA-256 utility is missing" >&2
  exit 2
fi
[[ -f "$compose_file" ]] || { echo "cold-standby drill compose file is missing" >&2; exit 2; }
[[ -r "$api_key_file" ]] || { echo "cold-standby drill API key file is unreadable" >&2; exit 2; }
api_key="$(head -c 4097 "$api_key_file")"
[[ -n "$api_key" && ${#api_key} -le 4096 && "$api_key" != *$'\n'* && "$api_key" != *$'\r'* ]] || {
  unset api_key
  echo "cold-standby drill API key file is invalid" >&2
  exit 2
}
if LC_ALL=C grep -q '[^ -~]' < <(printf '%s' "$api_key"); then
  unset api_key
  echo "cold-standby drill API key contains unsupported control or non-ASCII bytes" >&2
  exit 2
fi
[[ "$expected_revision" =~ ^[0-9a-f]{40}$ ]] || { unset api_key; echo "cold-standby drill revision is invalid" >&2; exit 2; }
[[ "$rto_limit" =~ ^[1-9][0-9]*$ ]] && (( rto_limit <= 1800 )) || { unset api_key; echo "cold-standby drill RTO is invalid" >&2; exit 2; }
[[ "$outbox_age_limit" =~ ^[1-9][0-9]*$ ]] && (( outbox_age_limit <= 86400 )) || { unset api_key; echo "cold-standby drill outbox age is invalid" >&2; exit 2; }
[[ "$outbox_drain_limit" =~ ^[1-9][0-9]*$ ]] && (( outbox_drain_limit <= 600 )) || { unset api_key; echo "cold-standby drill outbox drain time is invalid" >&2; exit 2; }
[[ "$poll_interval" =~ ^[1-9][0-9]*$ ]] && (( poll_interval <= 30 )) || { unset api_key; echo "cold-standby drill poll interval is invalid" >&2; exit 2; }
[[ "$active_url" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+$ ]] || { unset api_key; echo "cold-standby drill active URL must be loopback HTTP with an explicit port" >&2; exit 2; }
[[ "$standby_url" =~ ^http://(127\.0\.0\.1|localhost):[0-9]+$ ]] || { unset api_key; echo "cold-standby drill standby URL must be loopback HTTP with an explicit port" >&2; exit 2; }
[[ "$traffic_probe" == /* && -f "$traffic_probe" && -x "$traffic_probe" ]] || { unset api_key; echo "cold-standby traffic probe is invalid" >&2; exit 2; }
[[ "$promotion_probe" == /* && -f "$promotion_probe" && -x "$promotion_probe" ]] || { unset api_key; echo "cold-standby promotion probe is invalid" >&2; exit 2; }
evidence_directory="$(dirname "$evidence_file")"
checkpoint_file="$(mktemp "$evidence_directory/.omniwa-failover-checkpoints.XXXXXX")"
curl_header_file="$(mktemp "$evidence_directory/.omniwa-failover-header.XXXXXX")"
chmod 0600 "$checkpoint_file" "$curl_header_file"
trap finalize EXIT
trap 'handle_interrupt sighup 129' HUP
trap 'handle_interrupt sigint 130' INT
trap 'handle_interrupt sigterm 143' TERM

printf 'apikey: %s\n' "$api_key" >"$curl_header_file"
unset api_key
require_positive_integer "rto_seconds" "$rto_limit" 1800
require_positive_integer "outbox_age_seconds" "$outbox_age_limit" 86400
require_positive_integer "outbox_drain_seconds" "$outbox_drain_limit" 600
require_positive_integer "poll_seconds" "$poll_interval" 30
require_base_url "$active_url" "active"
require_base_url "$standby_url" "standby"
require_probe "$traffic_probe" "traffic_drain"
require_probe "$promotion_probe" "post_promotion"
curl_request=(curl --connect-timeout "$poll_interval" --max-time "$poll_interval")
traffic_probe_sha256="$(sha256_file "$traffic_probe")"
promotion_probe_sha256="$(sha256_file "$promotion_probe")"
record "configuration_valid" "passed"

"${curl_request[@]}" --fail --silent --show-error "$active_url/server/ready" | jq -e '.status=="ready"' >/dev/null || fail "active_not_ready_before_drill"
active_revision_before="$(runtime_revision)" || fail "active_revision_unavailable"
[[ "$active_revision_before" == "$expected_revision" ]] || fail "active_revision_mismatch_before_drill"
baseline_outbox="$(read_outbox)" || fail "baseline_outbox_unavailable"
record "active_baseline" "passed"

"${compose[@]}" --profile standby up --detach --wait "$standby_service" >/dev/null || fail "standby_start_failed"
"${curl_request[@]}" --fail --silent --show-error "$standby_url/server/live" | jq -e '.status=="ok"' >/dev/null || fail "standby_not_live"
standby_ready_status="$("${curl_request[@]}" --silent --show-error --output /dev/null --write-out '%{http_code}' "$standby_url/server/ready")" || fail "standby_readiness_unavailable"
[[ "$standby_ready_status" == "503" ]] || fail "standby_became_ready"
standby_container="$("${compose[@]}" ps --quiet "$standby_service")" || fail "standby_container_unavailable"
[[ -n "$standby_container" ]] || fail "standby_container_missing"
[[ "$(docker inspect --format '{{len .Mounts}}' "$standby_container")" == "0" ]] || fail "standby_has_mounts"
standby_environment="$(docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$standby_container")" || fail "standby_environment_unavailable"
if grep -Eq '^(GLOBAL_API_KEY|POSTGRES_|AMQP_|NATS_|MINIO_|WEBHOOK_|LICENSE_|INSTANCE_TOKEN_|API_AUDIO|PROXY_)' <<<"$standby_environment"; then
  fail "standby_has_application_credentials"
fi
record "standby_verified" "passed"

"$traffic_probe" >/dev/null 2>&1 || fail "traffic_not_drained"
[[ "$(sha256_file "$traffic_probe")" == "$traffic_probe_sha256" ]] || fail "traffic_probe_changed"
record "traffic_drained" "passed"

outage_started_epoch="$(date +%s)"
recovery_required=true
"${compose[@]}" stop "$active_service" >/dev/null || fail "active_stop_failed"
[[ -z "$("${compose[@]}" ps --status running --quiet "$active_service")" ]] || fail "active_still_running"
record "former_active_stopped" "passed"

"${compose[@]}" --profile standby stop "$standby_service" >/dev/null || fail "standby_stop_failed"
record "standby_stopped" "passed"

"${compose[@]}" --profile operations run --rm --no-deps --no-TTY "$migration_service" </dev/null >/dev/null || fail "migration_or_ownership_gate_failed"
record "migration_ownership_gate" "passed"

"${compose[@]}" up --detach "$active_service" >/dev/null || fail "promoted_active_start_failed"
deadline=$((outage_started_epoch + rto_limit))
while true; do
  ready_status="$("${curl_request[@]}" --silent --output /dev/null --write-out '%{http_code}' "$active_url/server/ready" 2>/dev/null || true)"
  if [[ "$ready_status" == "200" ]]; then
    ready_body="$("${curl_request[@]}" --fail --silent "$active_url/server/ready" 2>/dev/null || true)"
    if jq -e '.status=="ready"' >/dev/null 2>&1 <<<"$ready_body"; then
      break
    fi
  fi
  (( $(date +%s) < deadline )) || fail "rto_exceeded"
  sleep "$poll_interval"
done
rto_seconds=$(($(date +%s) - outage_started_epoch))
(( rto_seconds <= rto_limit )) || fail "rto_exceeded"
record "promoted_active_ready" "passed"

active_revision_after="$(runtime_revision)" || fail "active_revision_unavailable_after_promotion"
[[ "$active_revision_after" == "$expected_revision" ]] || fail "active_revision_mismatch_after_promotion"
baseline_active_rows="$(jq -r '.pending + .processing' <<<"$baseline_outbox")"
outbox_deadline=$(($(date +%s) + outbox_drain_limit))
while true; do
  post_outbox="$(read_outbox)" || fail "post_promotion_outbox_unavailable"
  post_active_rows="$(jq -r '.pending + .processing' <<<"$post_outbox")"
  if awk -v baseline="$baseline_active_rows" -v current="$post_active_rows" \
    'BEGIN {exit !((baseline == 0 && current == 0) || (baseline > 0 && current < baseline))}'; then
    break
  fi
  (( $(date +%s) < outbox_deadline )) || fail "outbox_did_not_resume"
  sleep "$poll_interval"
done
baseline_dead_letter="$(jq -r '.deadLetter' <<<"$baseline_outbox")"
post_dead_letter="$(jq -r '.deadLetter' <<<"$post_outbox")"
post_oldest="$(jq -r '.oldestPendingAgeSeconds' <<<"$post_outbox")"
awk -v before="$baseline_dead_letter" -v after="$post_dead_letter" 'BEGIN {exit !(after <= before)}' || fail "outbox_dead_letter_increased"
awk -v age="$post_oldest" -v limit="$outbox_age_limit" 'BEGIN {exit !(age <= limit)}' || fail "outbox_pending_too_old"
record "durable_outbox_verified" "passed"

"$promotion_probe" >/dev/null 2>&1 || fail "post_promotion_probe_failed"
[[ "$(sha256_file "$promotion_probe")" == "$promotion_probe_sha256" ]] || fail "post_promotion_probe_changed"
record "post_promotion_probe" "passed"

recovery_required=false
result_status="passed"
failure_code=""
record "drill_complete" "passed"
echo "cold-standby drill passed; evidence=$evidence_file rto_seconds=$rto_seconds"
