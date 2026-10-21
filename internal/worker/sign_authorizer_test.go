package worker

import (
	"context"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

// Session lifecycle tests explicitly allow SIGN so they reach the behavior under test.
// Authorization tests use the real gate or call the worker with no authorizer.
func allowSignForTest(context.Context, monolith.Intent) error { return nil }

func TestSessionWithoutSignAuthorizerNeverStartsCore(t *testing.T) {
	intent := validSignIntent(t)
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &countingSignRunner{}
	runSessionWithClock(
		context.Background(), intent, client, runner, nil, nil,
		primaryPartyID, time.Millisecond, &jobPermitLease{}, nil, nil,
		realReadinessClock(),
	)
	if runner.calls != 0 {
		t.Fatalf("Core calls = %d, want no execution without a sign authorizer", runner.calls)
	}
	if client.lastResult.Status != "FAILED" {
		t.Fatalf("terminal status = %q, want FAILED", client.lastResult.Status)
	}
}
