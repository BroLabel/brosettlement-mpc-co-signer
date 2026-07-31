package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

type stubPendingClient struct {
	mu         sync.Mutex
	intents    []monolith.Intent
	claimCalls int
	pollCalls  int
}

func (s *stubPendingClient) GetPendingIntents(context.Context) ([]monolith.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollCalls++
	if s.pollCalls > 1 {
		return nil, nil
	}
	return append([]monolith.Intent(nil), s.intents...), nil
}

func (s *stubPendingClient) ClaimIntent(_ context.Context, _ string) (monolith.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls++
	return monolith.ClaimResult{ExpiresAt: time.Now().Add(time.Minute)}, nil
}

func (s *stubPendingClient) PostResult(context.Context, string, monolith.IntentResult) error {
	return nil
}

func (s *stubPendingClient) PostMessage(context.Context, string, monolith.OutboundFrame) error {
	return nil
}

func (s *stubPendingClient) GetMessages(context.Context, string, uint64) ([]monolith.InboundMessage, error) {
	return nil, nil
}

func (s *stubPendingClient) getClaimCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.claimCalls
}

func TestSchedulerDispatchesOnlyAvailableSlots(t *testing.T) {
	client := &stubPendingClient{
		intents: []monolith.Intent{
			{IntentID: "1", SessionID: "s1", Type: "DKG"},
			{IntentID: "2", SessionID: "s2", Type: "DKG"},
		},
	}

	s := NewScheduler(
		client,
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			MinInterval:   time.Millisecond,
			MaxInterval:   5 * time.Millisecond,
			BackoffFactor: 2,
		},
		slog.Default(),
		1,
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		if client.getClaimCalls() >= 1 {
			break
		}
		time.Sleep(time.Millisecond)
	}

	if client.getClaimCalls() < 1 {
		t.Fatalf("claim calls = %d, want >= 1", client.getClaimCalls())
	}

	if client.getClaimCalls() > 1 {
		t.Fatalf("claim calls = %d, want <= 1", client.getClaimCalls())
	}
}
