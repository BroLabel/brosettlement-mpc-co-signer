package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

var (
	version  = "dev"
	revision = "unknown"
)

const shutdownTimeout = 30 * time.Second

func main() {
	identity := currentBuildIdentity()
	version = identity.version
	revision = identity.revision

	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	log.Info("co-signer starting", "version", version, "revision", revision)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	if err := run(ctx, log); err != nil {
		log.Error("co-signer failed", "err", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("config error: %w", err)
	}
	privateKey, err := decodePrivateKey(cfg.APIPrivateKey)
	if err != nil {
		return fmt.Errorf("failed to decode API private key: %w", err)
	}

	coordinator, err := newLifecycleCoordinator(log, cfg, privateKey)
	if err != nil {
		return fmt.Errorf("failed to construct lifecycle coordinator: %w", err)
	}
	if err := coordinator.Start(ctx); err != nil {
		return fmt.Errorf("co-signer lifecycle startup failed: %w", err)
	}

	<-ctx.Done()
	log.Info("shutdown started")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()
	if err := coordinator.Shutdown(shutdownCtx); err != nil {
		log.Warn("lifecycle shutdown completed with errors", "err", err)
	}
	log.Info("shutdown complete")
	return nil
}
