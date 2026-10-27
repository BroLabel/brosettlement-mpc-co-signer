#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
git init --bare -q "$TMP/origin.git"
git clone -q "$TMP/origin.git" "$TMP/repo" 2>/dev/null
git -C "$TMP/repo" checkout -qb main
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm production --allow-empty
git -C "$TMP/repo" push -q origin main
production=$(git -C "$TMP/repo" rev-parse HEAD)
printf '3.4.5\n' > "$TMP/VERSION"
check() {
  REF=$1 REPOSITORY_DIR="$TMP/repo" bash "$ROOT/scripts/fetch-release-source.sh" >/dev/null 2>&1 || return
  REF=$1 REVISION=$2 VERSION_FILE="$TMP/VERSION" REPOSITORY_DIR="$TMP/repo" \
    IMAGE=example/co-signer bash "$ROOT/scripts/check-release-identity.sh"
}
if ! check refs/tags/v3.4.5 "$production" > "$TMP/output"; then
  echo 'Stable publication must work when the remote has no staging branch' >&2
  exit 1
fi
grep -Fx 'tags=example/co-signer:v3.4.5' "$TMP/output" >/dev/null
check refs/heads/main "$production" >/dev/null
if check refs/tags/v3.4.5-rc.1 "$production" >/dev/null 2>&1; then
  echo 'RC publication must refuse a missing staging source' >&2
  exit 1
fi
git -C "$TMP/repo" checkout -qb eks-staging
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm staging --allow-empty
git -C "$TMP/repo" push -q origin eks-staging
staging=$(git -C "$TMP/repo" rev-parse HEAD)
check refs/tags/v3.4.5-rc.1 "$staging" > "$TMP/output"
grep -Fx 'prerelease=true' "$TMP/output" >/dev/null
git -C "$TMP/repo" checkout -q main
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm newer-production --allow-empty
git -C "$TMP/repo" push -q origin main
# An outdated cached tracking ref must not authorize an old stable revision.
git -C "$TMP/repo" update-ref refs/remotes/origin/main "$production"
if check refs/tags/v3.4.5 "$production" >/dev/null 2>&1; then
  echo 'Stable publication must refresh its source before checking revision ownership' >&2
  exit 1
fi
echo 'PASS: independent stable source, required RC source, and fresh revision ownership'
