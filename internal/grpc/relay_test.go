package grpc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type fakeRelayStream struct {
	ctx  context.Context
	recv chan *mpcv1.Frame
	sent chan *mpcv1.Frame
}

func newFakeRelayStream(ctx context.Context) *fakeRelayStream {
	return &fakeRelayStream{
		ctx:  ctx,
		recv: make(chan *mpcv1.Frame, 16),
		sent: make(chan *mpcv1.Frame, 16),
	}
}

func (f *fakeRelayStream) Recv() (*mpcv1.Frame, error) {
	select {
	case frame, ok := <-f.recv:
		if !ok {
			return nil, io.EOF
		}
		return frame, nil
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	}
}

func (f *fakeRelayStream) Send(frame *mpcv1.Frame) error {
	select {
	case f.sent <- frame:
		return nil
	case <-f.ctx.Done():
		return f.ctx.Err()
	}
}

func (f *fakeRelayStream) SetHeader(metadata.MD) error  { return nil }
func (f *fakeRelayStream) SendHeader(metadata.MD) error { return nil }
func (f *fakeRelayStream) SetTrailer(metadata.MD)       {}
func (f *fakeRelayStream) Context() context.Context     { return f.ctx }
func (f *fakeRelayStream) SendMsg(any) error            { return nil }
func (f *fakeRelayStream) RecvMsg(any) error            { return errors.New("not implemented") }

func TestRelayConnect_UnknownSession_ReturnsNotFound(t *testing.T) {
	store := session.NewStore()
	srv := NewRelayServer(store)
	stream := newFakeRelayStream(context.Background())
	stream.recv <- &mpcv1.Frame{SessionId: "missing-session", OrgId: "org-1"}

	err := srv.Connect(stream)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("got code=%v, want NotFound (err=%v)", status.Code(err), err)
	}
}

func TestRelayConnect_ForwardsFrames(t *testing.T) {
	store := session.NewStore()
	tr := transport.NewStreamTransport(8)
	store.Create("session-1", "org-1", time.Now().Add(time.Minute), tr)
	srv := NewRelayServer(store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stream := newFakeRelayStream(ctx)

	first := &mpcv1.Frame{
		SessionId:     "session-1",
		OrgId:         "org-1",
		MessageId:     "msg-1",
		Seq:           1,
		FromParty:     "party-a",
		ToParty:       "party-b",
		Payload:       []byte("first"),
		CorrelationId: "corr-1",
		SentAt:        timestamppb.New(time.Unix(1712345678, 0).UTC()),
		Stage:         "sign",
		RoundHint:     2,
		Broadcast:     false,
		Protocol:      "cmp",
		MessageType:   "commitment",
		PayloadHash:   "hash-1",
	}
	second := &mpcv1.Frame{
		SessionId: "session-1",
		OrgId:     "org-1",
		MessageId: "msg-2",
		Seq:       2,
		FromParty: "party-a",
		ToParty:   "party-b",
		Payload:   []byte("second"),
	}
	stream.recv <- first
	stream.recv <- second

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Connect(stream)
	}()

	recvCtx, recvCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer recvCancel()

	gotFirst, err := tr.RecvFrame(recvCtx)
	if err != nil {
		t.Fatalf("first RecvFrame: %v", err)
	}
	if gotFirst.MessageID != "msg-1" || string(gotFirst.Payload) != "first" {
		t.Fatalf("first inbound frame mismatch: %#v", gotFirst)
	}

	gotSecond, err := tr.RecvFrame(recvCtx)
	if err != nil {
		t.Fatalf("second RecvFrame: %v", err)
	}
	if gotSecond.MessageID != "msg-2" || string(gotSecond.Payload) != "second" {
		t.Fatalf("second inbound frame mismatch: %#v", gotSecond)
	}

	outbound := protocol.Frame{
		SessionID:     "session-1",
		OrgID:         "org-1",
		MessageID:     "msg-out",
		Seq:           10,
		Round:         3,
		FromParty:     "party-b",
		ToParty:       "party-a",
		Payload:       []byte("outbound"),
		CorrelationID: "corr-out",
		SentAt:        time.Unix(1712345700, 0).UTC(),
		Stage:         "sign",
		RoundHint:     4,
		Broadcast:     true,
		Protocol:      "cmp",
		MessageType:   "response",
		PayloadHash:   "hash-out",
	}

	if err := tr.SendFrame(recvCtx, outbound); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}

	select {
	case sent := <-stream.sent:
		if sent.GetMessageId() != "msg-out" || string(sent.GetPayload()) != "outbound" {
			t.Fatalf("outbound frame mismatch: %#v", sent)
		}
		if sent.GetSentAt().AsTime() != outbound.SentAt {
			t.Fatalf("sent_at mismatch: got %v, want %v", sent.GetSentAt().AsTime(), outbound.SentAt)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting outbound frame on stream")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil && status.Code(err) != codes.Canceled {
			t.Fatalf("Connect returned unexpected error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting Connect to return")
	}
}
