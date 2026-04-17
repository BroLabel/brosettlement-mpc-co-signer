package worker

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type stubClient struct {
	claimResult monolith.ClaimResult
	claimErr    error
	lastResult  monolith.IntentResult
}

func (s *stubClient) ClaimIntent(_ context.Context, _ string) (monolith.ClaimResult, error) {
	if s.claimResult.ExpiresAt.IsZero() {
		s.claimResult.ExpiresAt = time.Now().Add(time.Minute)
	}
	return s.claimResult, s.claimErr
}

func (s *stubClient) PostResult(_ context.Context, _ string, result monolith.IntentResult) error {
	s.lastResult = result
	return nil
}

func (s *stubClient) PostMessage(context.Context, string, monolith.OutboundFrame) error {
	return nil
}

func (s *stubClient) GetMessages(context.Context, string, uint64) ([]monolith.InboundMessage, error) {
	return nil, nil
}

type stubRunner struct{}

func (s *stubRunner) RunDKGSession(context.Context, coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	return coretss.DKGOutput{}, nil
}

func (s *stubRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

func TestRunSessionRejectsInvalidIntent(t *testing.T) {
	client := &stubClient{}
	runner := &stubRunner{}
	intent := monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Parties:   []string{"party-1", "party-2"},
			Threshold: 2,
		},
	}

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	RunSession(
		context.Background(),
		intent,
		client,
		runner,
		"party-1",
		time.Millisecond,
		sem,
		make(chan struct{}, 1),
		slog.Default(),
	)

	if client.lastResult.ErrorCode != ErrorCodeInvalidIntent {
		t.Fatalf("error code = %q, want %q", client.lastResult.ErrorCode, ErrorCodeInvalidIntent)
	}
}

func TestBuildResultMapsShareNotFound(t *testing.T) {
	result := BuildResult(coretss.ErrShareNotFound, context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeShareNotFound {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeShareNotFound)
	}
}

func TestBuildResultMapsKnownProtocolErrors(t *testing.T) {
	result := BuildResult(errors.New("duplicate frame"), context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeProtocol {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeProtocol)
	}
}

func TestBuildResultUsesCanceledSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeWorkerShutdown {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeWorkerShutdown)
	}
}

func TestBuildResultUsesExpiredSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeSessionTimeout {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeSessionTimeout)
	}
}
