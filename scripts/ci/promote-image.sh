#!/usr/bin/env bash

set -euo pipefail

image_name="${1:?image name is required}"
release_tag="${2:?release tag is required}"
repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"

if [[ ! "$release_tag" =~ ^v?[0-9]+\.[0-9]+\.[0-9]+([.-][0-9A-Za-z.-]+)?$ ]]; then
  echo "release tag must be a semantic version" >&2
  exit 1
fi

expected_version="$(tr -d '[:space:]' < "$repository_root/VERSION")"
if [[ "${release_tag#v}" != "$expected_version" ]]; then
  echo "release tag $release_tag does not match VERSION $expected_version" >&2
  exit 1
fi

source_sha="$(git -C "$repository_root" rev-list --max-count=1 "${release_tag}^{commit}")"
if [[ ! "$source_sha" =~ ^[0-9a-f]{40}$ ]]; then
  echo "release tag does not resolve to a full Git commit" >&2
  exit 1
fi

source_ref="$image_name:sha-$source_sha"
digest="$(docker buildx imagetools inspect "$source_ref" | awk '$1 == "Digest:" { print $2; exit }')"
if [[ ! "$digest" =~ ^sha256:[0-9a-f]{64}$ ]]; then
  echo "immutable source image is missing for $source_ref" >&2
  exit 1
fi

bash "$repository_root/scripts/ci/verify-image.sh" "$image_name@$digest" "$source_sha" "$expected_version"

alias_ref="$image_name:$release_tag"
if existing_output="$(docker buildx imagetools inspect "$alias_ref" 2>/dev/null)"; then
  existing_digest="$(awk '$1 == "Digest:" { print $2; exit }' <<<"$existing_output")"
  if [[ "$existing_digest" != "$digest" ]]; then
    echo "release alias $alias_ref already points to a different digest" >&2
    exit 1
  fi
  echo "release alias $alias_ref already points to $digest"
else
  docker buildx imagetools create --tag "$alias_ref" "$image_name@$digest"
fi

promoted_digest="$(docker buildx imagetools inspect "$alias_ref" | awk '$1 == "Digest:" { print $2; exit }')"
if [[ "$promoted_digest" != "$digest" ]]; then
  echo "promoted alias digest mismatch: expected $digest, got $promoted_digest" >&2
  exit 1
fi

echo "promoted $alias_ref to existing digest $digest from revision $source_sha"
