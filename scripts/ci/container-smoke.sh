#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
compose_file="$repository_root/docker/docker-compose.smoke.yml"
source_sha="${SOURCE_SHA:-$(git -C "$repository_root" rev-parse HEAD)}"
smoke_version="${SMOKE_VERSION:-ci-smoke}"
smoke_api_key="${SMOKE_API_KEY:-omniwa-container-smoke-key}"
project_suffix="${GITHUB_RUN_ID:-local}-${GITHUB_RUN_ATTEMPT:-1}-${PPID}"
project_name="omniwa-smoke-${project_suffix//[^a-zA-Z0-9_-]/-}"
expected_migrations="20:20"

if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "SOURCE_SHA must be a full 40-character lowercase Git commit" >&2
  exit 1
fi

export SOURCE_SHA="$source_sha"
export SMOKE_VERSION="$smoke_version"
export SMOKE_API_KEY="$smoke_api_key"

compose=(docker compose --project-name "$project_name" --file "$compose_file")

cleanup() {
  "${compose[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
}
trap cleanup EXIT

"${compose[@]}" up --detach --build

refresh_runtime_coordinates() {
  container_id="$("${compose[@]}" ps --all --quiet omniwa-go)"
  published_address="$("${compose[@]}" port omniwa-go 4000 | tail -n 1)"
  base_url="http://${published_address}"
}

refresh_runtime_coordinates

wait_for_liveness() {
  local attempt
  for attempt in $(seq 1 60); do
    if curl --fail --silent "$base_url/server/ok" 2>/dev/null | jq -e '.status == "ok"' >/dev/null; then
      return 0
    fi
    sleep 2
  done
  "${compose[@]}" logs omniwa-go >&2
  echo "container did not become live within 120 seconds" >&2
  return 1
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

wait_for_liveness
assert_artifact_identity
assert_migrations

"${compose[@]}" restart omniwa-go
refresh_runtime_coordinates
wait_for_liveness
assert_artifact_identity
assert_migrations

echo "container smoke test passed for revision $source_sha with migrations $expected_migrations"
