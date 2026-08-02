#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="$repository_root/docker/docker-compose.smoke.yml"
source_sha="${SOURCE_SHA:-$(git -C "$repository_root" rev-parse HEAD)}"
smoke_version="${SMOKE_VERSION:-ci-smoke}"
smoke_api_key="${SMOKE_API_KEY:-omniwa-container-smoke-key}"
smoke_postgres_auth_dsn="postgresql://postgres:postgres@postgres:5432/omniwa_auth?sslmode=disable"
smoke_postgres_users_dsn="postgresql://postgres:postgres@postgres:5432/omniwa_users?sslmode=disable"
project_suffix="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-${PPID}"
project_name="omniwa-smoke-${project_suffix//[^a-zA-Z0-9_-]/-}"
smoke_image="omniwa-smoke:${project_suffix//[^a-zA-Z0-9_.-]/-}"
smoke_active_port="$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1]); s.close()')"
expected_migrations="41:41"

if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "SOURCE_SHA must be a full 40-character lowercase Git commit" >&2
  exit 1
fi

export SOURCE_SHA="$source_sha"
export SMOKE_VERSION="$smoke_version"
export SMOKE_API_KEY="$smoke_api_key"
export SMOKE_POSTGRES_AUTH_DSN="$smoke_postgres_auth_dsn"
export SMOKE_POSTGRES_USERS_DSN="$smoke_postgres_users_dsn"
export SMOKE_IMAGE="$smoke_image"
export SMOKE_ACTIVE_PORT="$smoke_active_port"

compose=(docker compose --project-name "$project_name" --file "$compose_file")
standby_container="${project_name}-standby"
drill_temporary=""

cleanup() {
  docker rm --force "$standby_container" >/dev/null 2>&1 || true
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
  if [[ -n "$drill_temporary" && -d "$drill_temporary" ]]; then
    rm -rf -- "$drill_temporary"
  fi
}
trap cleanup EXIT

"${compose[@]}" build omniwa-go
docker run --detach --name "$standby_container" \
  --env RUNTIME_MODE=standby \
  --env SERVER_PORT=4000 \
  --publish "127.0.0.1::4000" \
  "$smoke_image" >/dev/null
standby_address="$(docker port "$standby_container" 4000/tcp | tail -n 1)"
standby_url="http://${standby_address}"
for attempt in $(seq 1 30); do
  if curl --fail --silent "$standby_url/server/live" 2>/dev/null | jq -e '.status == "ok"' >/dev/null; then
    break
  fi
  if [[ "$attempt" == "30" ]]; then
    docker logs "$standby_container" >&2
    echo "standby control plane did not become live" >&2
    exit 1
  fi
  sleep 1
done
standby_ready_status="$(curl --silent --show-error --output /dev/null --write-out '%{http_code}' "$standby_url/server/ready")"
standby_ready_body="$(curl --silent --show-error "$standby_url/server/ready")"
if [[ "$standby_ready_status" != "503" ]] ||
  ! jq -e '.status == "not_ready"' <<<"$standby_ready_body" >/dev/null; then
  echo "standby unexpectedly became ready: HTTP $standby_ready_status $standby_ready_body" >&2
  exit 1
fi
if [[ "$(curl --silent --output /dev/null --write-out '%{http_code}' "$standby_url/server/capabilities")" != "404" ]]; then
  echo "standby exposed an application route" >&2
  exit 1
fi
if [[ "$(docker inspect --format '{{len .Mounts}}' "$standby_container")" != "0" ]]; then
  echo "standby container unexpectedly received a mount" >&2
  exit 1
fi
if docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$standby_container" |
  grep -Eq '^(GLOBAL_API_KEY|POSTGRES_|AMQP_|NATS_|MINIO_|WEBHOOK_|LICENSE_|INSTANCE_TOKEN_|API_AUDIO|PROXY_)'; then
  echo "standby container unexpectedly received application credentials" >&2
  exit 1
fi
docker rm --force "$standby_container" >/dev/null

"${compose[@]}" up --detach --wait postgres
"${compose[@]}" run --rm --no-deps omniwa-go migrate
"${compose[@]}" run --rm --no-deps omniwa-go migrate

if [[ -n "$("${compose[@]}" ps --quiet omniwa-go)" ]]; then
  echo "one-shot migration unexpectedly left an application container running" >&2
  exit 1
fi

"${compose[@]}" up --detach omniwa-go

refresh_runtime_coordinates() {
  container_id="$("${compose[@]}" ps --all --quiet omniwa-go)"
  published_address="$("${compose[@]}" port omniwa-go 4000 | tail -n 1)"
  base_url="http://${published_address}"
}

refresh_runtime_coordinates

wait_for_liveness() {
  local attempt
  for attempt in $(seq 1 60); do
    if curl --fail --silent "$base_url/server/live" 2>/dev/null | jq -e '.status == "ok"' >/dev/null; then
      return 0
    fi
    sleep 2
  done
  "${compose[@]}" logs omniwa-go >&2
  echo "container did not become live within 120 seconds" >&2
  return 1
}

assert_runtime_health_contract() {
  curl --fail --silent --show-error "$base_url/server/live" | jq -e '.status == "ok"' >/dev/null
  curl --fail --silent --show-error "$base_url/server/ready" | jq -e '.status == "ready"' >/dev/null
  curl --fail --silent --show-error "$base_url/server/ok" | jq -e '.status == "ok"' >/dev/null
}

