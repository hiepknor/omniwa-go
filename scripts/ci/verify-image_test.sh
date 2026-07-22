#!/usr/bin/env bash

set -euo pipefail

repository_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
verifier="$repository_root/scripts/ci/verify-image.sh"
fixture_path="$repository_root/scripts/ci/testdata"
image_ref="example.invalid/omniwa@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
revision="cccccccccccccccccccccccccccccccccccccccc"

PATH="$fixture_path:$PATH" bash "$verifier" "$image_ref" "$revision" 1.2.3 >/dev/null
FAKE_IMAGE_SCENARIO=single PATH="$fixture_path:$PATH" bash "$verifier" "$image_ref" "$revision" 1.2.3 >/dev/null

if PATH="$fixture_path:$PATH" bash "$verifier" "$image_ref" 0000000000000000000000000000000000000000 1.2.3 >/dev/null 2>&1; then
  echo "verify-image accepted a mismatched revision" >&2
  exit 1
fi
if FAKE_IMAGE_SCENARIO=duplicate PATH="$fixture_path:$PATH" bash "$verifier" "$image_ref" "$revision" 1.2.3 >/dev/null 2>&1; then
  echo "verify-image accepted duplicate linux/amd64 manifests" >&2
  exit 1
fi
if FAKE_IMAGE_SCENARIO=wrong-platform PATH="$fixture_path:$PATH" bash "$verifier" "$image_ref" "$revision" 1.2.3 >/dev/null 2>&1; then
  echo "verify-image accepted the wrong platform config" >&2
  exit 1
fi
