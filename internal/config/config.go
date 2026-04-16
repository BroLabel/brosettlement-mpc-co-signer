package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"time"
)

type Config struct {
	MonolithURL        string
	APIKeyID           string
	APIPrivateKey      string
	ShareEncryptionKey string
	PartyID            string
	HTTPAddr           string
	SharesDir          string
	MaxConcurrent      int
	PollMinInterval    time.Duration
	PollMaxInterval    time.Duration
	PollBackoffFactor  float64
	FramePollInterval  time.Duration
	HTTPTimeout        time.Duration
}

func Load() (Config, error) {
	maxConcurrent, err := envInt("CO_SIGNER_MAX_CONCURRENT", 4)
	if err != nil {
		return Config{}, err
	}
	pollMinInterval, err := envDuration("CO_SIGNER_POLL_MIN_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	pollMaxInterval, err := envDuration("CO_SIGNER_POLL_MAX_INTERVAL", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	pollBackoffFactor, err := envFloat("CO_SIGNER_POLL_BACKOFF_FACTOR", 1.5)
	if err != nil {
		return Config{}, err
	}
	framePollInterval, err := envDuration("CO_SIGNER_FRAME_POLL_INTERVAL", 500*time.Millisecond)
	if err != nil {
		return Config{}, err
	}
	httpTimeout, err := envDuration("CO_SIGNER_HTTP_TIMEOUT", 30*time.Second)
	if err != nil {
		return Config{}, err
	}

	cfg := Config{
		MonolithURL:        os.Getenv("CO_SIGNER_MONOLITH_URL"),
		APIKeyID:           os.Getenv("CO_SIGNER_API_KEY_ID"),
		APIPrivateKey:      os.Getenv("CO_SIGNER_API_PRIVATE_KEY"),
		ShareEncryptionKey: os.Getenv("CO_SIGNER_SHARE_ENCRYPTION_KEY"),
		PartyID:            os.Getenv("CO_SIGNER_PARTY_ID"),
		HTTPAddr:           envString("CO_SIGNER_HTTP_ADDR", "0.0.0.0:8081"),
		SharesDir:          envString("CO_SIGNER_SHARES_DIR", "./data/shares"),
		MaxConcurrent:      maxConcurrent,
		PollMinInterval:    pollMinInterval,
		PollMaxInterval:    pollMaxInterval,
		PollBackoffFactor:  pollBackoffFactor,
		FramePollInterval:  framePollInterval,
		HTTPTimeout:        httpTimeout,
	}

	if cfg.MonolithURL == "" {
		return Config{}, errors.New("CO_SIGNER_MONOLITH_URL is required")
	}
	if cfg.APIKeyID == "" || cfg.APIPrivateKey == "" {
		return Config{}, errors.New("CO_SIGNER_API_KEY_ID and CO_SIGNER_API_PRIVATE_KEY are required")
	}
	if cfg.ShareEncryptionKey == "" {
		return Config{}, errors.New("CO_SIGNER_SHARE_ENCRYPTION_KEY is required")
	}
	if cfg.PartyID == "" {
		return Config{}, errors.New("CO_SIGNER_PARTY_ID is required")
	}
	if cfg.MaxConcurrent < 1 {
		return Config{}, errors.New("CO_SIGNER_MAX_CONCURRENT must be >= 1")
	}
	if cfg.PollMinInterval <= 0 {
		return Config{}, errors.New("CO_SIGNER_POLL_MIN_INTERVAL must be > 0")
	}
	if cfg.PollMaxInterval < cfg.PollMinInterval {
		return Config{}, errors.New("CO_SIGNER_POLL_MAX_INTERVAL must be >= CO_SIGNER_POLL_MIN_INTERVAL")
	}

	return cfg, nil
}

func envString(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be int: %w", key, err)
	}

	return parsed, nil
}

func envFloat(key string, fallback float64) (float64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be float: %w", key, err)
	}

	return parsed, nil
}

func envDuration(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be duration: %w", key, err)
	}

	return parsed, nil
}
