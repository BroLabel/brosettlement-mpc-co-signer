package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/joho/godotenv"
)

const (
	defaultPreParamsGenerationParallelism = 2
)

type Config struct {
	MonolithURL                    string
	APIKeyID                       string
	APIPrivateKey                  string
	HTTPAddr                       string
	PrimaryStore                   sharestore.StoreConfig
	RecoveryStore                  sharestore.StoreConfig
	LockPath                       string
	PreParamsGenerationParallelism int
	MaxConcurrent                  int
	PollMinInterval                time.Duration
	PollMaxInterval                time.Duration
	PollBackoffFactor              float64
	FramePollInterval              time.Duration
	HTTPTimeout                    time.Duration
}

func Load() (Config, error) {
	if err := loadDotEnv(); err != nil {
		return Config{}, err
	}

	maxConcurrent, err := envInt("CO_SIGNER_MAX_CONCURRENT", 4)
	if err != nil {
		return Config{}, err
	}
	pollMinInterval, err := envDuration("CO_SIGNER_POLL_MIN_INTERVAL", 2*time.Second)
	if err != nil {
		return Config{}, err
	}
	pollMaxInterval, err := envDuration("CO_SIGNER_POLL_MAX_INTERVAL", 10*time.Second)
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

	preParamsGenerationParallelism, err := envInt("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM", defaultPreParamsGenerationParallelism)
	if err != nil {
		return Config{}, err
	}

	if os.Getenv("CO_SIGNER_PARTY_ID") != "" || os.Getenv("CO_SIGNER_SHARES_DIR") != "" || os.Getenv("CO_SIGNER_SHARE_ENCRYPTION_KEY_REF") != "" {
		return Config{}, errors.New("legacy single-store and CO_SIGNER_SHARE_ENCRYPTION_KEY_REF settings are unsupported; configure explicit stores and CO_SIGNER_SHARE_ENCRYPTION_KEY_ID")
	}

	provider, err := sharestore.NewKeyProvider(os.Getenv("CO_SIGNER_SHARE_ENCRYPTION_KEY"), os.Getenv("CO_SIGNER_SHARE_ENCRYPTION_KEY_ID"))
	if err != nil {
		return Config{}, fmt.Errorf("configure share encryption key: %w", err)
	}
	primaryStore, err := sharestore.NewStoreConfig(sharestore.StorePurposePrimary, os.Getenv("CO_SIGNER_PRIMARY_SHARES_DIR"), provider)
	if err != nil {
		return Config{}, fmt.Errorf("configure primary store: %w", err)
	}
	recoveryStore, err := sharestore.NewStoreConfig(sharestore.StorePurposeRecovery, os.Getenv("CO_SIGNER_RECOVERY_SHARES_DIR"), provider)
	if err != nil {
		return Config{}, fmt.Errorf("configure recovery store: %w", err)
	}
	if err := sharestore.ValidateStorePair(primaryStore, recoveryStore); err != nil {
		return Config{}, fmt.Errorf("configure store pair: %w", err)
	}
	if os.Getenv("CO_SIGNER_STATE_DIR") != "" || os.Getenv("CO_SIGNER_LOCK_PATH") != "" {
		return Config{}, errors.New("CO_SIGNER_STATE_DIR and CO_SIGNER_LOCK_PATH are unsupported; the lifetime lock is derived from the primary store")
	}

	cfg := Config{
		MonolithURL:                    os.Getenv("CO_SIGNER_MONOLITH_URL"),
		APIKeyID:                       os.Getenv("CO_SIGNER_API_KEY_ID"),
		APIPrivateKey:                  os.Getenv("CO_SIGNER_API_PRIVATE_KEY"),
		HTTPAddr:                       httpAddr(),
		PrimaryStore:                   primaryStore,
		RecoveryStore:                  recoveryStore,
		LockPath:                       filepath.Join(primaryStore.Directory(), ".co-signer.lock"),
		PreParamsGenerationParallelism: preParamsGenerationParallelism,
		MaxConcurrent:                  maxConcurrent,
		PollMinInterval:                pollMinInterval,
		PollMaxInterval:                pollMaxInterval,
		PollBackoffFactor:              pollBackoffFactor,
		FramePollInterval:              framePollInterval,
		HTTPTimeout:                    httpTimeout,
	}

	if cfg.MonolithURL == "" {
		return Config{}, errors.New("CO_SIGNER_MONOLITH_URL is required")
	}
	if cfg.APIKeyID == "" || cfg.APIPrivateKey == "" {
		return Config{}, errors.New("CO_SIGNER_API_KEY_ID and CO_SIGNER_API_PRIVATE_KEY are required")
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
	if cfg.PreParamsGenerationParallelism < 1 {
		return Config{}, errors.New("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM must be >= 1")
	}

	return cfg, nil
}

func loadDotEnv() error {
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("load .env: %w", err)
	}
	return nil
}

func httpAddr() string {
	if value := os.Getenv("CO_SIGNER_HTTP_ADDR"); value != "" {
		return value
	}
	if port := os.Getenv("PORT"); port != "" {
		return "0.0.0.0:" + port
	}
	return "0.0.0.0:8081"
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
