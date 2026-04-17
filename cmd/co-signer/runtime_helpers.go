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
	"strings"
)

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
