#!/bin/sh
set -eu

: "${CONTRACT_SOURCE:?CONTRACT_SOURCE must be an absolute signer bundle path}"
: "${HTTP_SOURCE:?HTTP_SOURCE must be an absolute backend HTTP fixture path}"

case "$CONTRACT_SOURCE" in /*) ;; *) echo "CONTRACT_SOURCE must be absolute" >&2; exit 1 ;; esac
case "$HTTP_SOURCE" in /*) ;; *) echo "HTTP_SOURCE must be absolute" >&2; exit 1 ;; esac

repository_root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$repository_root"
exec env GOWORK=off go run ./cmd/mpc-contracts sync \
  --contract-source "$CONTRACT_SOURCE" \
  --http-source "$HTTP_SOURCE"
