package transport

import (
	"context"
	"errors"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

var ErrAlreadyAttached = errors.New("stream already attached")

// GRPCStream is the subset of a gRPC bidirectional stream needed by StreamTransport.
type GRPCStream interface {
	Send(protocol.Frame) error
	Recv() (protocol.Frame, error)
	Context() context.Context
}

// StreamTransport implements tss.Transport over a gRPC bidirectional stream.
// Call Attach once the gRPC stream is available; SendFrame/RecvFrame block until then.
type StreamTransport struct {
	inbound  chan protocol.Frame
	outbound chan protocol.Frame
	attachMu sync.Mutex
	closeMu  sync.Once
	doneMu   sync.Once
	done     chan struct{}
	attached bool
}

func NewStreamTransport(bufSize int) *StreamTransport {
	return &StreamTransport{
		inbound:  make(chan protocol.Frame, bufSize),
		outbound: make(chan protocol.Frame, bufSize),
		done:     make(chan struct{}),
	}
}

// Attach wires a live gRPC stream to the transport channels.
// Must be called exactly once; returns ErrAlreadyAttached on subsequent calls.
func (t *StreamTransport) Attach(stream GRPCStream) error {
	t.attachMu.Lock()
	defer t.attachMu.Unlock()
	if t.attached {
		return ErrAlreadyAttached
	}
	t.attached = true

	// reader: stream -> inbound
	go func() {
		defer t.closeDone()
		defer close(t.inbound)
		for {
			frame, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case t.inbound <- frame:
			case <-stream.Context().Done():
				return
			}
		}
	}()

	// writer: outbound -> stream
	go func() {
		for {
			select {
			case frame, ok := <-t.outbound:
				if !ok {
					return
				}
				_ = stream.Send(frame)
			case <-t.done:
				return
			case <-stream.Context().Done():
				t.closeDone()
				return
			}
		}
	}()

	return nil
}

// SendFrame implements tss.Transport.
func (t *StreamTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	select {
	case t.outbound <- frame:
		return nil
	case <-t.done:
		return errors.New("stream closed")
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecvFrame implements tss.Transport.
func (t *StreamTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	select {
	case frame, ok := <-t.inbound:
		if !ok {
			return protocol.Frame{}, errors.New("stream closed")
		}
		return frame, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

// PushInbound injects a frame into inbound queue (used to replay the first relay frame).
func (t *StreamTransport) PushInbound(ctx context.Context, frame protocol.Frame) error {
	select {
	case t.inbound <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (t *StreamTransport) Done() <-chan struct{} {
	return t.done
}

// Close drains and closes the outbound channel, signalling the writer goroutine to exit.
func (t *StreamTransport) Close() {
	t.closeMu.Do(func() {
		close(t.outbound)
	})
	t.closeDone()
}

func (t *StreamTransport) closeDone() {
	t.doneMu.Do(func() {
		close(t.done)
	})
}
