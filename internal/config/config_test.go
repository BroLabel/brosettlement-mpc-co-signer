package config_test

import (
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadMonolithDefaults(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MonolithURL != "https://monolith.test" {
		t.Errorf("got MonolithURL=%q, want https://monolith.test", cfg.MonolithURL)
	}
	if cfg.MaxConcurrent != 4 {
		t.Errorf("got MaxConcurrent=%d, want 4", cfg.MaxConcurrent)
	}
	if cfg.PollMinInterval != 2*time.Second {
		t.Errorf("got PollMinInterval=%s, want 2s", cfg.PollMinInterval)
	}
	if cfg.FramePollInterval != 500*time.Millisecond {
		t.Errorf("got FramePollInterval=%s, want 500ms", cfg.FramePollInterval)
	}
	if cfg.PartyID != "party-2" {
		t.Errorf("got PartyID=%q, want party-2", cfg.PartyID)
	}
}

func TestLoadAllowsOverridingPartyID(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-9")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.PartyID != "party-9" {
		t.Errorf("got PartyID=%q, want party-9", cfg.PartyID)
	}
}

func TestLoadRequiresSigningInputs(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing signing credentials")
	}
}

func TestLoadRequiresShareEncryptionKey(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing share encryption key")
	}
}