assert_artifact_identity() {
  local capabilities label_revision label_version runtime_revision runtime_version runtime_user
  capabilities="$(curl --fail --silent --show-error --header "apikey: $smoke_api_key" "$base_url/server/capabilities")"
  label_revision="$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$container_id")"
  label_version="$(docker inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$container_id")"
  runtime_revision="$(jq -er '.data.revision' <<<"$capabilities")"
  runtime_version="$(jq -er '.data.version' <<<"$capabilities")"
  runtime_user="$(docker inspect --format '{{ .Config.User }}' "$container_id")"

  [[ "$label_revision" == "$source_sha" ]]
  [[ "$runtime_revision" == "$source_sha" ]]
  [[ "$label_version" == "$smoke_version" ]]
  [[ "$runtime_version" == "$smoke_version" ]]
  [[ "$runtime_user" == "10001:10001" ]]
}

assert_migrations() {
  local migration_state
  migration_state="$("${compose[@]}" exec --no-TTY postgres psql \
    --username postgres --dbname omniwa_users --tuples-only --no-align \
    --command "SELECT COALESCE(MAX(version), 0) || ':' || COUNT(*) FROM schema_migrations")"
  if [[ "$migration_state" != "$expected_migrations" ]]; then
    echo "migration state mismatch: expected $expected_migrations, got $migration_state" >&2
    return 1
  fi
}

assert_auth_migrations() {
  local auth_schema_rows auth_migration_state
  auth_schema_rows="$("${compose[@]}" exec --no-TTY postgres psql \
    --username postgres --dbname omniwa_auth --tuples-only --no-align \
    --command "SELECT COUNT(*) FROM whatsmeow_version")"
  if [[ "$auth_schema_rows" != "1" ]]; then
    echo "WhatsApp auth migration state mismatch: expected one version row, got $auth_schema_rows" >&2
    return 1
  fi
  auth_migration_state="$("${compose[@]}" exec --no-TTY postgres psql \
    --username postgres --dbname omniwa_auth --tuples-only --no-align \
    --command "SELECT COALESCE(MAX(version), 0) || ':' || COUNT(*) FROM omniwa_auth_schema_migrations")"
  if [[ "$auth_migration_state" != "1:1" ]]; then
    echo "application auth migration state mismatch: expected 1:1, got $auth_migration_state" >&2
    return 1
  fi
}

ownership_epoch() {
  "${compose[@]}" exec --no-TTY postgres psql \
    --username postgres --dbname omniwa_users --tuples-only --no-align \
    --command "SELECT epoch FROM runtime_ownership_epochs WHERE scope = 'application'"
}

wait_for_liveness
assert_runtime_health_contract
assert_artifact_identity
assert_migrations
assert_auth_migrations
initial_ownership_epoch="$(ownership_epoch)"
[[ "$initial_ownership_epoch" =~ ^[1-9][0-9]*$ ]]

drill_temporary="$(mktemp -d)"
printf '%s\n' "$smoke_api_key" >"$drill_temporary/api-key"
"${compose[@]}" --profile standby up --detach --wait omniwa-standby
standby_published_address="$("${compose[@]}" port omniwa-standby 4000 | tail -n 1)"
COMPOSE_PROJECT_NAME="$project_name" \
OMNIWA_DRILL_APPROVAL=STOP_ACTIVE_AND_RUN_CONTROLLED_FAILOVER \
OMNIWA_DRILL_COMPOSE_FILE="$compose_file" \
OMNIWA_DRILL_PROJECT_DIRECTORY="$repository_root/docker" \
OMNIWA_DRILL_ACTIVE_URL="$base_url" \
OMNIWA_DRILL_STANDBY_URL="http://$standby_published_address" \
OMNIWA_DRILL_EVIDENCE_FILE="$drill_temporary/evidence.json" \
OMNIWA_DRILL_API_KEY_FILE="$drill_temporary/api-key" \
OMNIWA_DRILL_EXPECTED_REVISION="$source_sha" \
OMNIWA_DRILL_TRAFFIC_DRAIN_PROBE=/usr/bin/true \
OMNIWA_DRILL_POST_PROMOTION_PROBE=/usr/bin/true \
OMNIWA_DRILL_POLL_SECONDS=1 \
OMNIWA_DRILL_OUTBOX_DRAIN_SECONDS=10 \
bash "$repository_root/scripts/ops/cold-standby-drill.sh" --execute
jq -e --arg revision "$source_sha" \
  '.status == "passed" and .recoveryRequired == false and .revision.after == $revision and .rto.observedSeconds <= .rto.limitSeconds' \
  "$drill_temporary/evidence.json" >/dev/null
rm -rf "$drill_temporary"
drill_temporary=""
refresh_runtime_coordinates
wait_for_liveness
assert_runtime_health_contract
assert_artifact_identity
assert_migrations
assert_auth_migrations
promoted_ownership_epoch="$(ownership_epoch)"
[[ "$promoted_ownership_epoch" -eq $((initial_ownership_epoch + 1)) ]]

if "${compose[@]}" run --rm --no-deps omniwa-go migrate; then
  echo "migration command acquired ownership while the application was active" >&2
  exit 1
fi

"${compose[@]}" restart omniwa-go
refresh_runtime_coordinates
wait_for_liveness
assert_runtime_health_contract
assert_artifact_identity
assert_migrations
assert_auth_migrations
restarted_ownership_epoch="$(ownership_epoch)"
[[ "$restarted_ownership_epoch" -eq $((promoted_ownership_epoch + 1)) ]]

echo "container smoke test passed for revision $source_sha with migrations $expected_migrations and ownership epoch $restarted_ownership_epoch"
