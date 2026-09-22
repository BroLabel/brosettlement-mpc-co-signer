package transport_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

type stubClient struct {
	mu           sync.Mutex
	inbound      []monolith.InboundMessage
	lastAfterSeq uint64
	lastOutbound monolith.OutboundFrame
	postCalls    int
	getCalls     int
}

func (s *stubClient) PostMessage(_ context.Context, _ string, frame monolith.OutboundFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastOutbound = frame
	s.postCalls++
	return nil
}

func (s *stubClient) GetMessages(_ context.Context, _ string, afterSeq uint64) ([]monolith.InboundMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.lastAfterSeq = afterSeq
	s.getCalls++
	if len(s.inbound) == 0 {
		return nil, nil
	}

	msgs := s.inbound
	s.inbound = nil
	return msgs, nil
}

func TestRecvFramePollsAndPreservesProtocolFields(t *testing.T) {
	client := &stubClient{
		inbound: []monolith.InboundMessage{
			{
				DeliverySeq: 11,
				ProtocolSeq: 7,
				MessageID:   "msg-1",
				Round:       2,
				FromPartyID: "mpc-signer",
				ToPartyID:   "co-signer-primary",
				Payload:     []byte("frame"),
			},
		},
	}

	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)
	tr.Start(context.Background())
	t.Cleanup(tr.Close)

	frame, err := tr.RecvFrame(context.Background())
	if err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}
	if frame.SessionID != "session-1" {
		t.Fatalf("SessionID = %q, want %q", frame.SessionID, "session-1")
	}
	if frame.Stage != "dkg" {
		t.Fatalf("Stage = %q, want %q", frame.Stage, "dkg")
	}
	if frame.Seq != 7 {
		t.Fatalf("Seq = %d, want %d", frame.Seq, 7)
	}
	if frame.FromParty != "mpc-signer" {
		t.Fatalf("FromParty = %q, want %q", frame.FromParty, "mpc-signer")
	}
}

func TestSendFrameMapsOutboundPayload(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{
			IntentID:  "intent-123",
			OrgID:     "org-123",
			SessionID: "123e4567-e89b-42d3-a456-426614174123",
			Stage:     "dkg",
			Protocol:  "ECDSA",
		},
		time.Millisecond,
		slog.Default(),
	)

	err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg_0123456789abcdef",
		Seq:       9,
		Round:     2,
		FromParty: "co-signer-primary",
		ToParty:   "mpc-signer",
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	if client.lastOutbound.MessageID != "msg_0123456789abcdef" {
		t.Fatalf("MessageID = %q, want %q", client.lastOutbound.MessageID, "msg_0123456789abcdef")
	}
	if client.lastOutbound.ProtocolSeq != 9 {
		t.Fatalf("ProtocolSeq = %d, want %d", client.lastOutbound.ProtocolSeq, 9)
	}
	if client.lastOutbound.FromPartyID != "co-signer-primary" ||
		client.lastOutbound.ToPartyID != "mpc-signer" {
		t.Fatalf("party route = %q -> %q, want co-signer-primary -> mpc-signer", client.lastOutbound.FromPartyID, client.lastOutbound.ToPartyID)
	}
	if client.lastOutbound.IntentID != "intent-123" || client.lastOutbound.OrgID != "org-123" ||
		client.lastOutbound.SessionID != "123e4567-e89b-42d3-a456-426614174123" ||
		client.lastOutbound.AuthenticatedPartyID != "co-signer-primary" {
		t.Fatalf("outbound frame lost immutable or authenticated context: %+v", client.lastOutbound)
	}
}

func TestSendFramePreservesPartyBoundEnvelopeForPlatformRouting(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)

	err := tr.SendFrame(context.Background(), protocol.Frame{
		SessionID:   "session-1",
		Stage:       "dkg",
		Protocol:    "ECDSA",
		MessageID:   "msg-1",
		Seq:         9,
		RoundHint:   2,
		FromParty:   "co-signer-primary",
		ToParty:     "mpc-signer",
		MessageType: "KGRound2Message1",
		PayloadHash: "payload-hash",
		Payload:     []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame() error = %v", err)
	}

	got := client.lastOutbound
	if got.FromPartyID != "co-signer-primary" ||
		got.ToPartyID != "mpc-signer" ||
		got.Round != 2 {
		t.Fatalf("outbound frame lost party-bound envelope: %+v", got)
	}
}

func TestSendFrameAuthenticatesEachLocalPartyAsItself(t *testing.T) {
	for _, partyID := range []string{"co-signer-primary", "co-signer-recovery"} {
		t.Run(partyID, func(t *testing.T) {
			client := &stubClient{}
			tr := transport.NewHTTPTransport(
				client,
				transport.FrameContext{IntentID: "intent-123", OrgID: "org-123", SessionID: "123e4567-e89b-42d3-a456-426614174123", Stage: "dkg", Protocol: "ECDSA"},
				time.Millisecond,
				slog.Default(),
			)
			if err := tr.SendFrame(context.Background(), protocol.Frame{
				MessageID: "msg_0123456789abcdef",
				Seq:       1,
				Round:     1,
				FromParty: partyID,
				ToParty:   "mpc-signer",
				Payload:   []byte{0},
			}); err != nil {
				t.Fatalf("SendFrame() error = %v", err)
			}
			if client.lastOutbound.AuthenticatedPartyID != partyID || client.lastOutbound.FromPartyID != partyID {
				t.Fatalf("party authentication mismatch: %+v", client.lastOutbound)
			}
		})
	}
}

