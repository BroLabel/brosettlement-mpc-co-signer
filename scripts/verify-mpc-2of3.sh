#!/usr/bin/env sh
set -eu

if [ "$(uname -s)" != "Linux" ]; then
	echo "verify-mpc-2of3 requires Linux filesystem and lock evidence" >&2
	exit 1
fi

if grep -Eq '^replace[[:space:]]' go.mod; then
	echo "committed module replacements are forbidden" >&2
	exit 1
fi

core_version="$(GOWORK=off go list -m -f '{{.Version}}' github.com/BroLabel/brosettlement-mpc-core)"
if [ "$core_version" != "v0.3.0" ]; then
	echo "mpc-core must resolve exactly v0.3.0" >&2
	exit 1
fi

GOWORK=off go run ./cmd/mpc-contracts verify
GOWORK=off go test -race ./... -count=1
GOWORK=off go test ./internal/lifecycle -run '^TestLifetimeLock' -count=1
GOWORK=off go test ./internal/sharestore -run '^TestLinuxPublishCrash' -count=1
GOWORK=off go test ./internal/sharestore -run '^$' -fuzz '^FuzzInspectArtifactV1$' -fuzztime=10s

recovery_bin="$(mktemp -t mpc-recovery-proof.XXXXXX)"
production_bin="$(mktemp -t mpc-co-signer-production.XXXXXX)"
trap 'rm -f "$recovery_bin" "$production_bin"' EXIT HUP INT TERM
GOWORK=off go test -c -tags=mpc_recovery_test -o "$recovery_bin" ./internal/sharestore
"$recovery_bin" -test.run '^TestIsolatedRecoveryProof$'
GOWORK=off go build -o "$production_bin" ./cmd/co-signer
