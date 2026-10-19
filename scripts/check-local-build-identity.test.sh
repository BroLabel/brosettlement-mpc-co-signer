#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
mkdir "$TMP/repo"
cp "$ROOT/Makefile" "$TMP/repo/Makefile"
printf '3.4.5\n' > "$TMP/repo/VERSION"
git -C "$TMP/repo" init -q
git -C "$TMP/repo" add Makefile VERSION
git -C "$TMP/repo" -c user.name=Test -c user.email=test@example.com commit -qm fixture
revision=$(git -C "$TMP/repo" rev-parse HEAD)
check_build() {
  local suffix=$1 output
  output=$(cd "$TMP/repo" && make --no-print-directory -n build)
  for expected in "-X main.version=3.4.5-dev+${revision:0:12}$suffix" "-X main.revision=$revision"; do
    if [[ "$output" != *"$expected"* ]]; then
      printf 'Missing build metadata: %s\nActual:\n%s\n' "$expected" "$output" >&2
      exit 1
    fi
  done
}
check_build ''
# Keep the base version fixed while making the tracked checkout dirty.
printf '# Modified checkout\n' >> "$TMP/repo/Makefile"
check_build '.dirty'
echo 'PASS: local builds use development identity for clean and dirty checkouts'
