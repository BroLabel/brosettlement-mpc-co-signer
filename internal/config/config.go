package config

import (
	"errors"
	"os"
)

type Config struct {
	APIKey    string
	PartyID   string
	GRPCAddr  string
	HTTPAddr  string
	SharesDir string
}

func Load() (Config, error) {
	cfg := Config{
		APIKey:    os.Getenv("CO_SIGNER_API_KEY"),
		PartyID:   os.Getenv("CO_SIGNER_PARTY_ID"),
		GRPCAddr:  envString("CO_SIGNER_GRPC_ADDR", "0.0.0.0:50051"),
		HTTPAddr:  envString("CO_SIGNER_HTTP_ADDR", "0.0.0.0:8081"),
		SharesDir: envString("CO_SIGNER_SHARES_DIR", "./data/shares"),
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("CO_SIGNER_API_KEY is required")
	}
	if cfg.PartyID == "" {
		return Config{}, errors.New("CO_SIGNER_PARTY_ID is required")
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