func TestSendFrameRejectsNonPlatformUnicast(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)

	err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg-local",
		Seq:       1,
		Round:     1,
		FromParty: "co-signer-primary",
		ToParty:   "co-signer-recovery",
		Payload:   []byte("abc"),
	})
	if !errors.Is(err, transport.ErrInvalidFrameRoute) {
		t.Fatalf("SendFrame() error = %v, want ErrInvalidFrameRoute", err)
	}
	if client.postCalls != 0 {
		t.Fatalf("backend PostMessage() calls = %d, want 0", client.postCalls)
	}
}

func TestPollDoesNotBlockWithoutImmediateRecvFrame(t *testing.T) {
	client := &stubClient{
		inbound: []monolith.InboundMessage{
			{
				DeliverySeq: 1,
				ProtocolSeq: 1,
				MessageID:   "msg-1",
				Round:       1,
				FromPartyID: "co-signer",
				ToPartyID:   "mpc-signer",
				Payload:     []byte("frame"),
			},
		},
	}

	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)
	tr.Start(context.Background())
	t.Cleanup(tr.Close)

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		client.mu.Lock()
		getCalls := client.getCalls
		client.mu.Unlock()
		if getCalls >= 2 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	client.mu.Lock()
	getCalls := client.getCalls
	client.mu.Unlock()
	t.Fatalf("GetMessages calls = %d, want at least 2 without calling RecvFrame", getCalls)
}

func TestTransportSuppressesFrameDiagnosticsAtInfoLevel(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))

	client := &stubClient{
		inbound: []monolith.InboundMessage{
			{
				DeliverySeq: 11,
				ProtocolSeq: 7,
				MessageID:   "msg-in-1",
				Round:       2,
				FromPartyID: "mpc-signer",
				ToPartyID:   "co-signer",
				Payload:     []byte("frame"),
			},
		},
	}

	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		logger,
	)
	tr.Start(context.Background())
	t.Cleanup(tr.Close)

	if _, err := tr.RecvFrame(context.Background()); err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}

	if err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg-out-1",
		Seq:       9,
		Round:     3,
		FromParty: "co-signer-primary",
		ToParty:   "mpc-signer",
		Payload:   []byte("abc"),
	}); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	got := logs.String()
	if strings.Contains(got, "http transport received inbound frame") {
		t.Fatalf("logs = %q, want inbound diagnostic suppressed at info level", got)
	}
	if strings.Contains(got, "delivery_seq=11") || strings.Contains(got, "protocol_seq=7") {
		t.Fatalf("logs = %q, want inbound seq diagnostics suppressed at info level", got)
	}
	if strings.Contains(got, "http transport sending outbound frame") {
		t.Fatalf("logs = %q, want outbound diagnostic suppressed at info level", got)
	}
	if strings.Contains(got, "message_id=msg-out-1") || strings.Contains(got, "protocol_seq=9") {
		t.Fatalf("logs = %q, want outbound frame identifiers suppressed at info level", got)
	}
}

func TestRecvFrameRestoresBroadcastFlagFromPollingAPI(t *testing.T) {
	client := &stubClient{
		inbound: []monolith.InboundMessage{
			{
				DeliverySeq: 11,
				ProtocolSeq: 7,
				MessageID:   "msg-1",
				Round:       1,
				FromPartyID: "mpc-signer",
				ToPartyID:   "",
				Broadcast:   true,
				Payload:     []byte("frame"),
			},
		},
	}

	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)
	tr.Start(context.Background())
	t.Cleanup(tr.Close)

	frame, err := tr.RecvFrame(context.Background())
	if err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}
	if !frame.Broadcast {
		t.Fatalf("Broadcast = %v, want true", frame.Broadcast)
	}
	if !frame.IsBroadcast() {
		t.Fatal("expected inbound frame to be treated as broadcast")
	}
}

func TestRecvFramePreservesPartyBoundEnvelopeFromPlatform(t *testing.T) {
	client := &stubClient{
		inbound: []monolith.InboundMessage{
			{
				DeliverySeq: 11,
				ProtocolSeq: 7,
				MessageID:   "msg-1",
				Round:       3,
				FromPartyID: "mpc-signer",
				ToPartyID:   "co-signer-recovery",
				Payload:     []byte("frame"),
			},
		},
	}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)
	tr.Start(context.Background())
	t.Cleanup(tr.Close)

	got, err := tr.RecvFrame(context.Background())
	if err != nil {
		t.Fatalf("RecvFrame() error = %v", err)
	}
	if got.Round != 3 ||
		got.RoundHint != 3 ||
		got.Stage != "dkg" ||
		got.Protocol != "ECDSA" ||
		got.FromParty != "mpc-signer" ||
		got.ToParty != "co-signer-recovery" {
		t.Fatalf("inbound frame lost party-bound envelope: %+v", got)
	}
}

func TestSendFrameMapsBroadcastOutboundPayload(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)

	err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg-broadcast-1",
		Seq:       1,
		Round:     1,
		FromParty: "co-signer-primary",
		Broadcast: true,
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	if !client.lastOutbound.Broadcast {
		t.Fatalf("Broadcast = %v, want true", client.lastOutbound.Broadcast)
	}
	if client.lastOutbound.ToPartyID != "broadcast" {
		t.Fatalf("ToPartyID = %q, want explicit broadcast recipient marker", client.lastOutbound.ToPartyID)
	}
}
