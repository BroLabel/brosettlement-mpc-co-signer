package transport_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

// fakeStream implements transport.GRPCStream for tests.
type fakeStream struct {
	recv   chan protocol.Frame
	sent   []protocol.Frame
	sentMu sync.Mutex
	ctx    context.Context
}

func newFakeStream(ctx context.Context) *fakeStream {
	return &fakeStream{recv: make(chan protocol.Frame, 16), ctx: ctx}
}

func (f *fakeStream) Send(frame protocol.Frame) error {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeStream) sentCount() int {
	f.sentMu.Lock()
	defer f.sentMu.Unlock()
	return len(f.sent)
}

func (f *fakeStream) Recv() (protocol.Frame, error) {
	select {
	case fr, ok := <-f.recv:
		if !ok {
			return protocol.Frame{}, errors.New("stream closed")
		}
		return fr, nil
	case <-f.ctx.Done():
		return protocol.Frame{}, f.ctx.Err()
	}
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestSendFrameAndRecvFrame(t *testing.T) {
	ctx := context.Background()
	tr := transport.NewStreamTransport(8)
	stream := newFakeStream(ctx)
	stream.recv <- protocol.Frame{SessionID: "s1", FromParty: "p2"}

	if err := tr.Attach(stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// RecvFrame should read the frame pushed into the stream
	got, err := tr.RecvFrame(ctx)
	if err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}
	if got.SessionID != "s1" {
		t.Errorf("got SessionID=%q, want s1", got.SessionID)
	}

	// SendFrame should write to the stream
	if err := tr.SendFrame(ctx, protocol.Frame{SessionID: "s1", FromParty: "p1"}); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}
	// Give writer goroutine time to flush
	time.Sleep(10 * time.Millisecond)
	if stream.sentCount() != 1 {
		t.Fatalf("expected 1 sent frame, got %d", stream.sentCount())
	}
}

func TestDoubleAttachReturnsError(t *testing.T) {
	ctx := context.Background()
	tr := transport.NewStreamTransport(8)
	s1 := newFakeStream(ctx)
	s2 := newFakeStream(ctx)

	if err := tr.Attach(s1); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if err := tr.Attach(s2); err == nil {
		t.Fatal("expected error on second Attach")
	}
}

func TestRecvFrameContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := transport.NewStreamTransport(8)
	stream := newFakeStream(ctx)

	if err := tr.Attach(stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	cancel()
	_, err := tr.RecvFrame(ctx)
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
}

func TestRecvFrameBeforeAttach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	tr := transport.NewStreamTransport(8)

	_, err := tr.RecvFrame(ctx)
	if err == nil {
		t.Fatal("expected timeout error when no stream attached")
	}
}
