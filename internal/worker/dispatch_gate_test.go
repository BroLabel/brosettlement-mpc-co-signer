package worker

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

// The stop log is emitted after lifecycle terminalization, while this handler
// keeps the actual polling goroutine alive until the test releases it.
type gatedStopLog struct {
	stopped chan struct{}
	release <-chan struct{}
}

func (h *gatedStopLog) Enabled(context.Context, slog.Level) bool { return true }
func (h *gatedStopLog) WithAttrs([]slog.Attr) slog.Handler       { return h }
func (h *gatedStopLog) WithGroup(string) slog.Handler            { return h }
func (h *gatedStopLog) Handle(_ context.Context, r slog.Record) error {
	if r.Message == "http transport lifecycle stopped" {
		close(h.stopped)
		<-h.release
	}
	return nil
}

func TestTransportStopBeforeSignDispatchPreventsCoreCall(t *testing.T) {
	for _, status := range []string{"COMPLETED", "invalid", "expired"} {
		t.Run(status, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			intent := validSignIntent(t)
			claim := claimResultForIntent(intent)
			start := time.Now()
			expiry := claim.Deadline
			running := claim.Session
			running.Status = "RUNNING"
			running.StartedAt = &start
			running.ExecutionExpiresAt = &expiry
			stopped := running
			switch status {
			case "invalid":
				stopped.Deadline = stopped.Deadline.Add(time.Second)
			case "expired":
				old := start.Add(-301 * time.Second)
				expired := old.Add(300 * time.Second)
				stopped.StartedAt = &old
				stopped.ExecutionExpiresAt = &expired
			default:
				stopped.Status = status
			}
			polls := 0
			allowStop := make(chan struct{})
			client := &stubClient{claimResult: claim, poll: func(_ context.Context, _ string, seq uint64) (monolith.MessagesResult, error) {
				polls++
				if seq != 0 {
					t.Errorf("cursor advanced before dispatch: %d", seq)
				}
				if polls == 1 {
					return monolith.MessagesResult{Session: running}, nil
				}
				select {
				case <-allowStop:
				case <-ctx.Done():
					return monolith.MessagesResult{}, ctx.Err()
				}
				return monolith.MessagesResult{Session: stopped, Messages: []monolith.InboundMessage{{DeliverySeq: 9, Payload: []byte("historical")}}}, nil
			}}
			pollerRelease := make(chan struct{})
			log := &gatedStopLog{stopped: make(chan struct{}), release: pollerRelease}
			var calls atomic.Int64
			runner := signRunnerFunc(func(context.Context, coretss.SignSessionRequest) error { calls.Add(1); return nil })
			clock := realReadinessClock()
			clock.beforeDispatch = func(tr *transport.HTTPTransport) {
				close(allowStop)
				select {
				case <-tr.Done():
				case <-ctx.Done():
					t.Error("transport failed to observe stopping lifecycle")
				}
			}
			permit := make(chan struct{}, 1)
			permit <- struct{}{}
			done := make(chan struct{})
			go func() {
				defer close(done)
				runSessionWithExecutorsForTest(ctx, intent, client, runner, nil, nil, primaryPartyID, time.Millisecond, permit, nil, slog.New(log), clock)
			}()
			select {
			case <-log.stopped:
			case <-ctx.Done():
				close(pollerRelease)
				t.Fatal("post-readiness stop not observed")
			}
			// This bounded observation tests non-return; channel gates, not elapsed time,
			// establish that stop won dispatch and that the poller is still running.
			select {
			case <-done:
				t.Error("worker returned before poller joined")
			case <-time.After(time.Millisecond):
			}
			if len(permit) != 1 {
				t.Error("permit released before poller joined")
			}
			close(pollerRelease)
			select {
			case <-done:
			case <-ctx.Done():
				t.Fatal("worker failed to join poller")
			}
			if calls.Load() != 0 {
				t.Fatalf("Core called %d times after transport stop won dispatch", calls.Load())
			}
			if client.lastResult.Status != "FAILED" || len(permit) != 0 {
				t.Fatalf("result=%+v permit=%d", client.lastResult, len(permit))
			}
		})
	}
}
