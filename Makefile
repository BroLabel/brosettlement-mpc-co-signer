.PHONY: build verify-contracts build-recovery-proof verify-recovery-proof verify-mpc-2of3

VERSION := $(shell cat VERSION)
REVISION := $(shell git rev-parse --verify HEAD)

build:
	@mkdir -p bin
	GOWORK=off go build -trimpath \
		-ldflags="-s -w -X main.version=$(VERSION) -X main.revision=$(REVISION)" \
		-o ./bin/co-signer ./cmd/co-signer

verify-contracts:
	GOWORK=off go run ./cmd/mpc-contracts verify

build-recovery-proof:
	@test -n "$$MPC_RECOVERY_TEST_BIN" || { echo "MPC_RECOVERY_TEST_BIN is required" >&2; exit 1; }
	GOWORK=off go test -c -tags=mpc_recovery_test -o "$$MPC_RECOVERY_TEST_BIN" ./internal/sharestore

verify-recovery-proof: build-recovery-proof
	"$$MPC_RECOVERY_TEST_BIN" -test.run '^TestIsolatedRecoveryProof$$'

verify-mpc-2of3:
	GOWORK=off ./scripts/verify-mpc-2of3.sh
