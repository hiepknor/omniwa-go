#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
source_sha="${SOURCE_SHA:-$(git -C "$repository_root" rev-parse HEAD)}"
head_sha="$(git -C "$repository_root" rev-parse HEAD)"

if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "SOURCE_SHA must be a full 40-character lowercase Git commit" >&2
  exit 1
fi
if [[ "$source_sha" != "$head_sha" ]]; then
  echo "SOURCE_SHA does not match the checked-out commit" >&2
  exit 1
fi
if [[ -z "${TEST_POSTGRES_DSN:-}" ]]; then
  echo "TEST_POSTGRES_DSN is required" >&2
  exit 1
fi
if [[ -z "${TEST_RABBITMQ_URL:-}" ]]; then
  echo "TEST_RABBITMQ_URL is required" >&2
  exit 1
fi

cd "$repository_root"

go build ./...
go vet ./...
go test ./...

# Packages share this integration database. Serial package execution prevents
# one package's projection worker from claiming another package's fixtures.
go test -p=1 ./... -count=1

go test \
  -run 'Test(PublishAndAwaitConfirmationWithRabbitMQ|DeliverConfirmedWithRabbitMQCarriesStableMessageID)' \
  -count=1 ./pkg/events/rabbitmq

go test -race ./...
go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

go run github.com/swaggo/swag/cmd/swag@v1.16.3 init \
  -g cmd/evolution-go/main.go \
  -o ./docs \
  --parseDependency \
  --parseInternal
git diff --exit-code -- docs

go run github.com/zricethezav/gitleaks/v8@v8.30.1 dir \
  --redact \
  --exit-code 1 \
  .

go run github.com/rhysd/actionlint/cmd/actionlint@v1.7.12
bash -n scripts/ci/*.sh
bash -n scripts/ops/*.sh
bash scripts/ci/run-buildkite-verification_test.sh
bash scripts/ci/cold-standby-drill_test.sh
if bash scripts/ci/verify-image.sh example.invalid/image:latest 0000000000000000000000000000000000000000 0.0.0 >/dev/null 2>&1; then
  echo "verify-image accepted a mutable tag" >&2
  exit 1
fi
bash scripts/ci/verify-image_test.sh
if bash scripts/ci/promote-image.sh example.invalid/image not-a-version >/dev/null 2>&1; then
  echo "promote-image accepted an invalid release tag" >&2
  exit 1
fi

bash scripts/ci/validate-production-manifests.sh

SOURCE_SHA="$source_sha" \
SMOKE_VERSION="${SMOKE_VERSION:-ci-smoke}" \
SMOKE_API_KEY="${SMOKE_API_KEY:-omniwa-container-smoke-key}" \
bash scripts/ci/container-smoke.sh
