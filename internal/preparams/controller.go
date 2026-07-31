package preparams

import (
	"context"
	"errors"
	"sync"
	"time"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	admissionObservationInterval = 100 * time.Millisecond
	generationIdlePollInterval   = 10 * time.Millisecond
)

var (
	ErrInvalidHandle    = errors.New("invalid co-signer preparams handle")
	ErrForeignHandle    = errors.New("preparams handle belongs to another controller")
	ErrJobAlreadyActive = errors.New("preparams-controlled dkg job is already active")
)

// Handle deliberately exposes no pre-parameter material or party binding.
type Handle interface {
	Discard() error
}

type coreService interface {
	AcquireDKGPreParams(context.Context) (coretss.DKGPreParamsHandle, error)
	RunDKGSessionWithPreParams(context.Context, coretss.DKGSessionRequest, coretss.DKGPreParamsHandle) (coretss.DKGOutput, error)
	PausePreParamsRefill()
	ResumePreParamsRefill()
	Snapshot() coretss.Snapshot
}

type Controller struct {
	service coreService

	mu        sync.Mutex
	jobActive bool
	ready     bool
	wakeups   chan struct{}
}

type serviceHandle struct {
	owner *Controller
	core  coretss.DKGPreParamsHandle
}

func NewController(service coreService) (*Controller, error) {
	if service == nil {
		return nil, errors.New("preparams controller requires one core service")
	}
	return &Controller{
		service: service,
		wakeups: make(chan struct{}, 1),
	}, nil
}

// Run observes the core snapshot and emits a bounded scheduler hint whenever
// admission changes from unavailable to available.
func (c *Controller) Run(ctx context.Context) {
	if c == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.observe()
	ticker := time.NewTicker(admissionObservationInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.observe()
		}
	}
}

func (c *Controller) Wakeups() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.wakeups
}

// AdmissionHint is observational only. Acquisitions after a claim remain
// authoritative and may still fail.
func (c *Controller) AdmissionHint() bool {
	if c == nil {
		return false
	}
	return c.observe()
}

func (c *Controller) observe() bool {
	snapshot := c.service.Snapshot()
	ready := snapshot.PreParamsPoolSize >= 2 && snapshot.PreParamsGenerationInFlight == 0

	c.mu.Lock()
	if c.jobActive {
		ready = false
	}
	becameReady := ready && !c.ready
	c.ready = ready
	c.mu.Unlock()

	if becameReady {
		select {
		case c.wakeups <- struct{}{}:
		default:
		}
	}
	return ready
}

// BeginJob pauses new asynchronous generation before either handle is acquired.
// The returned idempotent function resumes asynchronous refill after all local
// runtimes and their persistence callbacks have returned.
func (c *Controller) BeginJob(ctx context.Context) (func(), error) {
	if c == nil {
		return nil, errors.New("preparams controller is nil")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	c.mu.Lock()
	if c.jobActive {
		c.mu.Unlock()
		return nil, ErrJobAlreadyActive
	}
	c.jobActive = true
	c.ready = false
	c.mu.Unlock()

	c.service.PausePreParamsRefill()
	if err := c.waitForGenerationIdle(ctx); err != nil {
		c.service.ResumePreParamsRefill()
		c.mu.Lock()
		c.jobActive = false
		c.mu.Unlock()
		c.observe()
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			c.service.ResumePreParamsRefill()
			c.mu.Lock()
			c.jobActive = false
			c.mu.Unlock()
			c.observe()
		})
	}, nil
}

func (c *Controller) waitForGenerationIdle(ctx context.Context) error {
	if c.service.Snapshot().PreParamsGenerationInFlight == 0 {
		return nil
	}
	ticker := time.NewTicker(generationIdlePollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if c.service.Snapshot().PreParamsGenerationInFlight == 0 {
				return nil
			}
		}
	}
}

func (c *Controller) AcquireDKGPreParams(ctx context.Context) (Handle, error) {
	if c == nil {
		return nil, ErrInvalidHandle
	}
	handle, err := c.service.AcquireDKGPreParams(ctx)
	if err != nil {
		return nil, err
	}
	if handle == nil {
		return nil, ErrInvalidHandle
	}
	return &serviceHandle{owner: c, core: handle}, nil
}

func (c *Controller) RunDKGSessionWithPreParams(
	ctx context.Context,
	request coretss.DKGSessionRequest,
	handle Handle,
) (coretss.DKGOutput, error) {
	owned, ok := handle.(*serviceHandle)
	if !ok || owned == nil || owned.core == nil {
		return coretss.DKGOutput{}, ErrInvalidHandle
	}
	if owned.owner != c {
		return coretss.DKGOutput{}, ErrForeignHandle
	}
	return c.service.RunDKGSessionWithPreParams(ctx, request, owned.core)
}

func (h *serviceHandle) Discard() error {
	if h == nil || h.core == nil {
		return ErrInvalidHandle
	}
	return h.core.Discard()
}

var _ Handle = (*serviceHandle)(nil)
