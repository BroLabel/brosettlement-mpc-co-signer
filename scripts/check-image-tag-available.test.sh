#!/usr/bin/env bash
set -euo pipefail
ROOT=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT
# A controlled registry response avoids network and credentials in the test.
cat > "$TMP/docker" <<'DOCKER'
#!/usr/bin/env bash
[[ "$*" == 'buildx imagetools inspect example/co-signer:v3.4.5' ]] || exit 99
case "$REGISTRY_RESPONSE" in
  exists) echo 'Digest: sha256:abc'; exit 0 ;;
  missing) echo 'ERROR: example/co-signer:v3.4.5: not found' >&2; exit 1 ;;
  unauthorized) echo 'ERROR: unauthorized' >&2; exit 1 ;;
  unavailable) echo 'ERROR: connection timed out' >&2; exit 1 ;;
esac
DOCKER
chmod +x "$TMP/docker"
run() { PATH="$TMP:$PATH" REGISTRY_RESPONSE=$1 "$ROOT/scripts/check-image-tag-available.sh" example/co-signer:v3.4.5; }
run missing
for response in exists unauthorized unavailable; do
  if run "$response" > "$TMP/output" 2>&1; then
    echo "Unexpectedly accepted registry response: $response" >&2
    exit 1
  fi
done
echo 'PASS: existing images and uncertain registry failures block publication'
