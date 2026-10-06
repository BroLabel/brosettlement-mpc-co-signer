package transport

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

func (t *HTTPTransport) poll(ctx context.Context) {
	afterSeq := uint64(0)
	sessionID := t.frameCtx.SessionID
	baseline := t.frameCtx.Session
	kind := strings.ToUpper(t.frameCtx.Stage)

	for {
		if t.isDone(ctx) {
			return
		}

		result, err := t.client.GetMessages(ctx, sessionID, afterSeq)
		if t.isDone(ctx) {
			return
		}
		if err != nil {
			if errors.Is(err, monolith.ErrInvalidLifecycle) {
				t.stop(err)
				return
			}
			t.log.Warn("http transport poll failed", "err", err)
			if !t.sleep(ctx, t.pollInterval) {
				return
			}
			continue
		}

		if err := validatePolledSession(kind, sessionID, baseline, result.Session, time.Now()); err != nil {
			t.stop(err)
			return
		}
		baseline = result.Session

		var delivered bool
		afterSeq, delivered = t.deliverMessages(ctx, result.Messages, afterSeq)
		if !delivered {
			return
		}
		if len(result.Messages) == 0 && !t.sleep(ctx, t.pollInterval) {
			return
		}
	}
}

func validatePolledSession(kind, sessionID string, previous, current monolith.SessionLifecycle, now time.Time) error {
	if err := current.Validate(kind, sessionID, previous.Deadline); err != nil {
		return err
	}
	if kind != "SIGN" {
		return nil
	}
	if current.Status != "RUNNING" ||
		!current.Deadline.After(now) ||
		current.ExecutionExpiresAt == nil ||
		!current.ExecutionExpiresAt.After(now) {
		return monolith.ErrInvalidLifecycle
	}
	if previous.StartedAt != nil && (previous.ExecutionExpiresAt == nil ||
		!current.StartedAt.Equal(*previous.StartedAt) ||
		!current.ExecutionExpiresAt.Equal(*previous.ExecutionExpiresAt)) {
		return monolith.ErrInvalidLifecycle
	}
	return nil
}

func (t *HTTPTransport) deliverMessages(ctx context.Context, messages []monolith.InboundMessage, afterSeq uint64) (uint64, bool) {
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].DeliverySeq < messages[j].DeliverySeq
	})
	for _, msg := range messages {
		t.logInboundFrame(msg)
		select {
		case t.inbound <- t.toFrame(msg):
			afterSeq = max(afterSeq, msg.DeliverySeq)
		case <-t.done:
			return afterSeq, false
		case <-ctx.Done():
			return afterSeq, false
		}
	}
	return afterSeq, true
}

func (t *HTTPTransport) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return !t.isDone(ctx)
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return true
	case <-t.done:
		return false
	case <-ctx.Done():
		return false
	}
}
