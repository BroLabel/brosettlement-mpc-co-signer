package transport

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

var (
	ErrTransportClosed   = errors.New("transport closed")
	ErrInvalidFrameRoute = errors.New("http transport accepts only platform-bound frames")
	ErrAlreadyDispatched = errors.New("transport execution already dispatched")
)

const platformPartyID = "mpc-signer"

type FrameContext struct {
	Session   monolith.SessionLifecycle
	IntentID  string
	OrgID     string
	SessionID string
	Stage     string
	Protocol  string
}

type messageClient interface {
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) (monolith.MessagesResult, error)
}

type HTTPTransport struct {
	client         messageClient
	frameCtx       FrameContext
	pollInterval   time.Duration
	inbound        chan protocol.Frame
	startOnce      sync.Once
	closeOnce      sync.Once
	mu             sync.Mutex
	cancel         context.CancelFunc
	stopped        chan struct{}
	err            error
	dispatched     bool
	dispatchCancel context.CancelFunc
	done           chan struct{}
	log            *slog.Logger
}

func NewHTTPTransport(client messageClient, frameCtx FrameContext, pollInterval time.Duration, log *slog.Logger) *HTTPTransport {
	if log == nil {
		log = slog.Default()
	}

	return &HTTPTransport{
		client:       client,
		frameCtx:     frameCtx,
		pollInterval: pollInterval,
		inbound:      make(chan protocol.Frame, 256),
		done:         make(chan struct{}),
		log:          log,
	}
}

func (t *HTTPTransport) Start(ctx context.Context) {
	t.startOnce.Do(func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		select {
		case <-t.done:
			return
		default:
		}
		pollCtx, cancel := context.WithCancel(ctx)
		t.cancel = cancel
		t.stopped = make(chan struct{})
		go func() { defer close(t.stopped); t.poll(pollCtx) }()
	})
}

func (t *HTTPTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	select {
	case <-t.done:
		return ErrTransportClosed
	default:
	}
	if frame.FromParty == "" ||
		frame.IsBroadcast() && frame.ToParty != "" ||
		!frame.IsBroadcast() && frame.ToParty != platformPartyID {
		return ErrInvalidFrameRoute
	}

	t.log.Debug("http transport sending outbound frame",
		"session_id", t.frameCtx.SessionID,
		"stage", t.frameCtx.Stage,
		"protocol", t.frameCtx.Protocol,
		"message_id", frame.MessageID,
		"protocol_seq", frame.Seq,
		"round", frame.Round,
		"round_hint", frame.RoundHint,
		"message_type", frame.MessageType,
		"from_party", frame.FromParty,
		"to_party", frame.ToParty,
		"broadcast", frame.IsBroadcast(),
		"payload_bytes", len(frame.Payload),
	)

	outbound := monolith.OutboundFrame{
		AuthenticatedPartyID:  frame.FromParty,
		MessageID:             frame.MessageID,
		IntentID:              t.frameCtx.IntentID,
		OrgID:                 t.frameCtx.OrgID,
		ProtocolSeq:           frame.Seq,
		Round:                 logicalRound(frame),
		FromPartyID:           frame.FromParty,
		Broadcast:             frame.IsBroadcast(),
		SessionID:             t.frameCtx.SessionID,
		Payload:               frame.Payload,
		DerivationContextHash: frame.DerivationContextHash,
	}
	if !frame.IsBroadcast() {
		outbound.ToPartyID = frame.ToParty
	} else {
		outbound.ToPartyID = "broadcast"
	}

	return t.client.PostMessage(ctx, t.frameCtx.SessionID, outbound)
}

