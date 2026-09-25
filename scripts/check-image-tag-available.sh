#!/usr/bin/env bash
set -euo pipefail
image=${1:?Versioned image reference is required}
if output=$(docker buildx imagetools inspect "$image" 2>&1); then
  echo "Image already exists; refusing to overwrite $image" >&2
  exit 1
fi
# Fail closed on authentication/network errors. Only an explicit absent manifest
# allows publication; a failed registry request is not proof that a tag is free.
if [[ "$output" == *"$image: not found"* || "$output" == *'manifest unknown'* ]]; then
  exit 0
fi
printf 'Cannot verify image availability: %s\n' "$output" >&2
exit 1
