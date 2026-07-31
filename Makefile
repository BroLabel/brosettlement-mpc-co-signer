.PHONY: sync-contracts verify-contracts

sync-contracts:
	./scripts/sync-contracts.sh

verify-contracts:
	GOWORK=off go run ./cmd/mpc-contracts verify
