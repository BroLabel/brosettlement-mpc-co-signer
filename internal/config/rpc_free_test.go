package config_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadIgnoresRemovedTronRPCEndpoints(t *testing.T) {
	setApprovalEnv(t)
	t.Setenv("CO_SIGNER_APPROVAL_TRON_MAINNET_RPC_URL", "not-a-url")
	t.Setenv("CO_SIGNER_APPROVAL_TRON_NILE_RPC_URL", "http://unused.invalid/synthetic-rpc-secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("removed RPC settings affected approval startup: %v", err)
	}
	if cfg.Approval == nil || cfg.Approval.Mode != "external" {
		t.Fatal("external approval configuration was lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", cfg), "synthetic-rpc-secret") {
		t.Fatal("removed RPC credential was retained in configuration")
	}
}
