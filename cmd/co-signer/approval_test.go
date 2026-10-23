package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
)

func TestApprovalStartupWithoutApprovalEnvInitializesAndProtectsJournal(t *testing.T) {
	root := t.TempDir()
	primary := filepath.Join(root, "primary")
	recovery := filepath.Join(root, "recovery")
	for _, path := range []string{primary, recovery} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, value := range map[string]string{
		"CO_SIGNER_APPROVAL_MODE": "", "CO_SIGNER_APPROVAL_JOURNAL_DIR": "",
		"CO_SIGNER_APPROVAL_ENDPOINT": "", "CO_SIGNER_APPROVAL_BEARER_TOKEN": "",
		"CO_SIGNER_MONOLITH_URL": "https://monolith.test",
		"CO_SIGNER_API_KEY_ID":   "key", "CO_SIGNER_API_PRIVATE_KEY": "unused",
		"CO_SIGNER_PRIMARY_SHARES_DIR": primary, "CO_SIGNER_RECOVERY_SHARES_DIR": recovery,
		"CO_SIGNER_SHARE_ENCRYPTION_KEY":    base64.StdEncoding.EncodeToString(make([]byte, 32)),
		"CO_SIGNER_SHARE_ENCRYPTION_KEY_ID": "keyref-1",
	} {
		t.Setenv(name, value)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	backend := idleApprovalBackend{make(chan struct{}, 1)}
	journal, dispatcher, err := openApprovalWorker(cfg, key, backend)
	if err != nil {
		t.Fatalf("first startup without approval settings: %v", err)
	}
	if dispatcher == nil {
		t.Fatal("approval dispatcher missing")
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "approval")
	if cfg.Approval.JournalDir != want {
		t.Fatalf("journal outside existing storage: %s", cfg.Approval.JournalDir)
	}
	journal, _, err = openApprovalWorker(cfg, key, backend)
	if err != nil {
		t.Fatalf("restart: %v", err)
	}
	journal.Close()
	if err := os.RemoveAll(want); err != nil {
		t.Fatal(err)
	}
	if journal, _, err = openApprovalWorker(cfg, key, backend); err == nil {
		journal.Close()
		t.Fatal("lost journal was silently recreated")
	}
	if _, err := os.Stat(want); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed startup created replacement journal: %v", err)
	}
	if err := os.Mkdir(want, 0700); err != nil {
		t.Fatal(err)
	}
	if journal, _, err = openApprovalWorker(cfg, key, backend); err == nil {
		journal.Close()
		t.Fatal("empty replacement accepted after restart")
	}
}

type idleApprovalBackend struct{ calls chan struct{} }

func (b idleApprovalBackend) PendingApprovals(context.Context, string) (approval.Page, error) {
	select {
	case b.calls <- struct{}{}:
	default:
	}
	return approval.Page{Version: 1, Items: []approval.Item{}}, nil
}
func (idleApprovalBackend) SendApprovalDecision(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("unexpected delivery")
}
func TestExplicitApprovalJournalRequiresProvisionedDirectoryAndIndependentWorker(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	primary := filepath.Join(dir, "primary")
	if err := os.Mkdir(primary, 0700); err != nil {
		t.Fatal(err)
	}
	provider, err := sharestore.NewKeyProvider(base64.StdEncoding.EncodeToString(make([]byte, 32)), "keyref-1")
	if err != nil {
		t.Fatal(err)
	}
	store, err := sharestore.NewStoreConfig(sharestore.StorePurposePrimary, primary, provider)
	if err != nil {
		t.Fatal(err)
	}
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	a := approval.Config{Mode: "auto_approve", JournalDir: filepath.Join(dir, "missing"), MinInterval: time.Millisecond, MaxInterval: time.Second, Timeout: time.Second, BackoffFactor: 2}
	b := idleApprovalBackend{make(chan struct{}, 1)}
	cfg := config.Config{APIKeyID: "key", Approval: &a, PrimaryStore: store}
	if _, _, e := openApprovalWorker(cfg, key, b); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("missing storage: %v", e)
	}
	a.JournalDir = filepath.Join(dir, "explicit-journal")
	if err := os.Mkdir(a.JournalDir, 0700); err != nil {
		t.Fatal(err)
	}
	j, d, e := openApprovalWorker(cfg, key, b)
	if e != nil {
		t.Fatal(e)
	}
	r := &applicationResources{approvalJournal: j, approvalDispatcher: d}
	ctx, cancel := context.WithCancel(context.Background())
	r.startScheduler(ctx)
	select {
	case <-b.calls:
	case <-time.After(time.Second):
		cancel()
		t.Fatal("approval worker tied to SIGN scheduler")
	}
	cancel()
	if e = r.Close(); e != nil {
		t.Fatal(e)
	}
	// Closing owned background work releases its independent journal lock.
	reopened, e := approval.OpenManagedJournal(a.JournalDir, primary, false)
	if e != nil {
		t.Fatal(e)
	}
	reopened.Close()
	cfg.Approval = nil
	if j, d, e := openApprovalWorker(cfg, key, b); e != nil || j != nil || d != nil {
		t.Fatal("provisioning-only startup unexpectedly requires approval configuration")
	}
}
