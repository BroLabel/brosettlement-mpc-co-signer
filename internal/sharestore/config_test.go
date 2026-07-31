package sharestore

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func TestNewKeyProviderRequiresCanonicalStandardBase64AES256Key(t *testing.T) {
	valid := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if _, err := NewKeyProvider(valid, "keyref-1"); err != nil {
		t.Fatalf("NewKeyProvider() error = %v", err)
	}

	for _, raw := range []string{
		base64.StdEncoding.EncodeToString(make([]byte, 31)),
		base64.StdEncoding.EncodeToString(make([]byte, 33)),
		"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=A",
		strings.TrimRight(valid, "="),
		valid + "\n",
	} {
		t.Run(raw, func(t *testing.T) {
			if _, err := NewKeyProvider(raw, "keyref-1"); err == nil {
				t.Fatal("NewKeyProvider() error = nil, want strict key validation error")
			}
		})
	}
}

func TestNewKeyProviderRedactsRawKey(t *testing.T) {
	secret := "this-is-not-base64"
	_, err := NewKeyProvider(secret, "keyref-1")
	if err == nil {
		t.Fatal("NewKeyProvider() error = nil, want error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("NewKeyProvider() error leaked key: %v", err)
	}
}

func TestNewKeyProviderRejectsNonCanonicalKeyReferenceWithoutEchoingIt(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(make([]byte, 32))
	for _, keyRef := range []string{
		" keyref-1",
		"keyref-1 ",
		"keyref\n1",
		"keyref-☃",
		strings.Repeat("a", 256),
	} {
		t.Run(fmt.Sprintf("%q", keyRef), func(t *testing.T) {
			_, err := NewKeyProvider(key, keyRef)
			if err == nil {
				t.Fatal("NewKeyProvider() error = nil, want invalid key reference error")
			}
			if strings.Contains(err.Error(), keyRef) {
				t.Fatalf("NewKeyProvider() error echoed key reference: %v", err)
			}
		})
	}
}

func TestKeyProviderDoesNotExposeKeyBytesWhenFormatted(t *testing.T) {
	raw := []byte("01234567890123456789012345678901")
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(raw), "keyref-1")
	if err != nil {
		t.Fatalf("NewKeyProvider() error = %v", err)
	}
	if rendered := fmt.Sprintf("%+v", provider); strings.Contains(rendered, "48 49 50 51") || strings.Contains(rendered, string(raw)) {
		t.Fatalf("formatted provider leaked key material: %s", rendered)
	}
}

func TestStoreConfigsRequireFixedPartyPurposePairAndSharedProvider(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	primary, err := NewStoreConfig("deployment-1", StorePurposePrimary, "co-signer-primary", "/var/lib/co-signer/primary", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(primary) error = %v", err)
	}
	recovery, err := NewStoreConfig("deployment-1", StorePurposeRecovery, "co-signer-recovery", "/var/lib/co-signer/recovery", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(recovery) error = %v", err)
	}
	if err := ValidateStorePair(primary, recovery); err != nil {
		t.Fatalf("ValidateStorePair() error = %v", err)
	}

	if _, err := NewStoreConfig("deployment-1", StorePurposePrimary, "co-signer-recovery", "/var/lib/co-signer/invalid", provider); err == nil {
		t.Fatal("NewStoreConfig() error = nil, want party/purpose mismatch")
	}
	changedProvider := testKeyProvider(t, "keyref-2")
	changedRef, err := NewStoreConfig("deployment-1", StorePurposeRecovery, "co-signer-recovery", "/var/lib/co-signer/recovery-2", changedProvider)
	if err != nil {
		t.Fatalf("NewStoreConfig(changed keyRef) error = %v", err)
	}
	if err := ValidateStorePair(primary, changedRef); err == nil {
		t.Fatal("ValidateStorePair() error = nil, want changed keyRef rejection")
	}
	mismatchedDeployment, err := NewStoreConfig("deployment-2", StorePurposeRecovery, "co-signer-recovery", "/var/lib/co-signer/recovery-3", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(mismatched deployment) error = %v", err)
	}
	if err := ValidateStorePair(primary, mismatchedDeployment); err == nil {
		t.Fatal("ValidateStorePair() error = nil, want deployment ID mismatch rejection")
	}
}

