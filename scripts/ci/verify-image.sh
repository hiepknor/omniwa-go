#!/usr/bin/env bash

set -euo pipefail

image_ref="${1:?image digest reference is required}"
expected_revision="${2:?expected source revision is required}"
expected_version="${3:?expected version is required}"

if [[ ! "$image_ref" =~ @sha256:[0-9a-f]{64}$ ]]; then
  echo "image reference must use an immutable sha256 digest" >&2
  exit 1
fi
if [[ ! "$expected_revision" =~ ^[0-9a-f]{40}$ ]]; then
  echo "expected revision must be a full 40-character lowercase Git SHA" >&2
  exit 1
fi

docker pull --quiet --platform linux/amd64 "$image_ref" >/dev/null
actual_revision="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.revision" }}' "$image_ref")"
actual_version="$(docker image inspect --format '{{ index .Config.Labels "org.opencontainers.image.version" }}' "$image_ref")"
runtime_user="$(docker image inspect --format '{{ .Config.User }}' "$image_ref")"

if [[ "$actual_revision" != "$expected_revision" ]]; then
  echo "OCI revision mismatch: expected $expected_revision, got $actual_revision" >&2
  exit 1
fi
if [[ "$actual_version" != "$expected_version" ]]; then
  echo "OCI version mismatch: expected $expected_version, got $actual_version" >&2
  exit 1
fi
if [[ "$runtime_user" != "10001:10001" ]]; then
  echo "runtime user mismatch: expected 10001:10001, got $runtime_user" >&2
  exit 1
fi

echo "verified immutable image $image_ref at revision $expected_revision"
