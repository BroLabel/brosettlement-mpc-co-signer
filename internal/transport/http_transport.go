package transport

import (
	"context"
	"errors"
	"log/slog"
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
	client       messageClient
	frameCtx     FrameContext
	pollInterval time.Duration
	inbound      chan protocol.Frame
	done         chan struct{} // Closed when stop is declared, before polling joins.
	log          *slog.Logger

	// mu serializes Start, stop, and Dispatch. Never hold it while running Core
	// or waiting for the poller.
	mu             sync.Mutex
	pollCancel     context.CancelFunc
	pollStopped    chan struct{} // Non-nil once started; closed when polling exits.
	err            error
	dispatched     bool
	dispatchCancel context.CancelFunc
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
		log: log.With(
			"session_id", frameCtx.SessionID,
			"stage", frameCtx.Stage,
			"protocol", frameCtx.Protocol,
		),
	}
}

func (t *HTTPTransport) Start(ctx context.Context) {
	t.mu.Lock()
	defer t.mu.Unlock()
	select {
	case <-t.done:
		return
	default:
	}
	if t.pollStopped != nil {
		return
	}

	pollCtx, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	t.pollCancel = cancel
	t.pollStopped = stopped
	go func() {
		defer close(stopped)
		t.poll(pollCtx)
	}()
}

// Close cancels execution and joins the poller. The caller must separately join
// Dispatch before releasing the execution's resources.
func (t *HTTPTransport) Close() {
	t.stop(nil)
	t.mu.Lock()
	stopped := t.pollStopped
	t.mu.Unlock()
	if stopped != nil {
		<-stopped
	}
}

func (t *HTTPTransport) Done() <-chan struct{} { return t.done }

func (t *HTTPTransport) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

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
	select {
	case <-t.done:
		t.mu.Unlock()
		return
	default:
	}
	t.err = err
	close(t.done)
	if t.pollCancel != nil {
		t.pollCancel()
	}
	if t.dispatchCancel != nil {
		t.dispatchCancel()
	}
	t.mu.Unlock()
	if err != nil {
		t.log.Warn("http transport lifecycle stopped", "err", err)
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
