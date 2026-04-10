package session_test

import (
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
)

func newTransport() *transport.StreamTransport {
	return transport.NewStreamTransport(8)
}

func TestCreateAndGet(t *testing.T) {
	store := session.NewStore()
	sess := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())

	got, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected session to be found")
	}
	if got.ID != sess.ID {
		t.Errorf("got ID=%q, want %q", got.ID, sess.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	store := session.NewStore()
	_, ok := store.Get("missing")
	if ok {
		t.Fatal("expected session not found")
	}
}

func TestCreateIdempotent(t *testing.T) {
	store := session.NewStore()
	s1 := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())
	s2 := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())

	if s1 != s2 {
		t.Fatal("expected same session returned for duplicate sessionId")
	}
}

func TestSetStateAndExpiredCleanup(t *testing.T) {
	store := session.NewStore()
	store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())
	store.SetState("sess-1", session.StateCompleted)

	got, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("session should still exist before cleanup")
	}
	if got.State != session.StateCompleted {
		t.Errorf("got state=%v, want Completed", got.State)
	}

	store.Cleanup(0)
	_, ok = store.Get("sess-1")
	if ok {
		t.Fatal("expected session to be removed after cleanup")
	}
}

func TestCleanupDoesNotRemoveRunning(t *testing.T) {
	store := session.NewStore()
	store.Create("sess-running", "org-1", time.Now().Add(time.Minute), newTransport())

	store.Cleanup(0)
	_, ok := store.Get("sess-running")
	if !ok {
		t.Fatal("running session should not be cleaned up")
	}
}
