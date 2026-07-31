package worker

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

type pendingClient interface {
	GetPendingIntents(ctx context.Context) ([]monolith.Intent, error)
	ClaimIntent(ctx context.Context, intentID string) (monolith.ClaimResult, error)
	PostResult(ctx context.Context, intentID string, result monolith.IntentResult) error
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]monolith.InboundMessage, error)
}

type SchedulerConfig struct {
	MinInterval        time.Duration
	MaxInterval        time.Duration
	BackoffFactor      float64
	ProvisioningHint   func() bool
	ProvisioningWakeup <-chan struct{}
	TerminalPublisher  DKGTerminalPublisher
}

type sessionLauncher func(context.Context, monolith.Intent, *jobPermitLease) <-chan struct{}

type Scheduler struct {
	client            pendingClient
	signRunner        signSessionRunner
	dkgRunner         dkgExecutor
	localPartyID      string
	framePollInterval time.Duration
	permits           *schedulerPermits
	repollCh          chan struct{}
	cfg               SchedulerConfig
	log               *slog.Logger
	dkgAdmissionOpen  atomic.Bool
	launch            sessionLauncher
}

func NewScheduler(
	client pendingClient,
	signRunner signSessionRunner,
	dkgRunner dkgExecutor,
	localPartyID string,
	framePollInterval time.Duration,
	cfg SchedulerConfig,
	log *slog.Logger,
	maxConcurrent int,
) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	if maxConcurrent <= 0 {
		maxConcurrent = 1
	}
	repollCh := make(chan struct{}, 1)
	scheduler := &Scheduler{
		client:            client,
		signRunner:        signRunner,
		dkgRunner:         dkgRunner,
		localPartyID:      localPartyID,
		framePollInterval: framePollInterval,
		permits:           newSchedulerPermits(maxConcurrent, repollCh),
		repollCh:          repollCh,
		cfg:               cfg,
		log:               log,
	}
	scheduler.dkgAdmissionOpen.Store(cfg.TerminalPublisher != nil)
	scheduler.launch = scheduler.launchSession
	return scheduler
}

func (s *Scheduler) Run(ctx context.Context) {
	go s.forwardProvisioningWakeups(ctx)
	backoff := s.cfg.MinInterval

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		case <-s.repollCh:
		}

		intents, err := s.client.GetPendingIntents(ctx)
		if err != nil {
			s.log.Warn("get pending intents failed", "err", err)
			backoff = nextBackoff(backoff, s.cfg)
			continue
		}

		if len(intents) == 0 {
			backoff = nextBackoff(backoff, s.cfg)
			continue
		}

		backoff = s.cfg.MinInterval
		s.dispatchBatch(ctx, intents)
	}
}

func (s *Scheduler) Semaphore() chan struct{} {
	if s == nil || s.permits == nil || s.permits.general == nil {
		return nil
	}
	return s.permits.general.slots
}

func (s *Scheduler) SetDKGAdmissionOpen(open bool) {
	if s == nil {
		return
	}
	s.dkgAdmissionOpen.Store(open)
	if open {
		s.Wake()
	}
}

func (s *Scheduler) DKGAdmissionOpen() bool {
	return s != nil && s.dkgAdmissionOpen.Load()
}

func (s *Scheduler) Wake() {
	if s == nil {
		return
	}
	select {
	case s.repollCh <- struct{}{}:
	default:
	}
}

func (s *Scheduler) forwardProvisioningWakeups(ctx context.Context) {
	if s == nil || s.cfg.ProvisioningWakeup == nil {
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-s.cfg.ProvisioningWakeup:
			if !ok {
				return
			}
			s.Wake()
		}
	}
}

func (s *Scheduler) dispatchBatch(ctx context.Context, intents []monolith.Intent) {
	dkgConsidered := false
	for _, intent := range intents {
		kind, ok := classifyIntentKind(intent.Type)
		if !ok {
			s.log.Error("unsupported pending intent type")
			continue
		}
		switch kind {
		case intentKindDKG:
			if dkgConsidered {
				continue
			}
			dkgConsidered = true
			if !s.DKGAdmissionOpen() || !s.provisioningReady() {
				continue
			}
			permits := s.permits.tryAcquireDKG()
			if permits == nil {
				continue
			}
			if !waitForClaimDispatch(ctx, s.launch(ctx, intent, permits)) {
				return
			}
		case intentKindSIGN:
			permits := s.permits.tryAcquireSIGN()
			if permits == nil {
				continue
			}
			if !waitForClaimDispatch(ctx, s.launch(ctx, intent, permits)) {
				return
			}
		}
	}
}

func waitForClaimDispatch(ctx context.Context, dispatched <-chan struct{}) bool {
	if dispatched == nil {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-dispatched:
		return true
	}
}

func (s *Scheduler) provisioningReady() bool {
	return s.cfg.ProvisioningHint == nil || s.cfg.ProvisioningHint()
}

func (s *Scheduler) launchSession(ctx context.Context, intent monolith.Intent, permits *jobPermitLease) <-chan struct{} {
	claimDispatched := make(chan struct{})
	go runSessionWithPermits(
		ctx,
		intent,
		s.client,
		s.signRunner,
		s.dkgRunner,
		s.cfg.TerminalPublisher,
		s.localPartyID,
		s.framePollInterval,
		permits,
		s.log,
		func() { close(claimDispatched) },
	)
	return claimDispatched
}

func nextBackoff(current time.Duration, cfg SchedulerConfig) time.Duration {
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = time.Millisecond
	}
	if current <= 0 {
		current = cfg.MinInterval
	}
	if cfg.BackoffFactor <= 1 {
		if current < cfg.MinInterval {
			current = cfg.MinInterval
		}
		if cfg.MaxInterval > 0 && current > cfg.MaxInterval {
			return cfg.MaxInterval
		}
		return current
	}

	next := time.Duration(float64(current) * cfg.BackoffFactor)
	if next < cfg.MinInterval {
		next = cfg.MinInterval
	}
	if cfg.MaxInterval > 0 && next > cfg.MaxInterval {
		return cfg.MaxInterval
	}
	return next
}
