package transport

import (
	"context"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

func (t *HTTPTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	select {
	case <-t.done:
		return ErrTransportClosed
	default:
	}
	outbound, err := t.toOutboundFrame(frame)
	if err != nil {
		return err
	}
	t.log.Debug("http transport sending outbound frame",
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

func (t *HTTPTransport) toOutboundFrame(frame protocol.Frame) (monolith.OutboundFrame, error) {
	broadcast := frame.IsBroadcast()
	if frame.FromParty == "" ||
		broadcast && frame.ToParty != "" ||
		!broadcast && frame.ToParty != platformPartyID {
		return monolith.OutboundFrame{}, ErrInvalidFrameRoute
	}
	toParty := frame.ToParty
	if broadcast {
		toParty = "broadcast"
	}
	return monolith.OutboundFrame{
		AuthenticatedPartyID:  frame.FromParty,
		MessageID:             frame.MessageID,
		IntentID:              t.frameCtx.IntentID,
		OrgID:                 t.frameCtx.OrgID,
		ProtocolSeq:           frame.Seq,
		Round:                 logicalRound(frame),
		FromPartyID:           frame.FromParty,
		Broadcast:             broadcast,
		SessionID:             t.frameCtx.SessionID,
		ToPartyID:             toParty,
		Payload:               frame.Payload,
		DerivationContextHash: frame.DerivationContextHash,
	}, nil
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

func (t *HTTPTransport) logInboundFrame(msg monolith.InboundMessage) {
	t.log.Debug("http transport received inbound frame",
		"message_id", msg.MessageID,
		"delivery_seq", msg.DeliverySeq,
		"protocol_seq", msg.ProtocolSeq,
		"round", msg.Round,
		"from_party", msg.FromPartyID,
		"to_party", msg.ToPartyID,
		"broadcast", msg.Broadcast,
		"payload_bytes", len(msg.Payload),
	)
}

func logicalRound(frame protocol.Frame) uint32 {
	if frame.Round != 0 {
		return frame.Round
	}
	return frame.RoundHint
}
