package localrouter

import (
	"context"
	"errors"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const partyQueueCapacity = 256

type Config struct {
	SessionID       string
	PlatformPartyID string
	PrimaryPartyID  string
	RecoveryPartyID string
	Stage           string
	Protocol        string
}

type Router struct {
	network   coretss.Transport
	config    Config
	validator *frameValidator

	mu            sync.Mutex
	endpoints     map[string]*partyTransport
	terminalErr   error
	finishing     bool
	receiveCancel context.CancelFunc
	startOnce     sync.Once
	closeOnce     sync.Once
	finishOnce    sync.Once
	receiveDone   chan struct{}
	receiveOnce   sync.Once
	finishErr     error
	done          chan struct{}
}

type partyTransport struct {
	router  *Router
	partyID string
	inbound chan protocol.Frame
}

func New(network coretss.Transport, config Config) (*Router, error) {
	if network == nil {
		return nil, errors.New("local router network transport is required")
	}
	if config.SessionID == "" || config.Stage == "" || config.Protocol == "" ||
		config.PlatformPartyID == "" || config.PrimaryPartyID == "" || config.RecoveryPartyID == "" {
		return nil, errors.New("local router requires a complete immutable frame context")
	}
	if config.PlatformPartyID == config.PrimaryPartyID ||
		config.PlatformPartyID == config.RecoveryPartyID ||
		config.PrimaryPartyID == config.RecoveryPartyID {
		return nil, errors.New("local router parties must be distinct")
	}
	router := &Router{
		network: network,
		config:  config,
		endpoints: map[string]*partyTransport{
			config.PrimaryPartyID: {
				partyID: config.PrimaryPartyID,
				inbound: make(chan protocol.Frame, partyQueueCapacity),
			},
			config.RecoveryPartyID: {
				partyID: config.RecoveryPartyID,
				inbound: make(chan protocol.Frame, partyQueueCapacity),
			},
		},
		receiveDone: make(chan struct{}),
		done:        make(chan struct{}),
	}
	router.validator = newFrameValidator(config)
	for _, endpoint := range router.endpoints {
		endpoint.router = router
	}
	return router, nil
}

func (r *Router) Start(ctx context.Context) {
	if r == nil {
		return
	}
	r.startOnce.Do(func() {
		receiveCtx, cancel := context.WithCancel(ctx)
		r.mu.Lock()
		if r.finishing || r.terminalErr != nil {
			r.mu.Unlock()
			cancel()
			r.markReceiverDone()
			return
		}
		r.receiveCancel = cancel
		r.mu.Unlock()
		go func() {
			defer r.markReceiverDone()
			r.receiveNetwork(receiveCtx)
		}()
	})
}

func (r *Router) Transport(partyID string) (coretss.Transport, error) {
	if r == nil {
		return nil, ErrUnsupportedParty
	}
	endpoint, ok := r.endpoints[partyID]
	if !ok {
		return nil, ErrUnsupportedParty
	}
	return endpoint, nil
}

func (r *Router) Done() <-chan struct{} {
	if r == nil {
		return nil
	}
	return r.done
}

func (r *Router) Err() error {
	if r == nil {
		return ErrRouterClosed
	}
	return r.currentError()
}

func (r *Router) Close() {
	if r == nil {
		return
	}
	_ = r.Finish()
}

func (r *Router) Finish() error {
	if r == nil {
		return ErrRouterClosed
	}
	r.finishOnce.Do(func() {
		r.mu.Lock()
		r.finishing = true
		r.mu.Unlock()

		r.startOnce.Do(r.markReceiverDone)

		r.mu.Lock()
		cancel := r.receiveCancel
		r.mu.Unlock()
		if cancel != nil {
			cancel()
		}
		<-r.receiveDone

		r.mu.Lock()
		r.finishErr = r.terminalErr
		r.mu.Unlock()
		if r.finishErr == nil {
			r.closeNormally()
		}
	})
	return r.finishErr
}

func (r *Router) closeNormally() {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		if r.terminalErr == nil {
			r.terminalErr = ErrRouterClosed
		}
		if r.receiveCancel != nil {
			r.receiveCancel()
		}
		close(r.done)
		r.mu.Unlock()
	})
}

