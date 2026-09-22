#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION_FILE=${VERSION_FILE:-$ROOT/VERSION}

: "${REF:?REF is required}"
: "${IMAGE:?IMAGE is required}"
: "${REVISION:?REVISION is required}"

version=$(<"$VERSION_FILE")
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION must contain plain SemVer, got: '$version'" >&2
  exit 1
fi

case "$REF" in
  refs/heads/main)
    tags="$IMAGE:latest,$IMAGE:sha-$REVISION"
    ;;
  refs/heads/eks-staging)
    tags="$IMAGE:staging,$IMAGE:sha-$REVISION"
    ;;
  refs/tags/v*)
    if [[ "$REF" != "refs/tags/v$version" ]]; then
      echo "Git tag must match VERSION: got '$REF', expected 'refs/tags/v$version'" >&2
      exit 1
    fi
    tags="$IMAGE:v$version"
    ;;
  *)
    echo "Unsupported publication ref: $REF" >&2
    exit 1
    ;;
esac

printf 'version=%s\n' "$version"
printf 'revision=%s\n' "$REVISION"
printf 'tags=%s\n' "$tags"
