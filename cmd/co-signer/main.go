package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/lifecycle"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/preparams"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/reconcile"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/worker"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	version         = "0.1.0"
	shutdownTimeout = 30 * time.Second
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	privateKey, err := decodePrivateKey(cfg.APIPrivateKey)
	if err != nil {
		log.Error("failed to decode API private key", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	readiness := health.NewReadiness()
	var resources *applicationResources
	var healthListener net.Listener
	coordinator, err := lifecycle.NewCoordinator(lifecycle.Dependencies{
		Validate: func(context.Context) error { return nil },
		AcquireLock: func() (io.Closer, error) {
			return lifecycle.AcquireLifetimeLock(cfg.LockPath)
		},
		OpenCapabilities: func(openCtx context.Context) (io.Closer, error) {
			opened, openErr := openApplicationResources(openCtx, log, cfg, privateKey, readiness)
			if openErr != nil {
				return nil, openErr
			}
			resources = opened
			return opened, nil
		},
		StartPublisher: func(publisherCtx context.Context) error {
			return resources.startupPublisher.Start(publisherCtx)
		},
		Reconcile: func(reconcileCtx context.Context) (reconcile.Result, error) {
			return resources.reconciler.Reconcile(reconcileCtx)
		},
		Handoff: func(handoffCtx context.Context, job terminal.Job, done func(terminal.PublishResult)) error {
			return resources.startupPublisher.Handoff(handoffCtx, job, done)
		},
		SetDKGAdmissionOpen: func(open bool) {
			if resources != nil {
				resources.scheduler.SetDKGAdmissionOpen(open)
			}
		},
		ProvisioningReady: func() bool {
			return resources.provisioningReady()
		},
		SigningReady: func() bool { return resources != nil && resources.signingReady() },
		StartScheduler: func(schedulerCtx context.Context) {
			resources.startScheduler(schedulerCtx)
		},
		WakeScheduler: func() {
			resources.scheduler.Wake()
		},
		StartIntake: func(context.Context) error {
			listener, listenErr := net.Listen("tcp", resources.healthServer.Addr)
			if listenErr != nil {
				return listenErr
			}
			healthListener = listener
			go serveHealthListener(log, resources.healthServer, listener)
			return nil
		},
		StopIntake: func(stopCtx context.Context) error {
			if healthListener == nil {
				return nil
			}
			return resources.healthServer.Shutdown(stopCtx)
		},
		Drain: func(drainCtx context.Context) error {
			if resources == nil {
				return nil
			}
			return drainWorkers(drainCtx, resources.scheduler.Semaphore())
		},
		WaitPublisher: func() {
			if resources != nil {
				resources.startupPublisher.Wait()
			}
		},
		Readiness: readiness,
	})
	if err != nil {
		log.Error("failed to construct lifecycle coordinator", "err", err)
		os.Exit(1)
	}
	if err := coordinator.Start(ctx); err != nil {
		log.Error("co-signer lifecycle startup failed", "err", err)
		os.Exit(1)
	}

	<-ctx.Done()
	log.Info("shutdown started")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := coordinator.Shutdown(shutdownCtx); err != nil {
		log.Warn("lifecycle shutdown interrupted", "err", err)
	}

	log.Info("shutdown complete")
}

type applicationResources struct {
	tssService        *coretss.Service
	scheduler         *worker.Scheduler
	startupPublisher  *terminal.SingleSlot
	reconciler        *reconcile.Reconciler
	healthServer      *http.Server
	provisioningReady func() bool
	signingReady      func() bool
	background        sync.WaitGroup
}

func (r *applicationResources) Close() error {
	if r == nil {
		return nil
	}
	r.background.Wait()
	if r.tssService == nil {
		return nil
	}
	return r.tssService.StopPreParamsPool()
}

func (r *applicationResources) startBackground(run func()) {
	if r == nil || run == nil {
		return
	}
	r.background.Add(1)
	go func() {
		defer r.background.Done()
		run()
	}()
}

func (r *applicationResources) startScheduler(ctx context.Context) {
	if r == nil || r.scheduler == nil {
		return
	}
	r.startBackground(func() { r.scheduler.Run(ctx) })
}

func openApplicationResources(
	ctx context.Context,
	log *slog.Logger,
	cfg config.Config,
	privateKey ed25519.PrivateKey,
	readiness *health.Readiness,
) (_ *applicationResources, returnErr error) {
	primaryStore, err := sharestore.NewStore(cfg.PrimaryStore)
	if err != nil {
		return nil, fmt.Errorf("initialize primary artifact store: %w", err)
	}
	recoveryStore, recoveryCapabilityErr := sharestore.OpenStore(cfg.RecoveryStore)
	if recoveryStore == nil {
		err := recoveryCapabilityErr
		return nil, fmt.Errorf("initialize recovery artifact store: %w", err)
	}
	storeCapabilityErr := errors.Join(
		recoveryCapabilityErr,
		probeArtifactStores(ctx, primaryStore, recoveryStore),
	)
	primaryReader, err := sharestore.NewPrimaryReader(primaryStore)
	if err != nil {
		return nil, fmt.Errorf("initialize primary share reader: %w", err)
	}
	activePair := sharestore.NewActivePair()
	routingWriter, err := sharestore.NewRoutingWriter(activePair, primaryStore, recoveryStore)
	if err != nil {
		return nil, fmt.Errorf("initialize routing share writer: %w", err)
	}
	tssService, preParamsController, err := newDKGCoreService(
		log,
		primaryReader,
		routingWriter,
		cfg.PreParamsGenerationParallelism,
	)
	if err != nil {
		return nil, fmt.Errorf("initialize dkg preparams service: %w", err)
	}
	preParamsCapabilityErr := tssService.StartPreParamsPool(ctx)
	defer func() {
		if returnErr != nil {
			_ = tssService.StopPreParamsPool()
		}
	}()
	provisioningCapabilityErr := errors.Join(storeCapabilityErr, preParamsCapabilityErr)

	dkgCoordinator, err := worker.NewDKGCoordinator(
		preParamsController,
		activePair,
		primaryStore,
		recoveryStore,
		worker.DKGCoordinatorConfig{
			PlatformPartyID: "mpc-signer",
			PrimaryPartyID:  cfg.PrimaryStore.PartyID(),
			RecoveryPartyID: cfg.RecoveryStore.PartyID(),
		},
	)
	if err != nil {
		return nil, fmt.Errorf("initialize dual-party dkg coordinator: %w", err)
	}
	client := monolith.New(cfg.MonolithURL, cfg.APIKeyID, privateKey, cfg.HTTPTimeout)
	terminalPublisher := terminal.NewPublisher(
		client,
		terminal.DefaultRetryPolicy(),
		nil,
		terminal.ProtocolAlertFunc(func(alert terminal.ProtocolAlert) {
			log.Error("dkg terminal protocol alert", "reason", alert.Reason, "http_status", alert.HTTPStatus)
		}),
	)
	startupPublisher, err := terminal.NewSingleSlot(terminalPublisher)
	if err != nil {
		return nil, err
	}
	actionableReconciler, err := reconcile.New(
		reconcile.Config{CoSignerDeploymentID: cfg.DeploymentID, Now: time.Now},
		client,
		staticInspectionPreflight{err: provisioningCapabilityErr},
		primaryStore,
		recoveryStore,
	)
	if err != nil {
		return nil, err
	}
	provisioningReady := func() bool {
		return provisioningCapabilityErr == nil && dkgProvisioningAdmissionHint(
			preParamsController,
			[]string{cfg.PrimaryStore.Directory(), cfg.RecoveryStore.Directory()},
			cfg.FreeSpaceThresholdBytes,
			filesystemFreeBytes,
		)
	}
	signingReady := func() bool { return primaryReader.ProbeReadCapability() == nil }
	scheduler := worker.NewScheduler(
		client,
		tssService,
		dkgCoordinator,
		cfg.PrimaryStore.PartyID(),
		cfg.FramePollInterval,
		worker.SchedulerConfig{
			MinInterval:        cfg.PollMinInterval,
			MaxInterval:        cfg.PollMaxInterval,
			BackoffFactor:      cfg.PollBackoffFactor,
			ProvisioningHint:   provisioningReady,
			PreparamsHint:      preParamsController.AdmissionHint,
			ProvisioningWakeup: preParamsController.Wakeups(),
			TerminalPublisher:  terminalPublisher,
		},
		log,
		cfg.MaxConcurrent,
	)
	resources := &applicationResources{
		tssService:        tssService,
		scheduler:         scheduler,
		startupPublisher:  startupPublisher,
		reconciler:        actionableReconciler,
		provisioningReady: provisioningReady,
		signingReady:      signingReady,
	}
	resources.healthServer = newApplicationHealthServer(cfg.HTTPAddr, cfg.StateDir, readiness, resources)
	if preParamsCapabilityErr == nil {
		resources.startBackground(func() { preParamsController.Run(ctx) })
	}
	return resources, nil
}

func newApplicationHealthServer(
	addr,
	stateDir string,
	readiness *health.Readiness,
	resources *applicationResources,
) *http.Server {
	return &http.Server{
		Addr: addr,
		Handler: health.NewLifecycleHandlerWithReadinessProbes(
			version,
			stateDir,
			readiness,
			resources.signingReady,
			resources.provisioningReady,
		),
	}
}

func newDKGCoreService(
	log *slog.Logger,
	reader coretss.ShareReader,
	writer coretss.ShareWriter,
	generationParallelism int,
) (*coretss.Service, *preparams.Controller, error) {
	profile, err := preparams.ProductionProfile(generationParallelism)
	if err != nil {
		return nil, nil, err
	}
	service := coretss.NewBnbService(
		log,
		coretss.WithPreParamsConfig(profile),
		coretss.WithShareReader(reader),
		coretss.WithShareWriter(writer),
	)
	controller, err := preparams.NewController(service)
	if err != nil {
		return nil, nil, err
	}
	return service, controller, nil
}
