package config_test

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadBuildsBoundPrimaryAndRecoveryStores(t *testing.T) {
	setRequiredEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.PrimaryStore.PartyID(), "co-signer-primary"; got != want {
		t.Errorf("primary PartyID() = %q, want %q", got, want)
	}
	if got, want := cfg.RecoveryStore.PartyID(), "co-signer-recovery"; got != want {
		t.Errorf("recovery PartyID() = %q, want %q", got, want)
	}
	if got, want := cfg.PrimaryStore.KeyRef(), "keyref-1"; got != want {
		t.Errorf("primary KeyRef() = %q, want %q", got, want)
	}
	if got, want := cfg.RecoveryStore.KeyRef(), "keyref-1"; got != want {
		t.Errorf("recovery KeyRef() = %q, want %q", got, want)
	}
	if cfg.PreParamsGenerationParallelism != 2 {
		t.Errorf("PreParamsGenerationParallelism = %d, want 2", cfg.PreParamsGenerationParallelism)
	}
	if cfg.FreeSpaceThresholdBytes != 1<<20 {
		t.Errorf("FreeSpaceThresholdBytes = %d, want %d", cfg.FreeSpaceThresholdBytes, 1<<20)
	}
	if got, want := cfg.LockPath, "/var/lib/co-signer/primary/.co-signer.lock"; got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
}

func TestLoadUsesProvisioningCapacityDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES", "")
	t.Setenv("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.FreeSpaceThresholdBytes, uint64(1<<30); got != want {
		t.Errorf("FreeSpaceThresholdBytes = %d, want %d", got, want)
	}
	if got, want := cfg.PreParamsGenerationParallelism, 2; got != want {
		t.Errorf("PreParamsGenerationParallelism = %d, want %d", got, want)
	}
}

func TestLoadRejectsNonPositiveOrInvalidFreeSpaceThreshold(t *testing.T) {
	for _, value := range []string{"0", "-1", "18446744073709551616"} {
		t.Run(value, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES", value)

			if _, err := config.Load(); err == nil {
				t.Fatal("Load() error = nil, want free-space threshold rejection")
			}
		})
	}
}

func TestLoadRejectsZeroGenerationParallelism(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM", "0")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want generation parallelism rejection")
	}
}

func TestLoadRejectsObsoleteStateAndLockSettings(t *testing.T) {
	for _, variable := range []string{"CO_SIGNER_STATE_DIR", "CO_SIGNER_LOCK_PATH"} {
		t.Run(variable, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv(variable, "/obsolete")

			if _, err := config.Load(); err == nil {
				t.Fatalf("Load() error = nil, want obsolete %s rejection", variable)
			}
		})
	}
}

func TestLoadRejectsLegacySingleStoreSettings(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_PARTY_ID", "co-signer")
	t.Setenv("CO_SIGNER_SHARES_DIR", "/var/lib/co-signer/shares")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want legacy configuration error")
	}
}

func TestLoadRejectsLegacyEncryptionKeyReferenceVariable(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY_ID", "")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY_REF", "legacy-keyref")

	if _, err := config.Load(); err == nil {
		t.Fatal("Load() error = nil, want legacy key reference variable rejection")
	}
}

func TestLoadRejectsRelativeOrOverlappingStorePaths(t *testing.T) {
	for _, tc := range []struct {
		name     string
		primary  string
		recovery string
	}{
		{name: "relative primary", primary: "./primary", recovery: "/var/lib/co-signer/recovery"},
		{name: "same directory", primary: "/var/lib/co-signer/stores", recovery: "/var/lib/co-signer/stores"},
		{name: "overlapping directory", primary: "/var/lib/co-signer/stores", recovery: "/var/lib/co-signer/stores/recovery"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			setRequiredEnv(t)
			t.Setenv("CO_SIGNER_PRIMARY_SHARES_DIR", tc.primary)
			t.Setenv("CO_SIGNER_RECOVERY_SHARES_DIR", tc.recovery)

			if _, err := config.Load(); err == nil {
				t.Fatal("Load() error = nil, want path validation error")
			}
		})
	}
}

func TestLoadRejectsInvalidKeyWithoutLeakingSecret(t *testing.T) {
	setRequiredEnv(t)
	secret := "not-a-base64-secret"
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", secret)

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() error = nil, want key validation error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Load() error leaked encryption key: %v", err)
	}
}

func TestLoadedConfigRedactsEncryptionKeyWhenFormatted(t *testing.T) {
	setRequiredEnv(t)
	secret := base64.StdEncoding.EncodeToString([]byte("01234567890123456789012345678901"))
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", secret)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if rendered := fmt.Sprintf("%+v", cfg); strings.Contains(rendered, secret) || strings.Contains(rendered, "01234567890123456789012345678901") {
		t.Fatalf("formatted config leaked encryption key: %s", rendered)
	}
}

func TestLoadUsesRuntimeDefaults(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_POLL_MAX_INTERVAL", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MaxConcurrent != 4 || cfg.PollMinInterval != 2*time.Second || cfg.PollMaxInterval != 10*time.Second || cfg.FramePollInterval != 500*time.Millisecond {
		t.Fatalf("unexpected runtime defaults: %+v", cfg)
	}
}

func setRequiredEnv(t *testing.T) {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for name, value := range map[string]string{
		"CO_SIGNER_MONOLITH_URL":                     "https://monolith.test",
		"CO_SIGNER_API_KEY_ID":                       "key-1",
		"CO_SIGNER_API_PRIVATE_KEY":                  "cHJpdmF0ZS1rZXk=",
		"CO_SIGNER_PRIMARY_SHARES_DIR":               "/var/lib/co-signer/primary",
		"CO_SIGNER_RECOVERY_SHARES_DIR":              "/var/lib/co-signer/recovery",
		"CO_SIGNER_SHARE_ENCRYPTION_KEY":             key,
		"CO_SIGNER_SHARE_ENCRYPTION_KEY_ID":          "keyref-1",
		"CO_SIGNER_SHARE_ENCRYPTION_KEY_REF":         "",
		"CO_SIGNER_STATE_DIR":                        "",
		"CO_SIGNER_LOCK_PATH":                        "",
		"CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES":       "1048576",
		"CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM": "2",
		"CO_SIGNER_PARTY_ID":                         "",
		"CO_SIGNER_SHARES_DIR":                       "",
	} {
		t.Setenv(name, value)
	}
}
