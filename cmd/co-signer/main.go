package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/preparams"
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

	primaryStore, err := sharestore.NewStore(cfg.PrimaryStore)
	if err != nil {
		log.Error("failed to initialize primary artifact store", "err", err)
		os.Exit(1)
	}
	recoveryStore, err := sharestore.NewStore(cfg.RecoveryStore)
	if err != nil {
		log.Error("failed to initialize recovery artifact store", "err", err)
		os.Exit(1)
	}
	if err := probeArtifactStores(context.Background(), primaryStore, recoveryStore); err != nil {
		log.Error("artifact store capability check failed", "err", err)
		os.Exit(1)
	}
	primaryReader, err := sharestore.NewPrimaryReader(primaryStore)
	if err != nil {
		log.Error("failed to initialize primary share reader", "err", err)
		os.Exit(1)
	}
	activePair := sharestore.NewActivePair()
	routingWriter, err := sharestore.NewRoutingWriter(activePair, primaryStore, recoveryStore)
	if err != nil {
		log.Error("failed to initialize routing share writer", "err", err)
		os.Exit(1)
	}

	tssSvc, preParamsController, err := newDKGCoreService(
		log,
		primaryReader,
		routingWriter,
		cfg.PreParamsGenerationParallelism,
	)
	if err != nil {
		log.Error("failed to initialize dkg preparams service", "err", err)
		os.Exit(1)
	}
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
		log.Error("failed to initialize dual-party dkg coordinator", "err", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := tssSvc.StartPreParamsPool(ctx); err != nil {
		log.Error("failed to start pre-params pool", "err", err)
		os.Exit(1)
	}
	go preParamsController.Run(ctx)
	defer func() {
		if err := tssSvc.StopPreParamsPool(); err != nil {
			log.Error("failed to stop pre-params pool", "err", err)
		}
	}()

	client := monolith.New(cfg.MonolithURL, cfg.APIKeyID, privateKey, cfg.HTTPTimeout)
	terminalPublisher := terminal.NewPublisher(
		client,
		terminal.DefaultRetryPolicy(),
		nil,
		terminal.ProtocolAlertFunc(func(alert terminal.ProtocolAlert) {
			log.Error(
				"dkg terminal protocol alert",
				"reason", alert.Reason,
				"http_status", alert.HTTPStatus,
			)
		}),
	)
	scheduler := worker.NewScheduler(
		client,
		tssSvc,
		dkgCoordinator,
		cfg.PrimaryStore.PartyID(),
		cfg.FramePollInterval,
		worker.SchedulerConfig{
			MinInterval:   cfg.PollMinInterval,
			MaxInterval:   cfg.PollMaxInterval,
			BackoffFactor: cfg.PollBackoffFactor,
			ProvisioningHint: func() bool {
				return dkgProvisioningAdmissionHint(
					preParamsController,
					[]string{cfg.PrimaryStore.Directory(), cfg.RecoveryStore.Directory()},
					cfg.FreeSpaceThresholdBytes,
					filesystemFreeBytes,
				)
			},
			ProvisioningWakeup: preParamsController.Wakeups(),
			TerminalPublisher:  terminalPublisher,
		},
		log,
		cfg.MaxConcurrent,
	)

	healthServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: health.NewHandler(version, cfg.StateDir),
	}

	go scheduler.Run(ctx)

	go serveHealth(log, healthServer)

	<-ctx.Done()
	log.Info("shutdown started")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	if err := healthServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("failed to shutdown health server", "err", err)
	}
	if err := drainWorkers(shutdownCtx, scheduler.Semaphore()); err != nil {
		log.Warn("worker drain interrupted", "err", err)
	}

	log.Info("shutdown complete")
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
