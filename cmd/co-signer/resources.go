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

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/approval"
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
	approvalJournal    *approval.Journal
	approvalDispatcher *approval.Dispatcher
	approvalReport     func(error)
	tssService         *coretss.Service
	scheduler          *worker.Scheduler
	startupPublisher   *terminal.SingleSlot
	reconciler         *reconcile.Reconciler
	healthServer       *http.Server
	provisioningReady  func() bool
	signingReady       func() bool
	background         sync.WaitGroup
}

func (r *applicationResources) Close() error {
	if r == nil {
		return nil
	}
	r.background.Wait()
	var journalErr, serviceErr error
	if r.approvalJournal != nil {
		journalErr = r.approvalJournal.Close()
	}
	if r.tssService != nil {
		serviceErr = r.tssService.StopPreParamsPool()
	}
	return errors.Join(journalErr, serviceErr)
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
	if r == nil {
		return
	}
	if r.scheduler != nil {
		r.startBackground(func() { r.scheduler.Run(ctx) })
	}
	if r.approvalDispatcher != nil {
		r.startBackground(func() { r.approvalDispatcher.Run(ctx, r.approvalReport) })
	}
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
	approvalJournal, approvalDispatcher, err := openApprovalWorker(cfg, privateKey, client)
	if err != nil {
		return nil, fmt.Errorf("initialize approval worker: %w", err)
	}
	defer func() {
		if returnErr != nil && approvalJournal != nil {
			_ = approvalJournal.Close()
		}
	}()
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
	gate := approval.SignGate{}
	if approvalJournal != nil {
		gate.Journal = approvalJournal
	}
	scheduler := worker.NewScheduler(
		client,
		tssService,
		dkgCoordinator,
		cfg.PrimaryStore.PartyID(),
		cfg.FramePollInterval,
		worker.SchedulerConfig{
			SigningDisabled:    cfg.Approval == nil,
			AuthorizeSign:      gate.Authorize,
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
		approvalJournal:    approvalJournal,
		approvalDispatcher: approvalDispatcher,
		approvalReport:     func(err error) { log.Error("approval dispatch requires retry or reconciliation", "err", err) },
		tssService:         tssService,
		scheduler:          scheduler,
		startupPublisher:   startupPublisher,
		reconciler:         actionableReconciler,
		provisioningReady:  provisioningReady,
		signingReady:       signingReady,
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

func openApprovalWorker(cfg config.Config, key ed25519.PrivateKey, backend approval.Backend) (*approval.Journal, *approval.Dispatcher, error) {
	if cfg.Approval == nil {
		return nil, nil, nil
	}
	if err := cfg.Approval.Validate(); err != nil {
		return nil, nil, err
	}
	var shares []string
	for _, path := range []string{cfg.PrimaryStore.Directory(), cfg.RecoveryStore.Directory()} {
		if path != "" {
			shares = append(shares, path)
		}
	}
	if err := approval.ValidateDirectories(cfg.Approval.JournalDir, shares...); err != nil {
		return nil, nil, err
	}
	journal, err := approval.OpenJournal(cfg.Approval.JournalDir)
	if err != nil {
		return nil, nil, err
	}
	var organization approval.Organization
	if cfg.Approval.Mode == "external" {
		organization, err = approval.NewExternal(*cfg.Approval, nil)
		if err != nil {
			journal.Close()
			return nil, nil, err
		}
	}
	dispatcher, err := approval.NewDispatcher(*cfg.Approval, journal, backend, organization, cfg.APIKeyID, key)
	if err != nil {
		journal.Close()
		return nil, nil, err
	}
	return journal, dispatcher, nil
}