func TestNewStoreConfigRejectsNonCanonicalDeploymentIDWithoutEchoingIt(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	for _, deploymentID := range []string{
		" deployment-1",
		"deployment-1 ",
		"deployment 1",
		"deployment/1",
		"_deployment-1",
		"-deployment-1",
		"deployment\x001",
		"deployment-☃",
		strings.Repeat("d", 256),
	} {
		t.Run(fmt.Sprintf("%q", deploymentID), func(t *testing.T) {
			_, err := NewStoreConfig(deploymentID, StorePurposePrimary, "co-signer-primary", "/var/lib/co-signer/primary", provider)
			if err == nil {
				t.Fatal("NewStoreConfig() error = nil, want invalid deployment ID error")
			}
			if strings.Contains(err.Error(), deploymentID) {
				t.Fatalf("NewStoreConfig() error echoed deployment ID: %v", err)
			}
		})
	}
}

func TestNewStoreConfigRejectsBothWrongPartyBindings(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	for _, tc := range []struct {
		name    string
		purpose StorePurpose
		partyID string
	}{
		{name: "primary given recovery party", purpose: StorePurposePrimary, partyID: "co-signer-recovery"},
		{name: "recovery given primary party", purpose: StorePurposeRecovery, partyID: "co-signer-primary"},
		{name: "primary padded with whitespace", purpose: StorePurposePrimary, partyID: " co-signer-primary "},
		{name: "recovery padded with whitespace", purpose: StorePurposeRecovery, partyID: " co-signer-recovery "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewStoreConfig("deployment-1", tc.purpose, tc.partyID, "/var/lib/co-signer/store", provider); err == nil {
				t.Fatal("NewStoreConfig() error = nil, want party binding rejection")
			}
		})
	}
}

func TestStoreConfigDoesNotExposeProviderKeyBytesWhenFormatted(t *testing.T) {
	raw := []byte("01234567890123456789012345678901")
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(raw), "keyref-1")
	if err != nil {
		t.Fatalf("NewKeyProvider() error = %v", err)
	}
	store, err := NewStoreConfig("deployment-1", StorePurposePrimary, "co-signer-primary", "/var/lib/co-signer/primary", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig() error = %v", err)
	}
	if rendered := fmt.Sprintf("%+v", store); strings.Contains(rendered, "48 49 50 51") || strings.Contains(rendered, string(raw)) {
		t.Fatalf("formatted store config leaked key material: %s", rendered)
	}
}

func TestStoreConfigsRejectRelativeOrOverlappingDirectories(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	if _, err := NewStoreConfig("deployment-1", StorePurposePrimary, "co-signer-primary", "relative", provider); err == nil {
		t.Fatal("NewStoreConfig() error = nil, want relative directory rejection")
	}
	primary, err := NewStoreConfig("deployment-1", StorePurposePrimary, "co-signer-primary", "/var/lib/co-signer/stores", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(primary) error = %v", err)
	}
	recovery, err := NewStoreConfig("deployment-1", StorePurposeRecovery, "co-signer-recovery", "/var/lib/co-signer/stores/recovery", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(recovery) error = %v", err)
	}
	if err := ValidateStorePair(primary, recovery); err == nil {
		t.Fatal("ValidateStorePair() error = nil, want overlapping directory rejection")
	}
}

func TestNewLegacyPrimaryFileStoreRejectsRecoveryProfile(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	recovery, err := NewStoreConfig("deployment-1", StorePurposeRecovery, "co-signer-recovery", t.TempDir(), provider)
	if err != nil {
		t.Fatalf("NewStoreConfig() error = %v", err)
	}
	if _, err := NewLegacyPrimaryFileStore(recovery); err == nil {
		t.Fatal("NewLegacyPrimaryFileStore() error = nil, want recovery profile rejection")
	}
}

func testKeyProvider(t *testing.T, keyRef string) *KeyProvider {
	t.Helper()
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(make([]byte, 32)), keyRef)
	if err != nil {
		t.Fatalf("NewKeyProvider() error = %v", err)
	}
	return provider
}
