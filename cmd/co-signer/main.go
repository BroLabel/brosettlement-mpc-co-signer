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
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
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

	shareStore, err := sharestore.NewFileStore(cfg.SharesDir, shareEncryptionKey(cfg.ShareEncryptionKey))
	if err != nil {
		log.Error("failed to initialize share store", "err", err)
		os.Exit(1)
	}

	tssSvc := coretss.NewBnbService(log, coretss.WithShareStore(shareStore))

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := tssSvc.StartPreParamsPool(ctx); err != nil {
		log.Error("failed to start pre-params pool", "err", err)
		os.Exit(1)
	}
	defer func() {
		if err := tssSvc.StopPreParamsPool(); err != nil {
			log.Error("failed to stop pre-params pool", "err", err)
		}
	}()

	client := monolith.New(cfg.MonolithURL, cfg.APIKeyID, privateKey, cfg.HTTPTimeout)
	scheduler := worker.NewScheduler(
		client,
		tssSvc,
		cfg.PartyID,
		cfg.FramePollInterval,
		worker.SchedulerConfig{
			MinInterval:   cfg.PollMinInterval,
			MaxInterval:   cfg.PollMaxInterval,
			BackoffFactor: cfg.PollBackoffFactor,
		},
		log,
		cfg.MaxConcurrent,
	)

	healthServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: health.NewHandler(version, cfg.SharesDir),
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
