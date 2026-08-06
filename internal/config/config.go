package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/joho/godotenv"
)

type Config struct {
	MonolithURL                    string
	APIKeyID                       string
	APIPrivateKey                  string
	HTTPAddr                       string
	PrimaryStore                   sharestore.StoreConfig
	RecoveryStore                  sharestore.StoreConfig
	StateDir                       string
	LockPath                       string
	FreeSpaceThresholdBytes        uint64
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

	freeSpaceThresholdBytes, err := requiredPositiveUint64("CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES")
	if err != nil {
		return Config{}, err
	}
	preParamsGenerationParallelism, err := requiredInt("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM")
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
	primaryStore, err := sharestore.NewStoreConfig(sharestore.StorePurposePrimary, os.Getenv("CO_SIGNER_PRIMARY_PARTY_ID"), os.Getenv("CO_SIGNER_PRIMARY_SHARES_DIR"), provider)
	if err != nil {
		return Config{}, fmt.Errorf("configure primary store: %w", err)
	}
	recoveryStore, err := sharestore.NewStoreConfig(sharestore.StorePurposeRecovery, os.Getenv("CO_SIGNER_RECOVERY_PARTY_ID"), os.Getenv("CO_SIGNER_RECOVERY_SHARES_DIR"), provider)
	if err != nil {
		return Config{}, fmt.Errorf("configure recovery store: %w", err)
	}
	if err := sharestore.ValidateStorePair(primaryStore, recoveryStore); err != nil {
		return Config{}, fmt.Errorf("configure store pair: %w", err)
	}

	stateDir, err := absolutePath("CO_SIGNER_STATE_DIR", os.Getenv("CO_SIGNER_STATE_DIR"))
	if err != nil {
		return Config{}, err
	}
	lockPath, err := absolutePath("CO_SIGNER_LOCK_PATH", os.Getenv("CO_SIGNER_LOCK_PATH"))
	if err != nil {
		return Config{}, err
	}
	if !pathWithin(lockPath, stateDir) || lockPath == stateDir {
		return Config{}, errors.New("CO_SIGNER_LOCK_PATH must be inside CO_SIGNER_STATE_DIR")
	}

	cfg := Config{
		MonolithURL:                    os.Getenv("CO_SIGNER_MONOLITH_URL"),
		APIKeyID:                       os.Getenv("CO_SIGNER_API_KEY_ID"),
		APIPrivateKey:                  os.Getenv("CO_SIGNER_API_PRIVATE_KEY"),
		HTTPAddr:                       httpAddr(),
		PrimaryStore:                   primaryStore,
		RecoveryStore:                  recoveryStore,
		StateDir:                       stateDir,
		LockPath:                       lockPath,
		FreeSpaceThresholdBytes:        freeSpaceThresholdBytes,
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

func absolutePath(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	path := filepath.Clean(value)
	if !filepath.IsAbs(path) || path == string(filepath.Separator) {
		return "", fmt.Errorf("%s must be a non-root absolute path", name)
	}
	return path, nil
}

func pathWithin(path, parent string) bool {
	relative, err := filepath.Rel(parent, path)
	if err != nil {
		return false
	}
	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
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

func envUint64(key string, fallback uint64) (uint64, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s must be an unsigned integer: %w", key, err)
	}
	return parsed, nil
}

func requiredInt(key string) (int, error) {
	if os.Getenv(key) == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	return envInt(key, 0)
}

func requiredUint64(key string) (uint64, error) {
	if os.Getenv(key) == "" {
		return 0, fmt.Errorf("%s is required", key)
	}
	return envUint64(key, 0)
}

func requiredPositiveUint64(key string) (uint64, error) {
	value, err := requiredUint64(key)
	if err != nil {
		return 0, err
	}
	if value == 0 {
		return 0, fmt.Errorf("%s must be > 0", key)
	}
	return value, nil
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
