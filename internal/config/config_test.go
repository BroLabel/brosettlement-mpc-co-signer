package config_test

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
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
	if got, want := cfg.LockPath, "/var/lib/co-signer/primary/.co-signer.lock"; got != want {
		t.Errorf("LockPath = %q, want %q", got, want)
	}
}

func TestLoadUsesPreParamsGenerationDefault(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM", "")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if got, want := cfg.PreParamsGenerationParallelism, 2; got != want {
		t.Errorf("PreParamsGenerationParallelism = %d, want %d", got, want)
	}
}

func TestLoadIgnoresRemovedFreeSpaceThreshold(t *testing.T) {
	setRequiredEnv(t)
	t.Setenv("CO_SIGNER_FREE_SPACE_THRESHOLD_BYTES", "not-a-number")

	if _, err := config.Load(); err != nil {
		t.Fatalf("Load() error = %v, want removed free-space setting ignored", err)
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
		"CO_SIGNER_APPROVAL_MODE":                    "",
		"CO_SIGNER_APPROVAL_JOURNAL_DIR":             "",
		"CO_SIGNER_APPROVAL_ENDPOINT":                "",
		"CO_SIGNER_APPROVAL_BEARER_TOKEN":            "",
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
		"CO_SIGNER_PREPARAMS_GENERATION_PARALLELISM": "2",
		"CO_SIGNER_PARTY_ID":                         "",
		"CO_SIGNER_SHARES_DIR":                       "",
	} {
		t.Setenv(name, value)
	}
}

func TestApprovalConfigurationFailsClosed(t *testing.T) {
	for _, values := range []map[string]string{
		{"CO_SIGNER_APPROVAL_MODE": "invalid"},
		{"CO_SIGNER_APPROVAL_MODE": "external"},
		{"CO_SIGNER_APPROVAL_MODE": "auto_approve", "CO_SIGNER_POLL_BACKOFF_FACTOR": "NaN"},
	} {
		t.Run(fmt.Sprint(values), func(t *testing.T) {
			setRequiredEnv(t)
			for k, v := range values {
				t.Setenv(k, v)
			}
			if _, err := config.Load(); err == nil {
				t.Fatal("unsafe approval configuration admitted")
			}
		})
	}
}

func setApprovalEnv(t *testing.T) {
	t.Helper()
	setRequiredEnv(t)
	dir := t.TempDir()
	os.Chmod(dir, 0700)
	for k, v := range map[string]string{"CO_SIGNER_APPROVAL_MODE": "external", "CO_SIGNER_APPROVAL_JOURNAL_DIR": dir, "CO_SIGNER_APPROVAL_ENDPOINT": "https://org.test/exact?integration=1", "CO_SIGNER_APPROVAL_BEARER_TOKEN": "dedicated-secret", "CO_SIGNER_HTTP_TIMEOUT": "1s", "CO_SIGNER_POLL_MIN_INTERVAL": "1s", "CO_SIGNER_POLL_MAX_INTERVAL": "10s", "CO_SIGNER_POLL_BACKOFF_FACTOR": "1.5"} {
		t.Setenv(k, v)
	}
}
func TestApprovalDefaultsToAutoApproveWhenModeIsUnset(t *testing.T) {
	setRequiredEnv(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval == nil || cfg.Approval.Mode != "auto_approve" || cfg.Approval.JournalDir != "/var/lib/co-signer/approval" {
		t.Fatal("approval must default to auto_approve with the default durable journal")
	}
}

func TestApprovalDoesNotRequireEnvironment(t *testing.T) {
	setApprovalEnv(t)
	if _, err := config.Load(); err != nil {
		t.Fatal(err)
	}
}

func TestApprovalModesUseSiblingStorageWithoutJournalEnv(t *testing.T) {
	for _, mode := range []string{"auto_approve", "external"} {
		t.Run(mode, func(t *testing.T) {
			setRequiredEnv(t)
			root := t.TempDir()
			t.Setenv("CO_SIGNER_PRIMARY_SHARES_DIR", filepath.Join(root, "primary"))
			t.Setenv("CO_SIGNER_RECOVERY_SHARES_DIR", filepath.Join(root, "recovery"))
			t.Setenv("CO_SIGNER_APPROVAL_MODE", mode)
			if mode == "external" {
				t.Setenv("CO_SIGNER_APPROVAL_ENDPOINT", "https://org.test/approve")
				t.Setenv("CO_SIGNER_APPROVAL_BEARER_TOKEN", "dedicated-secret")
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Approval.JournalDir != filepath.Join(root, "approval") || !cfg.Approval.AutoInitializeJournal || cfg.Approval.Mode != mode {
				t.Fatal("journal default changed decision mode or did not follow share storage")
			}
		})
	}
}

func TestExternalApprovalUsesDefaultJournalWithoutPinnedOrganizationOrRevision(t *testing.T) {
	setApprovalEnv(t)
	t.Setenv("CO_SIGNER_APPROVAL_JOURNAL_DIR", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Approval == nil || cfg.Approval.JournalDir != "/var/lib/co-signer/approval" {
		t.Fatal("external approval did not use the default journal")
	}
}

func TestApprovalSettingsEnforceTrustedBoundaryAndRedactCredential(t *testing.T) {
	setApprovalEnv(t)
	cfg, e := config.Load()
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Approval == nil || cfg.Approval.Mode != "external" {
		t.Fatal("explicit approval settings lost")
	}
	if strings.Contains(fmt.Sprintf("%+v", cfg), "dedicated-secret") {
		t.Fatal("formatted configuration leaked bearer credential")
	}
	for _, tc := range []struct{ k, v string }{
		{"CO_SIGNER_APPROVAL_MODE", "automatic"}, {"CO_SIGNER_APPROVAL_ENDPOINT", ""}, {"CO_SIGNER_APPROVAL_BEARER_TOKEN", ""},
		{"CO_SIGNER_APPROVAL_ENDPOINT", "http://org.test"}, {"CO_SIGNER_APPROVAL_ENDPOINT", "https://user:password@org.test"}, {"CO_SIGNER_APPROVAL_ENDPOINT", "https://org.test/#fragment"}, {"CO_SIGNER_APPROVAL_BEARER_TOKEN", "secret\r\nheader"}, {"CO_SIGNER_APPROVAL_JOURNAL_DIR", "relative"}, {"CO_SIGNER_POLL_BACKOFF_FACTOR", "NaN"}, {"CO_SIGNER_POLL_BACKOFF_FACTOR", "+Inf"}, {"CO_SIGNER_POLL_BACKOFF_FACTOR", "0.9"}, {"CO_SIGNER_HTTP_TIMEOUT", "0s"},
	} {
		t.Run(tc.k+tc.v, func(t *testing.T) {
			setApprovalEnv(t)
			t.Setenv(tc.k, tc.v)
			if _, e := config.Load(); e == nil {
				t.Fatal("unsafe approval setting accepted")
			}
		})
	}
}
func TestApprovalRejectsCanonicalShareDirectoryOverlap(t *testing.T) {
	setApprovalEnv(t)
	dir := t.TempDir()
	shares := filepath.Join(dir, "shares")
	os.Mkdir(shares, 0700)
	alias := filepath.Join(dir, "alias")
	if e := os.Symlink(shares, alias); e != nil {
		t.Fatal(e)
	}
	t.Setenv("CO_SIGNER_PRIMARY_SHARES_DIR", shares)
	t.Setenv("CO_SIGNER_APPROVAL_JOURNAL_DIR", filepath.Join(alias, "journal"))
	if _, e := config.Load(); e == nil {
		t.Fatal("canonical share/journal overlap accepted")
	}
}
