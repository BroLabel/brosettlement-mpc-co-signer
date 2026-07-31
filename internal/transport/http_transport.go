package transport

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

var (
	ErrTransportClosed   = errors.New("transport closed")
	ErrInvalidFrameRoute = errors.New("http transport accepts only platform-bound frames")
)

const platformPartyID = "mpc-signer"

type FrameContext struct {
	SessionID string
	Stage     string
	Protocol  string
}

type messageClient interface {
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]monolith.InboundMessage, error)
}

type HTTPTransport struct {
	client       messageClient
	frameCtx     FrameContext
	pollInterval time.Duration
	inbound      chan protocol.Frame
	startOnce    sync.Once
	closeOnce    sync.Once
	done         chan struct{}
	log          *slog.Logger
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
		go t.poll(ctx)
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
		MessageID:             frame.MessageID,
		ProtocolSeq:           frame.Seq,
		Round:                 logicalRound(frame),
		FromPartyID:           frame.FromParty,
		Broadcast:             frame.IsBroadcast(),
		Payload:               frame.Payload,
		DerivationContextHash: frame.DerivationContextHash,
	}
	if !frame.IsBroadcast() {
		outbound.ToPartyID = frame.ToParty
	}

	return t.client.PostMessage(ctx, t.frameCtx.SessionID, outbound)
}

func (t *HTTPTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
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
	t.closeOnce.Do(func() {
		close(t.done)
	})
}

func (t *HTTPTransport) poll(ctx context.Context) {
	afterSeq := uint64(0)
	sessionID := t.frameCtx.SessionID

	for {
		if t.isDone(ctx) {
			return
		}

		msgs, err := t.client.GetMessages(ctx, sessionID, afterSeq)
		if err != nil {
			if ctx.Err() != nil || t.isDone(ctx) {
				return
			}
			t.log.Warn("http transport poll failed", "err", err)
			if !t.sleep(ctx, t.pollInterval) {
				return
			}
			continue
		}

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
