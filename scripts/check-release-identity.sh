#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
VERSION_FILE=${VERSION_FILE:-$ROOT/VERSION}
REPOSITORY_DIR=${REPOSITORY_DIR:-$ROOT}
: "${REF:?REF is required}"
: "${IMAGE:?IMAGE is required}"
: "${REVISION:?REVISION is required}"
version=$(<"$VERSION_FILE")
if [[ ! "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]; then
  echo "VERSION must contain plain SemVer, got: '$version'" >&2
  exit 1
fi
prerelease=false
case "$REF" in
  refs/heads/main) tags="$IMAGE:sha-$REVISION" ;;
  refs/heads/eks-staging) tags="$IMAGE:staging,$IMAGE:sha-$REVISION" ;;
  refs/tags/v*)
    release_version=${REF#refs/tags/v}
    if [[ "$release_version" == "$version" ]]; then
      branch=main
    elif [[ "$release_version" == "$version"-rc.* && "${release_version#"$version"-rc.}" =~ ^[1-9][0-9]*$ ]]; then
      branch=eks-staging
      prerelease=true
    else
      echo "Tag must be v$version or v$version-rc.N (N >= 1)" >&2
      exit 1
    fi
    expected=$(git -C "$REPOSITORY_DIR" rev-parse "refs/remotes/origin/$branch")
    if [[ "$REVISION" != "$expected" ]]; then
      echo "Release must use the current origin/$branch commit" >&2
      exit 1
    fi
    version=$release_version
    tags="$IMAGE:v$version"
    ;;
  *) echo "Unsupported publication ref: $REF" >&2; exit 1 ;;
esac
printf 'version=%s\nrevision=%s\ntags=%s\nprerelease=%s\n' "$version" "$REVISION" "$tags" "$prerelease"
