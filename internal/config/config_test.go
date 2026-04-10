package config_test

import (
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "test-key")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != "0.0.0.0:50051" {
		t.Errorf("got GRPCAddr=%q, want 0.0.0.0:50051", cfg.GRPCAddr)
	}
	if cfg.HTTPAddr != "0.0.0.0:8081" {
		t.Errorf("got HTTPAddr=%q, want 0.0.0.0:8081", cfg.HTTPAddr)
	}
	if cfg.SharesDir != "./data/shares" {
		t.Errorf("got SharesDir=%q, want ./data/shares", cfg.SharesDir)
	}
}

func TestLoadMissingAPIKey(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestLoadMissingPartyID(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "test-key")
	t.Setenv("CO_SIGNER_PARTY_ID", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing party ID")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "p2")
	t.Setenv("CO_SIGNER_GRPC_ADDR", "127.0.0.1:9090")
	t.Setenv("CO_SIGNER_HTTP_ADDR", "127.0.0.1:9091")
	t.Setenv("CO_SIGNER_SHARES_DIR", "/tmp/shares")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != "127.0.0.1:9090" {
		t.Errorf("got %q, want 127.0.0.1:9090", cfg.GRPCAddr)
	}
	if cfg.SharesDir != "/tmp/shares" {
		t.Errorf("got %q, want /tmp/shares", cfg.SharesDir)
	}
	if cfg.APIKey != "secret" {
		t.Error("APIKey not loaded")
	}
	if cfg.PartyID != "p2" {
		t.Error("PartyID not loaded")
	}
}
