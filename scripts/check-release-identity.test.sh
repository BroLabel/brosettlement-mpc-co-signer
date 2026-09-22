#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
CHECK="$ROOT/scripts/check-release-identity.sh"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

run_check() {
  REF=$1 \
  IMAGE=ghcr.io/brolabel/brosettlement-mpc-co-signer \
  REVISION=0123456789abcdef \
  VERSION_FILE=${2:-$ROOT/VERSION} \
    "$CHECK"
}

main_output=$(run_check refs/heads/main)
grep -Fx 'version=2.0.1' <<<"$main_output" >/dev/null
grep -Fx 'revision=0123456789abcdef' <<<"$main_output" >/dev/null
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:latest,ghcr.io/brolabel/brosettlement-mpc-co-signer:sha-0123456789abcdef' <<<"$main_output" >/dev/null

staging_output=$(run_check refs/heads/eks-staging)
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:staging,ghcr.io/brolabel/brosettlement-mpc-co-signer:sha-0123456789abcdef' <<<"$staging_output" >/dev/null

release_output=$(run_check refs/tags/v2.0.1)
grep -Fx 'tags=ghcr.io/brolabel/brosettlement-mpc-co-signer:v2.0.1' <<<"$release_output" >/dev/null

if run_check refs/tags/v2.0.2 >/dev/null 2>&1; then
  echo 'mismatched release tag was accepted' >&2
  exit 1
fi

printf 'not-semver\n' >"$TMP/VERSION"
if run_check refs/heads/main "$TMP/VERSION" >/dev/null 2>&1; then
  echo 'invalid VERSION was accepted' >&2
  exit 1
fi

echo 'PASS: release identity checks'
