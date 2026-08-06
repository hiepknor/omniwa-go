#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
verification_script="$repository_root/scripts/ci/run-buildkite-verification.sh"
head_sha="$(git -C "$repository_root" rev-parse HEAD)"
canonical_repository="https://github.com/hiepknor/omniwa-go.git"

expect_failure() {
  local expected_message="$1"
  shift
  local output
  if output="$("$@" 2>&1)"; then
    echo "command unexpectedly succeeded" >&2
    exit 1
  fi
  if [[ "$output" != *"$expected_message"* ]]; then
    echo "failure did not contain expected message: $expected_message" >&2
    exit 1
  fi
}

expect_failure "not the trusted OmniWA GO repository" \
  env BUILDKITE_REPO=https://example.invalid/repository.git \
  BUILDKITE_COMMIT="$head_sha" \
  bash "$verification_script"

expect_failure "does not match the checked-out commit" \
  env BUILDKITE_REPO="$canonical_repository" \
  BUILDKITE_COMMIT=0000000000000000000000000000000000000000 \
  bash "$verification_script"

expect_failure "fork pull requests is disabled" \
  env BUILDKITE_REPO="$canonical_repository" \
  BUILDKITE_COMMIT="$head_sha" \
  BUILDKITE_PULL_REQUEST_REPO=https://github.com/example/fork.git \
  bash "$verification_script"

echo "Buildkite verification trust-boundary tests passed"
