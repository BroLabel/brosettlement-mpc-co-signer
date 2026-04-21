package transport_test

import (
	"bytes"
	"context"
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
	if frame.FromParty != "co-signer" {
		t.Fatalf("FromParty = %q, want %q", frame.FromParty, "co-signer")
	}
}

func TestSendFrameMapsOutboundPayload(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(
		client,
		transport.FrameContext{SessionID: "session-1", Stage: "dkg", Protocol: "ECDSA"},
		time.Millisecond,
		slog.Default(),
	)

	err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg-1",
		Seq:       9,
		Round:     2,
		ToParty:   "co-signer",
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	if client.lastOutbound.MessageID != "msg-1" {
		t.Fatalf("MessageID = %q, want %q", client.lastOutbound.MessageID, "msg-1")
	}
	if client.lastOutbound.ProtocolSeq != 9 {
		t.Fatalf("ProtocolSeq = %d, want %d", client.lastOutbound.ProtocolSeq, 9)
	}
	if client.lastOutbound.ToPartyID != "mpc-signer" {
		t.Fatalf("ToPartyID = %q, want %q", client.lastOutbound.ToPartyID, "mpc-signer")
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
		Broadcast: true,
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	if !client.lastOutbound.Broadcast {
		t.Fatalf("Broadcast = %v, want true", client.lastOutbound.Broadcast)
	}
	if client.lastOutbound.ToPartyID != "" {
		t.Fatalf("ToPartyID = %q, want empty for broadcast frame", client.lastOutbound.ToPartyID)
	}
}
