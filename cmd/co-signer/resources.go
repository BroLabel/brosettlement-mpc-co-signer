package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/preparams"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/reconcile"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/worker"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

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
	primaryStore, err := sharestore.OpenStore(cfg.PrimaryStore)
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
		reconcile.Config{Now: time.Now},
		client,
		staticInspectionPreflight{err: provisioningCapabilityErr},
		primaryStore,
		recoveryStore,
	)
	if err != nil {
		return nil, err
	}
	artifactDirectories := []string{cfg.PrimaryStore.Directory(), cfg.RecoveryStore.Directory()}
	provisioningReady := func() bool {
		return provisioningReadiness(
			artifactDirectories,
			filesystemFreeBytes,
			provisioningCapabilityErr,
			preParamsController,
			primaryStore,
			recoveryStore,
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
	resources.healthServer = newApplicationHealthServer(cfg.HTTPAddr, cfg.PrimaryStore.Directory(), readiness, resources)
	if preParamsCapabilityErr == nil {
		resources.startBackground(func() { preParamsController.Run(ctx) })
	}
	return resources, nil
}

func newApplicationHealthServer(
	addr,
	primarySharesDir string,
	readiness *health.Readiness,
	resources *applicationResources,
) *http.Server {
	return &http.Server{
		Addr: addr,
		Handler: health.NewLifecycleHandlerWithReadinessProbes(
			version,
			revision,
			primarySharesDir,
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
