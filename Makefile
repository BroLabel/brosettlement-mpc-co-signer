.PHONY: sync-contracts verify-contracts build-recovery-proof verify-recovery-proof verify-mpc-2of3

sync-contracts:
	./scripts/sync-contracts.sh

verify-contracts:
	GOWORK=off go run ./cmd/mpc-contracts verify

build-recovery-proof:
	@test -n "$$MPC_RECOVERY_TEST_BIN" || { echo "MPC_RECOVERY_TEST_BIN is required" >&2; exit 1; }
	GOWORK=off go test -c -tags=mpc_recovery_test -o "$$MPC_RECOVERY_TEST_BIN" ./internal/sharestore

verify-recovery-proof: build-recovery-proof
	"$$MPC_RECOVERY_TEST_BIN" -test.run '^TestIsolatedRecoveryProof$$'

verify-mpc-2of3:
	GOWORK=off ./scripts/verify-mpc-2of3.sh
