#!/usr/bin/env bash
set -euo pipefail

usage() {
  echo "usage: $0 --ref REF --image IMAGE --revision SHA --version-file FILE --output FILE" >&2
  exit 2
}

ref=
image=
revision=
version_file=
output_file=
while (($#)); do
  case "$1" in
    --ref) ref=${2-}; shift 2 ;;
    --image) image=${2-}; shift 2 ;;
    --revision) revision=${2-}; shift 2 ;;
    --version-file) version_file=${2-}; shift 2 ;;
    --output) output_file=${2-}; shift 2 ;;
    *) usage ;;
  esac
done

[[ -n "$ref" && -n "$image" && -n "$revision" && -n "$version_file" && -n "$output_file" ]] || usage
[[ "$revision" =~ ^[0-9a-fA-F]{7,64}$ ]] || { echo "revision must be a Git SHA" >&2; exit 1; }
[[ -f "$version_file" ]] || { echo "VERSION file is unavailable" >&2; exit 1; }
version=$(cat "$version_file")
[[ "$version" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo "VERSION must be plain semver, got: '$version'" >&2; exit 1; }

publication_state=${RELEASE_PUBLICATION_STATE:-not-started}
if [[ "$publication_state" != "not-started" ]]; then
  echo "publication state is '$publication_state'; operator verification is required" >&2
  exit 1
fi

case "$ref" in
  refs/heads/main)
    route=main
    tags="$image:latest,$image:sha-$revision"
    ;;
  refs/tags/v*)
    expected_ref="refs/tags/v$version"
    [[ "$ref" == "$expected_ref" ]] || {
      echo "Git tag must exactly match v<VERSION>: got '$ref', expected '$expected_ref'" >&2
      exit 1
    }
    route=release
    release_image="$image:v$version"
    inspect_output_file=$(mktemp)
    trap 'rm -f "$inspect_output_file"' EXIT
    if [[ -n "${RELEASE_IMAGE_INSPECT_CMD:-}" ]]; then
      inspect_command=("$RELEASE_IMAGE_INSPECT_CMD")
    else
      inspect_command=(docker buildx imagetools inspect)
    fi
    if "${inspect_command[@]}" "$release_image" >"$inspect_output_file" 2>&1; then
      echo "release image already exists: $release_image" >&2
      exit 1
    fi
    inspect_output=$(cat "$inspect_output_file")
    if ! grep -Eiq 'manifest unknown' <<<"$inspect_output" &&
      ! grep -Fiq "$release_image: not found" <<<"$inspect_output"; then
      echo "registry evidence is unavailable or ambiguous for $release_image" >&2
      printf '%s\n' "$inspect_output" >&2
      exit 1
    fi
    tags=$release_image
    ;;
  *)
    echo "unsupported publication ref: $ref" >&2
    exit 1
    ;;
esac

{
  printf 'route=%s\n' "$route"
  printf 'version=%s\n' "$version"
  printf 'revision=%s\n' "$revision"
  printf 'tags=%s\n' "$tags"
} >> "$output_file"
