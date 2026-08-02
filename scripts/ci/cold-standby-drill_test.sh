#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
runner="$repository_root/scripts/ops/cold-standby-drill.sh"
fixture="$repository_root/scripts/ci/testdata/failover-drill"
revision="aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
temporary="$(mktemp -d)"
trap 'rm -rf "$temporary"' EXIT

printf '%s\n' 'fixture-api-key' >"$temporary/api-key"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$temporary/traffic-probe"
printf '%s\n' '#!/usr/bin/env bash' 'exit 0' >"$temporary/promotion-probe"
printf '%s\n' '#!/usr/bin/env bash' 'exit 1' >"$temporary/failing-probe"
printf '%s\n' '#!/usr/bin/env bash' 'printf "# changed\\n" >>"$0"' 'exit 0' >"$temporary/changing-probe"
chmod 0700 "$temporary/traffic-probe" "$temporary/promotion-probe" "$temporary/failing-probe" "$temporary/changing-probe"

run_drill() {
  local state="$1" evidence="$2" scenario="${3:-success}" promotion_probe="${4:-$temporary/promotion-probe}" mode="${5:-run}"
  mkdir -p "$state"
  : >"$state/active-running"
  local -a command=(
    env
    "FAKE_DRILL_STATE=$state"
    "FAKE_DRILL_REVISION=$revision"
    "FAKE_DRILL_SCENARIO=$scenario"
    "PATH=$fixture:$PATH"
    OMNIWA_DRILL_APPROVAL=STOP_ACTIVE_AND_RUN_CONTROLLED_FAILOVER
    "OMNIWA_DRILL_COMPOSE_FILE=$repository_root/docker/docker-compose.yml"
    "OMNIWA_DRILL_PROJECT_DIRECTORY=$repository_root/docker"
    "OMNIWA_DRILL_EVIDENCE_FILE=$evidence"
    "OMNIWA_DRILL_API_KEY_FILE=$temporary/api-key"
    "OMNIWA_DRILL_EXPECTED_REVISION=$revision"
    "OMNIWA_DRILL_TRAFFIC_DRAIN_PROBE=$temporary/traffic-probe"
    "OMNIWA_DRILL_POST_PROMOTION_PROBE=$promotion_probe"
    OMNIWA_DRILL_POLL_SECONDS=1
    OMNIWA_DRILL_OUTBOX_DRAIN_SECONDS=2
    bash "$runner" --execute
  )
  if [[ "$mode" == "exec" ]]; then
    exec "${command[@]}"
  fi
  "${command[@]}"
}

if bash "$runner" --execute >/dev/null 2>&1; then
  echo "drill accepted missing approval and evidence configuration" >&2
  exit 1
fi

success_state="$temporary/success-state"
success_evidence="$temporary/success.json"
run_drill "$success_state" "$success_evidence" >/dev/null
jq -e --arg revision "$revision" '
  .schemaVersion == 1 and .status == "passed" and .failureCode == null and
  .recoveryRequired == false and .revision.expected == $revision and
  .revision.before == $revision and .revision.after == $revision and
  .outbox.baseline.pending == 1 and .outbox.after.pending == 0 and
  ([.checkpoints[].name] | index("migration_ownership_gate") != null) and
  ([.checkpoints[].name] | index("post_promotion_probe") != null)
' "$success_evidence" >/dev/null
if evidence_mode="$(stat -f '%Lp' "$success_evidence" 2>/dev/null)"; then
  :
else
  evidence_mode="$(stat -c '%a' "$success_evidence")"
fi
[[ "$evidence_mode" == "600" ]]
if grep -q 'fixture-api-key\|127.0.0.1' "$success_evidence"; then
  echo "drill evidence leaked a credential or endpoint" >&2
  exit 1
fi

migration_state="$temporary/migration-state"
migration_evidence="$temporary/migration.json"
if run_drill "$migration_state" "$migration_evidence" migration_failure >/dev/null 2>&1; then
  echo "drill accepted a failed migration ownership gate" >&2
  exit 1
fi
jq -e '.status == "failed" and .failureCode == "migration_or_ownership_gate_failed" and .recoveryRequired == true' \
  "$migration_evidence" >/dev/null
[[ ! -f "$migration_state/active-running" ]]

probe_state="$temporary/probe-state"
probe_evidence="$temporary/probe.json"
if run_drill "$probe_state" "$probe_evidence" success "$temporary/failing-probe" >/dev/null 2>&1; then
  echo "drill accepted a failed post-promotion probe" >&2
  exit 1
fi
jq -e '.status == "failed" and .failureCode == "post_promotion_probe_failed" and .recoveryRequired == true' \
  "$probe_evidence" >/dev/null
[[ -f "$probe_state/active-running" ]]

changed_state="$temporary/changed-state"
changed_evidence="$temporary/changed.json"
if run_drill "$changed_state" "$changed_evidence" success "$temporary/changing-probe" >/dev/null 2>&1; then
  echo "drill accepted a post-promotion probe changed during execution" >&2
  exit 1
fi
jq -e '.status == "failed" and .failureCode == "post_promotion_probe_changed" and .recoveryRequired == true' \
  "$changed_evidence" >/dev/null

interrupted_state="$temporary/interrupted-state"
interrupted_evidence="$temporary/interrupted.json"
run_drill "$interrupted_state" "$interrupted_evidence" never_ready "$temporary/promotion-probe" exec >/dev/null 2>&1 &
interrupted_pid=$!
for _ in $(seq 1 100); do
  [[ -f "$interrupted_state/promoted" ]] && break
  sleep 0.05
done
[[ -f "$interrupted_state/promoted" ]]
kill -TERM "$interrupted_pid"
if wait "$interrupted_pid"; then
  echo "drill accepted an interrupted promotion" >&2
  exit 1
fi
jq -e '.status == "failed" and .failureCode == "interrupted_sigterm" and .recoveryRequired == true' \
  "$interrupted_evidence" >/dev/null

echo "cold-standby drill tests passed"
