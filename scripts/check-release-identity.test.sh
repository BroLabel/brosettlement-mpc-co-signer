#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHECK="$ROOT/scripts/check-release-identity.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

fail() {
  printf 'FAIL: %s\n' "$*" >&2
  exit 1
}

run_check() {
  local ref=$1
  local inspect_behavior=$2
  local publication_state=${3:-not-started}
  local version_file=${4:-$ROOT/VERSION}
  local output_file="$TMP/output"
  local calls_file="$TMP/calls"
  : > "$output_file"
  : > "$calls_file"
  cat > "$TMP/image-inspect" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$INSPECT_CALLS_FILE"
case "$INSPECT_BEHAVIOR" in
  missing) printf 'manifest unknown\n' >&2; exit 1 ;;
  existing) printf 'digest: sha256:existing\n'; exit 0 ;;
  unavailable) printf 'network error: registry server not found\n' >&2; exit 2 ;;
  ambiguous) printf 'unexpected inspect response\n' >&2; exit 1 ;;
  *) exit 9 ;;
esac
STUB
  chmod +x "$TMP/image-inspect"
  INSPECT_CALLS_FILE="$calls_file" \
  INSPECT_BEHAVIOR="$inspect_behavior" \
  RELEASE_IMAGE_INSPECT_CMD="$TMP/image-inspect" \
  RELEASE_PUBLICATION_STATE="$publication_state" \
  "$CHECK" \
    --ref "$ref" \
    --image ghcr.io/brolabel/brosettlement-mpc-co-signer \
    --revision 0123456789abcdef \
    --version-file "$version_file" \
    --output "$output_file"
}

VERSION_FILE="$TMP/VERSION"
printf '2.0.0\n' > "$VERSION_FILE"

run_check refs/heads/main unavailable not-started "$VERSION_FILE"
grep -Fx 'route=main' "$TMP/output" >/dev/null || fail "main route not selected"
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:latest,ghcr.io/brolabel/brosettlement-mpc-co-signer:sha-0123456789abcdef' "$TMP/output" >/dev/null || fail "main tags are not latest plus immutable SHA"
test ! -s "$TMP/calls" || fail "main route inspected the release registry tag"

run_check refs/tags/v2.0.0 missing not-started "$VERSION_FILE"
grep -Fx 'route=release' "$TMP/output" >/dev/null || fail "release route not selected"
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:v2.0.0' "$TMP/output" >/dev/null || fail "release route did not select the canonical tag"
grep -Fx 'ghcr.io/brolabel/brosettlement-mpc-co-signer:v2.0.0' "$TMP/calls" >/dev/null || fail "canonical release tag was not inspected"

if run_check refs/tags/v2.0.1 missing not-started "$VERSION_FILE"; then
  fail "mismatched Git tag was accepted"
fi
printf 'v2.0.0\n' > "$VERSION_FILE"
if run_check refs/tags/v2.0.0 missing not-started "$VERSION_FILE"; then
  fail "non-plain semver VERSION was accepted"
fi
printf '2.0.0\n' > "$VERSION_FILE"
if run_check refs/tags/v2.0.0 existing not-started "$VERSION_FILE"; then
  fail "existing release image was accepted"
fi
if run_check refs/tags/v2.0.0 unavailable not-started "$VERSION_FILE"; then
  fail "unavailable registry evidence was accepted"
fi
if run_check refs/tags/v2.0.0 ambiguous not-started "$VERSION_FILE"; then
  fail "ambiguous registry evidence was accepted"
fi
if run_check refs/tags/v2.0.0 missing ambiguous "$VERSION_FILE"; then
  fail "ambiguous publication state was accepted"
fi

printf 'PASS: release identity checks\n'
