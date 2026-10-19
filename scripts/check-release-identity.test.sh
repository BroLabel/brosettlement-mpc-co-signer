#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir "$TMP/repo"
git -C "$TMP/repo" init -q
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm production --allow-empty
production=$(git -C "$TMP/repo" rev-parse HEAD)
git -C "$TMP/repo" update-ref refs/remotes/origin/main "$production"
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm staging --allow-empty
staging=$(git -C "$TMP/repo" rev-parse HEAD)
git -C "$TMP/repo" update-ref refs/remotes/origin/eks-staging "$staging"
printf '3.4.5\n' > "$TMP/VERSION"
check() {
  REF=$1 REVISION=$2 VERSION_FILE="$TMP/VERSION" REPOSITORY_DIR="$TMP/repo" \
    IMAGE=example/co-signer "$ROOT/scripts/check-release-identity.sh"
}
expect_line() {
  if ! grep -Fx "$2" <<< "$1" >/dev/null; then
    printf 'Missing output: %s\nActual:\n%s\n' "$2" "$1" >&2
    exit 1
  fi
}
reject() {
  if check "$1" "$2" >"$TMP/output" 2>&1; then
    echo "Unexpectedly accepted $1 at $2" >&2
    exit 1
  fi
}
output=$(check refs/heads/main "$production")
expect_line "$output" "version=3.4.5-dev+${production:0:12}"
expect_line "$output" "revision=$production"
expect_line "$output" "tags=example/co-signer:sha-$production"
output=$(check refs/heads/eks-staging "$staging")
expect_line "$output" "version=3.4.5-dev+${staging:0:12}"
expect_line "$output" "revision=$staging"
expect_line "$output" "tags=example/co-signer:staging,example/co-signer:sha-$staging"
git -C "$TMP/repo" tag v3.4.5 "$production"
output=$(check refs/tags/v3.4.5 "$production")
expect_line "$output" 'version=3.4.5'
expect_line "$output" "revision=$production"
expect_line "$output" 'tags=example/co-signer:v3.4.5'
expect_line "$output" 'prerelease=false'
output=$(check refs/tags/v3.4.5-rc.1 "$staging")
expect_line "$output" 'version=3.4.5-rc.1'
expect_line "$output" 'tags=example/co-signer:v3.4.5-rc.1'
expect_line "$output" 'prerelease=true'
# A branch build stays development even when its commit also owns a release tag.
output=$(check refs/heads/main "$production")
expect_line "$output" "version=3.4.5-dev+${production:0:12}"
expect_line "$output" "revision=$production"
for branch in main eks-staging; do
  reject "refs/heads/$branch" "${production:0:12}"
  reject "refs/heads/$branch" 'ABCDEF0123456789abcdef0123456789abcdef0123'
  reject "refs/heads/$branch" 'g123456789abcdef0123456789abcdef012345678'
  reject "refs/heads/$branch" "${production}0"
done
reject refs/tags/v3.4.5 "$staging"
reject refs/tags/v3.4.5-rc.1 "$production"
reject refs/tags/v3.4.4 "$production"
reject refs/tags/v3.4.5-rc.0 "$staging"
reject refs/tags/v3.4.5-rc.01 "$staging"
reject refs/heads/feature "$staging"
# An old main commit must not roll back the stable channel on a delayed run.
git -C "$TMP/repo" update-ref refs/remotes/origin/main "$staging"
reject refs/tags/v3.4.5 "$production"
printf '03.4.5\n' > "$TMP/VERSION"
reject refs/heads/main "$production"
echo 'PASS: release channels, version validation and branch ownership'
