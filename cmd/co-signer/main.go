package main

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
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

func decodePrivateKey(raw string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("private key is empty")
	}

	bytes, err := hex.DecodeString(trimmed)
	if err != nil {
		bytes, err = base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("private key must be hex or base64: %w", err)
		}
	}

	switch len(bytes) {
	case ed25519.PrivateKeySize:
		return ed25519.PrivateKey(bytes), nil
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(bytes), nil
	default:
		return nil, fmt.Errorf("private key length must be %d or %d bytes, got %d", ed25519.PrivateKeySize, ed25519.SeedSize, len(bytes))
	}
}

func shareEncryptionKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

func serveHealth(log *slog.Logger, srv *http.Server) {
	log.Info("health server listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("health server stopped", "err", err)
	}
}

func drainWorkers(ctx context.Context, sem chan struct{}) error {
	if sem == nil {
		return nil
	}

	for i := 0; i < cap(sem); i++ {
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return nil
}
