package config_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadMonolithDefaults(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("CO_SIGNER_POLL_MAX_INTERVAL", "")

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
	if cfg.PollMaxInterval != 10*time.Second {
		t.Errorf("got PollMaxInterval=%s, want 10s", cfg.PollMaxInterval)
	}
	if cfg.FramePollInterval != 500*time.Millisecond {
		t.Errorf("got FramePollInterval=%s, want 500ms", cfg.FramePollInterval)
	}
	if cfg.PartyID != "co-signer" {
		t.Errorf("got PartyID=%q, want co-signer", cfg.PartyID)
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

func TestLoadUsesRenderPortWhenHTTPAddrIsUnset(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("PORT", "10000")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.HTTPAddr != "0.0.0.0:10000" {
		t.Errorf("got HTTPAddr=%q, want 0.0.0.0:10000", cfg.HTTPAddr)
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

func TestLoadReadsDotEnvWhenPresent(t *testing.T) {
	dir := t.TempDir()

	content := []byte("" +
		"CO_SIGNER_MONOLITH_URL=https://monolith.test\n" +
		"CO_SIGNER_API_KEY_ID=key-1\n" +
		"CO_SIGNER_API_PRIVATE_KEY=cHJpdmF0ZS1rZXk=\n" +
		"CO_SIGNER_SHARE_ENCRYPTION_KEY=share-secret\n")
	if err := os.WriteFile(filepath.Join(dir, ".env"), content, 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatalf("restore cwd: %v", err)
		}
	})

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MonolithURL != "https://monolith.test" {
		t.Errorf("got MonolithURL=%q, want https://monolith.test", cfg.MonolithURL)
	}
	if cfg.APIKeyID != "key-1" {
		t.Errorf("got APIKeyID=%q, want key-1", cfg.APIKeyID)
	}
}
