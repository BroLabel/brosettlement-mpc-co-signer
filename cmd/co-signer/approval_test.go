package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/approval"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type idleApprovalBackend struct{ calls chan struct{} }

func (b idleApprovalBackend) PendingApprovals(context.Context, string, string) (approval.Page, error) {
	select {
	case b.calls <- struct{}{}:
	default:
	}
	return approval.Page{Version: 1, Items: []approval.Item{}}, nil
}
func (idleApprovalBackend) SendApprovalDecision(context.Context, string, []byte) ([]byte, error) {
	return nil, errors.New("unexpected delivery")
}
func TestApprovalStartupRequiresProvisionedDirectoryAndIndependentWorker(t *testing.T) {
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	key := ed25519.NewKeyFromSeed(make([]byte, 32))
	a := approval.Config{Mode: "auto_approve", OrgID: "org", Environment: "test", Revision: "rev", JournalDir: filepath.Join(dir, "missing"), MinInterval: time.Millisecond, MaxInterval: time.Second, Timeout: time.Second, BackoffFactor: 2}
	b := idleApprovalBackend{make(chan struct{}, 1)}
	cfg := config.Config{APIKeyID: "key", Approval: &a}
	if _, _, e := openApprovalWorker(cfg, key, b); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("missing storage: %v", e)
	}
	a.JournalDir = dir
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
	reopened, e := approval.OpenJournal(dir)
	if e != nil {
		t.Fatal(e)
	}
	reopened.Close()
	cfg.Approval = nil
	if j, d, e := openApprovalWorker(cfg, key, b); e != nil || j != nil || d != nil {
		t.Fatal("provisioning-only startup unexpectedly requires approval configuration")
	}
}
