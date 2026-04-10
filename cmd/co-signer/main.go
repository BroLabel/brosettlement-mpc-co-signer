package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	cosgrpc "github.com/BroLabel/brosettlement-mpc-co-signer/internal/grpc"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	version                = "0.1.0"
	sessionCleanupInterval = time.Minute
	sessionRetention       = 5 * time.Minute
	shutdownTimeout        = 30 * time.Second
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	shareStore, err := sharestore.NewFileStore(cfg.SharesDir, shareEncryptionKey(cfg.APIKey))
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

	store := session.NewStore()
	go runSessionCleanup(ctx, store)

	grpcServer := cosgrpc.NewServer(tssSvc, store, cfg.PartyID, cfg.APIKey, log)

	grpcListener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Error("failed to listen for gRPC", "addr", cfg.GRPCAddr, "err", err)
		os.Exit(1)
	}

	httpServer := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: health.NewHandler(version, cfg.SharesDir),
	}

	go func() {
		log.Info("gRPC server listening", "addr", cfg.GRPCAddr)
		if err := grpcServer.Serve(grpcListener); err != nil {
			log.Error("gRPC server stopped", "err", err)
		}
	}()

	go func() {
		log.Info("health server listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("health server stopped", "err", err)
		}
	}()

	<-ctx.Done()
	log.Info("shutdown started")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer shutdownCancel()

	gracefulDone := make(chan struct{})
	go func() {
		grpcServer.GracefulStop()
		close(gracefulDone)
	}()

	select {
	case <-gracefulDone:
	case <-shutdownCtx.Done():
		grpcServer.Stop()
	}

	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("failed to shutdown health server", "err", err)
	}

	log.Info("shutdown complete")
}

func shareEncryptionKey(apiKey string) []byte {
	sum := sha256.Sum256([]byte(apiKey))
	return sum[:]
}

func runSessionCleanup(ctx context.Context, store *session.Store) {
	ticker := time.NewTicker(sessionCleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			store.Cleanup(sessionRetention)
		}
	}
}
