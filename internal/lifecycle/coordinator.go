package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/reconcile"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

type Dependencies struct {
	Validate            func(context.Context) error
	AcquireLock         func() (io.Closer, error)
	OpenCapabilities    func(context.Context) (io.Closer, error)
	StartPublisher      func(context.Context) error
	Reconcile           func(context.Context) (reconcile.Result, error)
	Handoff             func(context.Context, terminal.Job, func(terminal.PublishResult)) error
	SetDKGAdmissionOpen func(bool)
	// SigningReady reports systemic primary-store/shared-key availability. A
	// key-specific B failure is reported by that SIGN only and must not flip it.
	SigningReady      func() bool
	ProvisioningReady func() bool
	StartScheduler    func(context.Context)
	WakeScheduler     func()
	StartIntake       func(context.Context) error
	StopIntake        func(context.Context) error
	Drain             func(context.Context) error
	WaitPublisher     func()
	Readiness         *health.Readiness
	OnReadiness       func(bool)
	OnCancel          func()
}

type Coordinator struct {
	deps Dependencies

	mu           sync.Mutex
	lock         io.Closer
	capabilities io.Closer
	cancel       context.CancelFunc
	started      bool
	stopping     bool
	latched      bool
	confirmed    bool
	admission    atomic.Bool
}

func NewCoordinator(deps Dependencies) (*Coordinator, error) {
	switch {
	case deps.Validate == nil:
		return nil, errors.New("lifecycle config validator is required")
	case deps.AcquireLock == nil:
		return nil, errors.New("lifecycle lock acquisition is required")
	case deps.OpenCapabilities == nil:
		return nil, errors.New("lifecycle capability opener is required")
	case deps.StartPublisher == nil || deps.Handoff == nil || deps.WaitPublisher == nil:
		return nil, errors.New("lifecycle publisher controls are required")
	case deps.Reconcile == nil:
		return nil, errors.New("lifecycle reconciler is required")
	case deps.SetDKGAdmissionOpen == nil || deps.ProvisioningReady == nil ||
		deps.StartScheduler == nil || deps.WakeScheduler == nil:
		return nil, errors.New("lifecycle scheduler controls are required")
	case deps.StartIntake == nil || deps.StopIntake == nil || deps.Drain == nil:
		return nil, errors.New("lifecycle intake controls are required")
	case deps.Readiness == nil:
		return nil, errors.New("lifecycle readiness state is required")
	}
	return &Coordinator{deps: deps}, nil
}

