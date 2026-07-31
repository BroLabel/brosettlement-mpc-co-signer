package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
)

type artifactCapabilityProber interface {
	ProbePublishCapability(context.Context) error
}

type admissionHinter interface {
	AdmissionHint() bool
}

type freeSpaceReader func(path string) (uint64, error)

func probeArtifactStores(ctx context.Context, primary, recovery artifactCapabilityProber) error {
	if primary == nil || recovery == nil {
		return errors.New("both artifact store capabilities are required")
	}
	if err := primary.ProbePublishCapability(ctx); err != nil {
		return fmt.Errorf("primary artifact store capability: %w", err)
	}
	if err := recovery.ProbePublishCapability(ctx); err != nil {
		return fmt.Errorf("recovery artifact store capability: %w", err)
	}
	return nil
}

func dkgProvisioningAdmissionHint(
	preparams admissionHinter,
	artifactDirectories []string,
	freeSpaceThreshold uint64,
	freeSpace freeSpaceReader,
) bool {
	if preparams == nil || !preparams.AdmissionHint() || freeSpaceThreshold == 0 || freeSpace == nil {
		return false
	}
	for _, directory := range artifactDirectories {
		available, err := freeSpace(directory)
		if err != nil || available < freeSpaceThreshold {
			return false
		}
	}
	return true
}

func decodePrivateKey(raw string) (ed25519.PrivateKey, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, errors.New("private key is empty")
	}

	if block, _ := pem.Decode([]byte(normalizePEMEnv(trimmed))); block != nil {
		key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("private key PEM must be valid PKCS#8: %w", err)
		}

		edKey, ok := key.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("private key PEM must contain Ed25519 private key, got %T", key)
		}
		return edKey, nil
	}

	bytes, err := hex.DecodeString(trimmed)
	if err != nil {
		bytes, err = base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return nil, fmt.Errorf("private key must be PEM, hex or base64: %w", err)
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

func normalizePEMEnv(raw string) string {
	if strings.Contains(raw, `\n`) {
		return strings.ReplaceAll(raw, `\n`, "\n")
	}
	return raw
}

func serveHealth(log *slog.Logger, srv *http.Server) {
	log.Info("health server listening", "addr", srv.Addr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("health server stopped", "err", err)
	}
}

func serveHealthListener(log *slog.Logger, srv *http.Server, listener net.Listener) {
	log.Info("health server listening", "addr", listener.Addr().String())
	if err := srv.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("health server stopped", "err", err)
	}
}

type staticInspectionPreflight struct {
	err error
}

func (p staticInspectionPreflight) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.err
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
