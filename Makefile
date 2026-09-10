.PHONY: build-release verify-contracts build-recovery-proof verify-recovery-proof verify-mpc-2of3

verify-contracts:
	GOWORK=off go run ./cmd/mpc-contracts verify

build-release:
	@VERSION=$$(cat VERSION); \
	printf '%s' "$$VERSION" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "VERSION must be plain semver" >&2; exit 1; }; \
	mkdir -p bin; \
	GOWORK=off go build -trimpath -buildvcs=false -ldflags "-X main.version=$$VERSION" -o ./bin/co-signer ./cmd/co-signer

build-recovery-proof:
	@test -n "$$MPC_RECOVERY_TEST_BIN" || { echo "MPC_RECOVERY_TEST_BIN is required" >&2; exit 1; }
	GOWORK=off go test -c -tags=mpc_recovery_test -o "$$MPC_RECOVERY_TEST_BIN" ./internal/sharestore

verify-recovery-proof: build-recovery-proof
	"$$MPC_RECOVERY_TEST_BIN" -test.run '^TestIsolatedRecoveryProof$$'

verify-mpc-2of3:
	GOWORK=off ./scripts/verify-mpc-2of3.sh
