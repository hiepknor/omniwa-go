#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
expected_repository_https="https://github.com/hiepknor/omniwa-go.git"
expected_repository_ssh="git@github.com:hiepknor/omniwa-go.git"
buildkite_repository="${BUILDKITE_REPO:-}"
buildkite_commit="${BUILDKITE_COMMIT:-}"
pull_request_repository="${BUILDKITE_PULL_REQUEST_REPO:-}"

repository_is_expected() {
  [[ "$1" == "$expected_repository_https" || "$1" == "$expected_repository_ssh" ]]
}

if ! repository_is_expected "$buildkite_repository"; then
  echo "Buildkite repository is not the trusted OmniWA GO repository" >&2
  exit 1
fi
if [[ ! "$buildkite_commit" =~ ^[0-9a-f]{40}$ ]]; then
  echo "BUILDKITE_COMMIT must be a full 40-character lowercase Git commit" >&2
  exit 1
fi
if [[ -n "$pull_request_repository" ]] && ! repository_is_expected "$pull_request_repository"; then
  echo "Verification of fork pull requests is disabled on the trusted agent queue" >&2
  exit 1
fi
if [[ "$(git -C "$repository_root" rev-parse HEAD)" != "$buildkite_commit" ]]; then
  echo "BUILDKITE_COMMIT does not match the checked-out commit" >&2
  exit 1
fi

job_suffix="${BUILDKITE_JOB_ID:-local}-${PPID}"
job_suffix="${job_suffix//[^a-zA-Z0-9_.-]/-}"
postgres_container="omniwa-ci-postgres-$job_suffix"
rabbitmq_container="omniwa-ci-rabbitmq-$job_suffix"

cleanup() {
  docker rm --force "$postgres_container" "$rabbitmq_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

docker run --detach --rm --name "$postgres_container" \
  --env POSTGRES_USER=postgres \
  --env POSTGRES_PASSWORD=postgres \
  --env POSTGRES_DB=omniwa_ci \
  --publish 127.0.0.1::5432 \
  --health-cmd 'pg_isready -U postgres -d omniwa_ci' \
  --health-interval 5s \
  --health-timeout 5s \
  --health-retries 10 \
  postgres:15-alpine >/dev/null

docker run --detach --rm --name "$rabbitmq_container" \
  --env RABBITMQ_DEFAULT_USER=omniwa \
  --env RABBITMQ_DEFAULT_PASS=omniwa \
  --env RABBITMQ_DEFAULT_VHOST=omniwa_ci \
  --publish 127.0.0.1::5672 \
  --health-cmd 'rabbitmq-diagnostics -q ping' \
  --health-interval 5s \
  --health-timeout 5s \
  --health-retries 12 \
  rabbitmq:3-management-alpine >/dev/null

for container in "$postgres_container" "$rabbitmq_container"; do
  for attempt in $(seq 1 60); do
    health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}missing{{end}}' "$container")"
    if [[ "$health" == "healthy" ]]; then
      break
    fi
    if [[ "$health" == "unhealthy" || "$attempt" == "60" ]]; then
      docker logs "$container" >&2
      echo "$container did not become healthy" >&2
      exit 1
    fi
    sleep 2
  done
done

postgres_address="$(docker port "$postgres_container" 5432/tcp | tail -n 1)"
rabbitmq_address="$(docker port "$rabbitmq_container" 5672/tcp | tail -n 1)"

SOURCE_SHA="$buildkite_commit" \
TEST_POSTGRES_DSN="postgresql://postgres:postgres@${postgres_address}/omniwa_ci?sslmode=disable" \
TEST_RABBITMQ_URL="amqp://omniwa:omniwa@${rabbitmq_address}/omniwa_ci" \
bash "$repository_root/scripts/ci/verify-repository.sh"
