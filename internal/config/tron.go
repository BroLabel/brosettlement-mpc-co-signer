package config

import (
	"os"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/chainrpc/tron"
)

func loadTronApprovalRPC(timeout time.Duration) (map[string]string, error) {
	endpoints := make(map[string]string)
	for chain, name := range map[string]string{"tron:mainnet": "CO_SIGNER_APPROVAL_TRON_MAINNET_RPC_URL", "tron:nile": "CO_SIGNER_APPROVAL_TRON_NILE_RPC_URL"} {
		if value := os.Getenv(name); value != "" {
			endpoints[chain] = value
		}
	}
	if _, err := tron.NewChecker(endpoints, timeout, nil); err != nil {
		return nil, err
	}
	return endpoints, nil
}
