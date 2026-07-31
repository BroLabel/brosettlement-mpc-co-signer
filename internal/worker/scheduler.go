package worker

import (
	"context"
	"log/slog"
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
	MinInterval   time.Duration
	MaxInterval   time.Duration
	BackoffFactor float64
}

type Scheduler struct {
	client            pendingClient
	signRunner        signSessionRunner
	dkgRunner         dkgExecutor
	localPartyID      string
	framePollInterval time.Duration
	sem               chan struct{}
	repollCh          chan struct{}
	cfg               SchedulerConfig
	log               *slog.Logger
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
	return &Scheduler{
		client:            client,
		signRunner:        signRunner,
		dkgRunner:         dkgRunner,
		localPartyID:      localPartyID,
		framePollInterval: framePollInterval,
		sem:               make(chan struct{}, maxConcurrent),
		repollCh:          make(chan struct{}, 1),
		cfg:               cfg,
		log:               log,
	}
}

func (s *Scheduler) Run(ctx context.Context) {
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
	dispatch:
		for _, intent := range intents {
			select {
			case s.sem <- struct{}{}:
				intent := intent
				go RunSessionWithExecutors(
					ctx,
					intent,
					s.client,
					s.signRunner,
					s.dkgRunner,
					s.localPartyID,
					s.framePollInterval,
					s.sem,
					s.repollCh,
					s.log,
				)
			default:
				break dispatch
			}
		}
	}
}

func (s *Scheduler) Semaphore() chan struct{} {
	return s.sem
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
