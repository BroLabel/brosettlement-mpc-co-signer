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
[[ "$revision" != *$'\n'* && "$revision" =~ ^[0-9a-fA-F]{7,64}$ ]] || { echo "revision must be a single-line Git SHA" >&2; exit 1; }
[[ -f "$version_file" ]] || { echo "VERSION file is unavailable" >&2; exit 1; }
version_lines=$(awk 'END { print NR }' "$version_file")
version=$(awk 'NR == 1 { print; exit }' "$version_file")
[[ "$version_lines" == "1" && "$version" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]] || {
  echo "VERSION must be a single-line plain SemVer without leading zeros, got: '$version'" >&2
  exit 1
}

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
    canonical_absence=false
    for expected in "manifest unknown" "$release_image: not found" "ERROR: $release_image: not found"; do
      if cmp -s "$inspect_output_file" <(printf '%s' "$expected") ||
        cmp -s "$inspect_output_file" <(printf '%s\n' "$expected"); then
        canonical_absence=true
        break
      fi
    done
    if [[ "$canonical_absence" != "true" ]]; then
      echo "registry evidence is unavailable or ambiguous for $release_image" >&2
      cat "$inspect_output_file" >&2
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
