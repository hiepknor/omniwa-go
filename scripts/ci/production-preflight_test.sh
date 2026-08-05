#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="$repository_root/scripts/ops/production-preflight.sh"
temporary="$(mktemp -d)"
trap 'rm -rf -- "$temporary"' EXIT

revision="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
printf '%s\n' 'fixture-api-key' >"$temporary/api-key"
chmod 0600 "$temporary/api-key"
mkdir "$temporary/bin"

cat >"$temporary/bin/curl" <<'EOF'
#!/usr/bin/env bash
set -euo pipefail
url="${!#}"
case "$url" in
  */server/live) printf '{"status":"ok"}\n' ;;
  */server/ready)
    [[ "${FAKE_PREFLIGHT_SCENARIO:-success}" != "not_ready" ]] || exit 22
    printf '{"status":"ready"}\n'
    ;;
  */server/capabilities)
    revision="${FAKE_PREFLIGHT_REVISION:-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa}"
    printf '{"data":{"revision":"%s"}}\n' "$revision"
    ;;
  */metrics)
    dead_letters=0
    oldest=0
    [[ "${FAKE_PREFLIGHT_SCENARIO:-success}" != "dead_letter" ]] || dead_letters=1
    [[ "${FAKE_PREFLIGHT_SCENARIO:-success}" != "old_pending" ]] || oldest=301
    printf 'omniwa_external_event_outbox_rows{status="pending"} 0\n'
    printf 'omniwa_external_event_outbox_rows{status="processing"} 0\n'
    printf 'omniwa_external_event_outbox_rows{status="dead_letter"} %s\n' "$dead_letters"
    printf 'omniwa_external_event_outbox_oldest_pending_age_seconds %s\n' "$oldest"
    ;;
  *) exit 22 ;;
esac
EOF
chmod 0700 "$temporary/bin/curl"

printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$temporary/media-probe"
printf '%s\n' '#!/usr/bin/env bash' 'exit 1' >"$temporary/failing-media-probe"
chmod 0700 "$temporary/media-probe" "$temporary/failing-media-probe"

run_preflight() {
  env \
    "PATH=$temporary/bin:$PATH" \
    OMNIWA_PREFLIGHT_API_URL=http://127.0.0.1:4000 \
    "OMNIWA_PREFLIGHT_API_KEY_FILE=$temporary/api-key" \
    "OMNIWA_PREFLIGHT_EXPECTED_REVISION=$revision" \
    "OMNIWA_PREFLIGHT_MEDIA_PROBE=$temporary/media-probe" \
    "$@" \
    bash "$runner"
}

output="$(run_preflight)"
[[ "$output" == *"production preflight passed"* && "$output" == *"revision=$revision"* ]]

for scenario in not_ready dead_letter old_pending; do
  if run_preflight "FAKE_PREFLIGHT_SCENARIO=$scenario" >/dev/null 2>&1; then
    echo "production preflight accepted failure scenario: $scenario" >&2
    exit 1
  fi
done

if run_preflight FAKE_PREFLIGHT_REVISION=bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb >/dev/null 2>&1; then
  echo "production preflight accepted a mismatched runtime revision" >&2
  exit 1
fi

if run_preflight "OMNIWA_PREFLIGHT_MEDIA_PROBE=$temporary/failing-media-probe" >/dev/null 2>&1; then
  echo "production preflight accepted a failed media probe" >&2
  exit 1
fi

if run_preflight OMNIWA_PREFLIGHT_API_URL=http://user@example.invalid >/dev/null 2>&1; then
  echo "production preflight accepted URL credentials" >&2
  exit 1
fi

printf 'first-line\nsecond-line\n' >"$temporary/api-key"
if run_preflight >/dev/null 2>&1; then
  echo "production preflight accepted a multiline API key" >&2
  exit 1
fi
printf '%s\n' 'fixture-api-key' >"$temporary/api-key"

chmod 0644 "$temporary/api-key"
if run_preflight >/dev/null 2>&1; then
  echo "production preflight accepted an unsafe API key file" >&2
  exit 1
fi

echo "production preflight tests passed"