func (t *HTTPTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	if t.isDone(ctx) {
		if ctx.Err() != nil {
			return protocol.Frame{}, ctx.Err()
		}
		return protocol.Frame{}, ErrTransportClosed
	}
	select {
	case frame := <-t.inbound:
		return frame, nil
	case <-t.done:
		return protocol.Frame{}, ErrTransportClosed
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func (t *HTTPTransport) Close() {
	t.stop(nil)
	t.mu.Lock()
	stopped := t.stopped
	t.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
}

func (t *HTTPTransport) Done() <-chan struct{} { return t.done }
func (t *HTTPTransport) Err() error            { t.mu.Lock(); defer t.mu.Unlock(); return t.err }

// Dispatch atomically reserves execution against stop. This reservation is
// the dispatch linearization point: a prior stop prohibits run, while a later
// stop cancels the already-owned execution. No lock is held while run executes.
// The caller must join this synchronous call before releasing its resources.
func (t *HTTPTransport) Dispatch(ctx context.Context, run func(context.Context) error) error {
	t.mu.Lock()
	select {
	case <-t.done:
		err := t.err
		t.mu.Unlock()
		if err != nil {
			return err
		}
		return ErrTransportClosed
	default:
	}
	if err := ctx.Err(); err != nil {
		t.mu.Unlock()
		return err
	}
	if t.dispatched {
		t.mu.Unlock()
		return ErrAlreadyDispatched
	}
	runnerCtx, cancel := context.WithCancel(ctx)
	t.dispatched = true
	t.dispatchCancel = cancel
	t.mu.Unlock()
	defer cancel()
	return run(runnerCtx)
}

func (t *HTTPTransport) stop(err error) {
	t.mu.Lock()
	stopped := false
	t.closeOnce.Do(func() {
		stopped = true
		t.err = err
		close(t.done)
		if t.cancel != nil {
			t.cancel()
		}
		if t.dispatchCancel != nil {
			t.dispatchCancel()
		}
	})
	t.mu.Unlock()
	if stopped && err != nil {
		t.log.Warn("http transport lifecycle stopped", "err", err)
	}
}

func (t *HTTPTransport) poll(ctx context.Context) {
	afterSeq := uint64(0)
	sessionID := t.frameCtx.SessionID
	baseline := t.frameCtx.Session
	kind := strings.ToUpper(t.frameCtx.Stage)

	for {
		if t.isDone(ctx) {
			return
		}

		pollCtx, cancel := context.WithTimeout(ctx, 400*time.Millisecond)
		result, err := t.client.GetMessages(pollCtx, sessionID, afterSeq)
		cancel()
		if t.isDone(ctx) {
			return
		}
		if err != nil {
			if errors.Is(err, monolith.ErrInvalidLifecycle) {
				t.stop(err)
				return
			}
			if ctx.Err() != nil || t.isDone(ctx) {
				return
			}
			t.log.Warn("http transport poll failed", "err", err)
			if !t.sleep(ctx, t.pollInterval) {
				return
			}
			continue
		}
		if err := result.Session.Validate(kind, sessionID, baseline.Deadline); err != nil {
			t.stop(err)
			return
		}
		if kind == "SIGN" {
			if result.Session.Status != "RUNNING" || !result.Session.Deadline.After(time.Now()) || result.Session.ExecutionExpiresAt == nil || !result.Session.ExecutionExpiresAt.After(time.Now()) {
				t.stop(monolith.ErrInvalidLifecycle)
				return
			}
			if baseline.StartedAt != nil && (baseline.ExecutionExpiresAt == nil || !result.Session.StartedAt.Equal(*baseline.StartedAt) || !result.Session.ExecutionExpiresAt.Equal(*baseline.ExecutionExpiresAt)) {
				t.stop(monolith.ErrInvalidLifecycle)
				return
			}
		}
		baseline = result.Session

		msgs := result.Messages
		sort.Slice(msgs, func(i, j int) bool {
			return msgs[i].DeliverySeq < msgs[j].DeliverySeq
		})

		for _, msg := range msgs {
			frame := t.toFrame(msg)
			t.log.Debug("http transport received inbound frame",
				"session_id", t.frameCtx.SessionID,
				"stage", t.frameCtx.Stage,
				"protocol", t.frameCtx.Protocol,
				"message_id", msg.MessageID,
				"delivery_seq", msg.DeliverySeq,
				"protocol_seq", msg.ProtocolSeq,
				"round", msg.Round,
				"from_party", msg.FromPartyID,
				"to_party", msg.ToPartyID,
				"broadcast", msg.Broadcast,
				"payload_bytes", len(msg.Payload),
			)
			select {
			case t.inbound <- frame:
			case <-t.done:
				return
			case <-ctx.Done():
				return
			}
			afterSeq = maxUint64(afterSeq, msg.DeliverySeq)
		}

		if len(msgs) == 0 && !t.sleep(ctx, t.pollInterval) {
			return
		}
	}
}

func (t *HTTPTransport) toFrame(msg monolith.InboundMessage) protocol.Frame {
	return protocol.Frame{
		SessionID:             t.frameCtx.SessionID,
		MessageID:             msg.MessageID,
		Seq:                   msg.ProtocolSeq,
		Round:                 msg.Round,
		RoundHint:             msg.Round,
		Broadcast:             msg.Broadcast,
		Stage:                 t.frameCtx.Stage,
		Protocol:              t.frameCtx.Protocol,
		FromParty:             msg.FromPartyID,
		ToParty:               msg.ToPartyID,
		Payload:               msg.Payload,
		DerivationContextHash: msg.DerivationContextHash,
	}
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

func (t *HTTPTransport) isDone(ctx context.Context) bool {
	select {
	case <-t.done:
		return true
	case <-ctx.Done():
		return true
	default:
		return false
	}
}

func maxUint64(a, b uint64) uint64 {
	if b > a {
		return b
	}
	return a
}

func logicalRound(frame protocol.Frame) uint32 {
	if frame.Round != 0 {
		return frame.Round
	}
	return frame.RoundHint
}
