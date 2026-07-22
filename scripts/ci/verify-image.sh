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

command -v docker >/dev/null
command -v jq >/dev/null

# Inspect registry content directly. Pulling an OCI index by digest and then
# calling `docker image inspect` is not portable across Docker image stores: a
# daemon may retain the index object instead of resolving the requested
# platform, leaving `.Config` empty even though the platform manifests are
# correct.
raw_manifest="$(docker buildx imagetools inspect --raw "$image_ref")"
media_type="$(jq -r '.mediaType // empty' <<<"$raw_manifest")"
platform_ref="$image_ref"
case "$media_type" in
  application/vnd.oci.image.index.v1+json|application/vnd.docker.distribution.manifest.list.v2+json)
    platform_digest="$(jq -r '
      [.manifests[] | select(.platform.os == "linux" and .platform.architecture == "amd64") | .digest]
      | if length == 1 then .[0] else empty end
    ' <<<"$raw_manifest")"
    if [[ ! "$platform_digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
      echo "image index must contain exactly one linux/amd64 manifest" >&2
      exit 1
    fi
    platform_ref="${image_ref%@*}@$platform_digest"
    ;;
  application/vnd.oci.image.manifest.v1+json|application/vnd.docker.distribution.manifest.v2+json)
    ;;
  *)
    echo "unsupported image manifest media type: $media_type" >&2
    exit 1
    ;;
esac

image_config="$(docker buildx imagetools inspect --format '{{json .Image}}' "$platform_ref")"
actual_architecture="$(jq -r '.architecture // empty' <<<"$image_config")"
actual_os="$(jq -r '.os // empty' <<<"$image_config")"
actual_revision="$(jq -r '.config.Labels["org.opencontainers.image.revision"] // empty' <<<"$image_config")"
actual_version="$(jq -r '.config.Labels["org.opencontainers.image.version"] // empty' <<<"$image_config")"
runtime_user="$(jq -r '.config.User // empty' <<<"$image_config")"

if [[ "$actual_os/$actual_architecture" != "linux/amd64" ]]; then
  echo "platform mismatch: expected linux/amd64, got $actual_os/$actual_architecture" >&2
  exit 1
fi

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