func (r *Router) routeOutbound(ctx context.Context, authenticatedSender string, frame protocol.Frame) error {
	if err := r.currentError(); err != nil {
		return err
	}
	r.mu.Lock()
	var err error
	if r.finishing {
		err = ErrRouterClosed
	} else {
		err = r.validator.validateAndRecord(frame, authenticatedSender)
	}
	r.mu.Unlock()
	if err != nil {
		return err
	}

	if frame.Broadcast {
		localParty := r.config.PrimaryPartyID
		if authenticatedSender == localParty {
			localParty = r.config.RecoveryPartyID
		}
		if err := r.deliver(ctx, localParty, frame); err != nil {
			return err
		}
		return r.network.SendFrame(ctx, cloneFrame(frame))
	}
	if frame.ToParty == r.config.PlatformPartyID {
		return r.network.SendFrame(ctx, cloneFrame(frame))
	}
	return r.deliver(ctx, frame.ToParty, frame)
}

func (r *Router) receiveNetwork(ctx context.Context) {
	for {
		frame, err := r.network.RecvFrame(ctx)
		if err != nil {
			canceled := errors.Is(err, context.Canceled)
			if canceled && r.isFinishing() {
				return
			}
			r.fail(err)
			return
		}
		r.mu.Lock()
		if r.finishing {
			r.mu.Unlock()
			return
		}
		err = r.validator.validateAndRecord(frame, r.config.PlatformPartyID)
		r.mu.Unlock()
		if err != nil {
			r.fail(err)
			return
		}
		if frame.Broadcast {
			if err := r.deliver(ctx, r.config.PrimaryPartyID, frame); err != nil {
				if errors.Is(err, context.Canceled) && r.isFinishing() {
					return
				}
				r.fail(err)
				return
			}
			if err := r.deliver(ctx, r.config.RecoveryPartyID, frame); err != nil {
				if errors.Is(err, context.Canceled) && r.isFinishing() {
					return
				}
				r.fail(err)
				return
			}
			continue
		}
		if frame.ToParty != r.config.PrimaryPartyID && frame.ToParty != r.config.RecoveryPartyID {
			r.fail(ErrInvalidFrame)
			return
		}
		if err := r.deliver(ctx, frame.ToParty, frame); err != nil {
			if errors.Is(err, context.Canceled) && r.isFinishing() {
				return
			}
			r.fail(err)
			return
		}
	}
}

func (r *Router) deliver(ctx context.Context, partyID string, frame protocol.Frame) error {
	endpoint, ok := r.endpoints[partyID]
	if !ok {
		return ErrUnsupportedParty
	}
	select {
	case endpoint.inbound <- cloneFrame(frame):
		return nil
	case <-r.done:
		return r.currentError()
	case <-ctx.Done():
		return ctx.Err()
	default:
		metrics.ObserveRelayQueueOverflow()
		return ErrQueueOverflow
	}
}

func (r *Router) fail(err error) {
	if err == nil {
		err = ErrRouterClosed
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.terminalErr = err
		if r.receiveCancel != nil {
			r.receiveCancel()
		}
		close(r.done)
		r.mu.Unlock()
	})
}

func (r *Router) isFinishing() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.finishing
}

func (r *Router) markReceiverDone() {
	r.receiveOnce.Do(func() {
		close(r.receiveDone)
	})
}

func (r *Router) currentError() error {
	select {
	case <-r.done:
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.terminalErr != nil {
			return r.terminalErr
		}
		return ErrRouterClosed
	default:
		return nil
	}
}

func (t *partyTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	return t.router.routeOutbound(ctx, t.partyID, frame)
}

func (t *partyTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	if err := t.router.currentError(); err != nil {
		return protocol.Frame{}, err
	}
	select {
	case frame := <-t.inbound:
		if err := t.router.currentError(); err != nil {
			return protocol.Frame{}, err
		}
		return frame, nil
	case <-t.router.done:
		return protocol.Frame{}, t.router.currentError()
	case <-ctx.Done():
		if err := t.router.currentError(); err != nil {
			return protocol.Frame{}, err
		}
		return protocol.Frame{}, ctx.Err()
	}
}

func cloneFrame(frame protocol.Frame) protocol.Frame {
	frame.Payload = append([]byte(nil), frame.Payload...)
	return frame
}

var _ coretss.Transport = (*partyTransport)(nil)
