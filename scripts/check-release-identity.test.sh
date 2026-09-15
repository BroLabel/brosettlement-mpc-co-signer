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
  local version_file=${3:-$ROOT/VERSION}
  local output_file="$TMP/output"
  local calls_file="$TMP/calls"
  : > "$output_file"
  : > "$calls_file"
  cat > "$TMP/image-inspect" <<'STUB'
#!/usr/bin/env bash
printf '%s\n' "$*" >> "$INSPECT_CALLS_FILE"
case "$INSPECT_BEHAVIOR" in
  missing) printf 'manifest unknown\n' >&2; exit 1 ;;
  missing-image) printf '%s: not found\n' "$1" >&2; exit 1 ;;
  missing-error) printf 'ERROR: %s: not found\n' "$1" >&2; exit 1 ;;
  missing-blank) printf 'manifest unknown\n\n' >&2; exit 1 ;;
  mixed) printf 'manifest unknown\nauthorization token expired\n' >&2; exit 1 ;;
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
  "$CHECK" \
    --ref "$ref" \
    --image ghcr.io/brolabel/brosettlement-mpc-co-signer \
    --revision 0123456789abcdef \
    --version-file "$version_file" \
    --output "$output_file"
}

VERSION_FILE="$TMP/VERSION"
printf '2.0.0\n' > "$VERSION_FILE"

run_check refs/heads/main unavailable "$VERSION_FILE"
grep -Fx 'route=main' "$TMP/output" >/dev/null || fail "main route not selected"
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:latest,ghcr.io/brolabel/brosettlement-mpc-co-signer:sha-0123456789abcdef' "$TMP/output" >/dev/null || fail "main tags are not latest plus immutable SHA"
test ! -s "$TMP/calls" || fail "main route inspected the release registry tag"

run_check refs/tags/v2.0.0 missing "$VERSION_FILE"
grep -Fx 'route=release' "$TMP/output" >/dev/null || fail "release route not selected"
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:v2.0.0' "$TMP/output" >/dev/null || fail "release route did not select the canonical tag"
grep -Fx 'ghcr.io/brolabel/brosettlement-mpc-co-signer:v2.0.0' "$TMP/calls" >/dev/null || fail "canonical release tag was not inspected"
run_check refs/tags/v2.0.0 missing-image "$VERSION_FILE"
run_check refs/tags/v2.0.0 missing-error "$VERSION_FILE"

if run_check refs/tags/v2.0.0 mixed "$VERSION_FILE"; then
  fail "mixed absence and authorization output was accepted"
fi
if run_check refs/tags/v2.0.0 missing-blank "$VERSION_FILE"; then
  fail "multiline absence output was accepted"
fi

if run_check refs/tags/v2.0.1 missing "$VERSION_FILE"; then
  fail "mismatched Git tag was accepted"
fi
printf 'v2.0.0\n' > "$VERSION_FILE"
if run_check refs/tags/v2.0.0 missing "$VERSION_FILE"; then
  fail "non-plain semver VERSION was accepted"
fi
printf '2.0.0\n' > "$VERSION_FILE"
for invalid_version in 01.2.3 1.02.3 1.2.03; do
  printf '%s\n' "$invalid_version" > "$VERSION_FILE"
  if run_check "refs/tags/v$invalid_version" missing "$VERSION_FILE"; then
    fail "leading-zero VERSION $invalid_version was accepted"
  fi
done
for valid_version in 0.0.0 0.10.0 10.0.1; do
  printf '%s\n' "$valid_version" > "$VERSION_FILE"
  run_check "refs/tags/v$valid_version" missing "$VERSION_FILE"
done
printf '2.0.0\n\n' > "$VERSION_FILE"
if run_check refs/tags/v2.0.0 missing "$VERSION_FILE"; then
  fail "multiline VERSION was accepted"
fi
printf '2.0.0\n' > "$VERSION_FILE"
if INSPECT_CALLS_FILE="$TMP/calls" INSPECT_BEHAVIOR=missing RELEASE_IMAGE_INSPECT_CMD="$TMP/image-inspect" \
  "$CHECK" --ref refs/tags/v2.0.0 --image ghcr.io/brolabel/brosettlement-mpc-co-signer \
    --revision $'0123456789abcdef\nsecond-line' --version-file "$VERSION_FILE" --output "$TMP/output"; then
  fail "multiline revision was accepted"
fi

if run_check refs/tags/v2.0.0 existing "$VERSION_FILE"; then
  fail "existing release image was accepted"
fi
if run_check refs/tags/v2.0.0 unavailable "$VERSION_FILE"; then
  fail "unavailable registry evidence was accepted"
fi
if run_check refs/tags/v2.0.0 ambiguous "$VERSION_FILE"; then
  fail "ambiguous registry evidence was accepted"
fi

printf 'PASS: release identity checks\n'