func (c *Coordinator) Start(parent context.Context) (err error) {
	if parent == nil {
		parent = context.Background()
	}
	c.mu.Lock()
	if c.started || c.cancel != nil {
		c.mu.Unlock()
		return errors.New("lifecycle coordinator is already started")
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.mu.Unlock()

	defer func() {
		if err != nil {
			err = errors.Join(err, c.abortStartup(context.Background()))
		}
	}()
	if err = c.deps.Validate(ctx); err != nil {
		return fmt.Errorf("validate lifecycle configuration: %w", err)
	}
	c.lock, err = c.deps.AcquireLock()
	if err != nil {
		return fmt.Errorf("acquire lifetime lock: %w", err)
	}
	metrics.SetLockHeld(true)
	c.capabilities, err = c.deps.OpenCapabilities(ctx)
	if err != nil {
		return fmt.Errorf("open signing and provisioning capabilities: %w", err)
	}
	if err = c.deps.StartPublisher(ctx); err != nil {
		return fmt.Errorf("start lifecycle terminal publisher: %w", err)
	}

	var result reconcile.Result
	for {
		result, err = c.deps.Reconcile(ctx)
		if err != nil {
			return fmt.Errorf("reconcile actionable DKG: %w", err)
		}
		if result.Disposition != reconcile.DispositionFreshPollRequired {
			break
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}

	switch result.Disposition {
	case reconcile.DispositionEligible:
		metrics.SetTerminalUnconfirmed(false)
		c.setAdmission(true)
	case reconcile.DispositionCapabilityDeferred:
		metrics.SetTerminalUnconfirmed(false)
		c.mu.Lock()
		c.latched = true
		c.mu.Unlock()
		c.setAdmission(false)
	case reconcile.DispositionTerminalPublicationRequired:
		if result.Job == nil {
			return errors.New("reconciliation requires publication without an immutable job")
		}
		c.setAdmission(false)
		metrics.SetTerminalUnconfirmed(true)
		if err = c.deps.Handoff(ctx, *result.Job, c.publicationDone); err != nil {
			return fmt.Errorf("handoff startup terminal publication: %w", err)
		}
	case reconcile.DispositionProtocolIntegrity:
		if result.Cause != nil {
			return result.Cause
		}
		return errors.New("startup reconciliation protocol integrity failure")
	default:
		return fmt.Errorf("unsupported reconciliation disposition %q", result.Disposition)
	}

	c.deps.StartScheduler(ctx)
	if err = c.deps.StartIntake(ctx); err != nil {
		return fmt.Errorf("start normal intake: %w", err)
	}

	signingReady := c.deps.SigningReady == nil || c.deps.SigningReady()
	snapshot := health.Snapshot{ProcessReady: signingReady, SigningReady: signingReady}
	switch result.Disposition {
	case reconcile.DispositionEligible:
		snapshot.ProvisioningReady = signingReady && c.deps.ProvisioningReady()
		if !snapshot.ProvisioningReady {
			snapshot.ProvisioningReason = health.ReasonProvisioningUnavailable
		}
	case reconcile.DispositionCapabilityDeferred:
		snapshot.ProvisioningReason = health.ReasonCapabilityDeferred
	case reconcile.DispositionTerminalPublicationRequired:
		c.mu.Lock()
		confirmed := c.confirmed
		c.mu.Unlock()
		if confirmed {
			snapshot.ProvisioningReady = true
		} else {
			snapshot.ProvisioningReason = health.ReasonDKGTerminalUnconfirmed
		}
	}
	c.deps.Readiness.Set(snapshot)
	if c.deps.OnReadiness != nil {
		c.deps.OnReadiness(true)
	}

	c.mu.Lock()
	c.started = true
	c.mu.Unlock()
	return nil
}

func (c *Coordinator) publicationDone(result terminal.PublishResult) {
	if result.Err != nil {
		return
	}
	c.mu.Lock()
	if c.stopping || c.latched {
		c.mu.Unlock()
		return
	}
	c.confirmed = true
	c.mu.Unlock()

	c.setAdmission(true)
	metrics.SetTerminalUnconfirmed(false)
	c.mu.Lock()
	if c.stopping || c.latched {
		c.mu.Unlock()
		return
	}
	snapshot := c.deps.Readiness.Snapshot()
	snapshot.ProvisioningReady = c.deps.ProvisioningReady()
	if snapshot.ProvisioningReady {
		snapshot.ProvisioningReason = health.ReasonNone
	} else {
		snapshot.ProvisioningReason = health.ReasonProvisioningUnavailable
	}
	c.deps.Readiness.Set(snapshot)
	c.deps.WakeScheduler()
	c.mu.Unlock()
}

func (c *Coordinator) CapabilitiesRestored() {
	c.mu.Lock()
	latched := c.latched
	stopping := c.stopping
	c.mu.Unlock()
	if latched || stopping {
		return
	}
	c.deps.WakeScheduler()
}

func (c *Coordinator) DKGAdmissionOpen() bool {
	return c != nil && c.admission.Load()
}

func (c *Coordinator) setAdmission(open bool) {
	c.admission.Store(open)
	c.deps.SetDKGAdmissionOpen(open)
}

func (c *Coordinator) Shutdown(ctx context.Context) error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	if c.stopping {
		c.mu.Unlock()
		return nil
	}
	c.stopping = true
	c.setAdmission(false)
	c.mu.Unlock()
	return c.shutdown(ctx, true)
}

func (c *Coordinator) abortStartup(ctx context.Context) error {
	c.mu.Lock()
	c.stopping = true
	c.setAdmission(false)
	c.mu.Unlock()
	return c.shutdown(ctx, false)
}

func (c *Coordinator) shutdown(ctx context.Context, stopIntake bool) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c.deps.Readiness.Set(health.Snapshot{})
	metrics.SetTerminalUnconfirmed(false)
	if c.deps.OnReadiness != nil {
		c.deps.OnReadiness(false)
	}

	var errs []error
	if stopIntake {
		errs = appendError(errs, c.deps.StopIntake(ctx))
	}
	if c.cancel != nil {
		c.cancel()
		if c.deps.OnCancel != nil {
			c.deps.OnCancel()
		}
	}
	errs = appendError(errs, c.deps.Drain(ctx))
	c.deps.WaitPublisher()
	if c.capabilities != nil {
		errs = appendError(errs, c.capabilities.Close())
		c.capabilities = nil
	}
	if c.lock != nil {
		errs = appendError(errs, c.lock.Close())
		c.lock = nil
		metrics.SetLockHeld(false)
	}
	return errors.Join(errs...)
}

func appendError(errs []error, err error) []error {
	if err != nil {
		return append(errs, err)
	}
	return errs
}
