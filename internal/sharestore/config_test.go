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

func TestStoreConfigsDeriveFixedPartyFromPurposeAndRequireSharedProvider(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	primary, err := NewStoreConfig(StorePurposePrimary, "/var/lib/co-signer/primary", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(primary) error = %v", err)
	}
	recovery, err := NewStoreConfig(StorePurposeRecovery, "/var/lib/co-signer/recovery", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(recovery) error = %v", err)
	}
	if primary.PartyID() != "co-signer-primary" || recovery.PartyID() != "co-signer-recovery" {
		t.Fatalf("derived parties = %q, %q", primary.PartyID(), recovery.PartyID())
	}
	if err := ValidateStorePair(primary, recovery); err != nil {
		t.Fatalf("ValidateStorePair() error = %v", err)
	}

	changedProvider := testKeyProvider(t, "keyref-2")
	changedRef, err := NewStoreConfig(StorePurposeRecovery, "/var/lib/co-signer/recovery-2", changedProvider)
	if err != nil {
		t.Fatalf("NewStoreConfig(changed keyRef) error = %v", err)
	}
	if err := ValidateStorePair(primary, changedRef); err == nil {
		t.Fatal("ValidateStorePair() error = nil, want changed keyRef rejection")
	}
}

func TestStoreConfigDoesNotExposeProviderKeyBytesWhenFormatted(t *testing.T) {
	raw := []byte("01234567890123456789012345678901")
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(raw), "keyref-1")
	if err != nil {
		t.Fatalf("NewKeyProvider() error = %v", err)
	}
	store, err := NewStoreConfig(StorePurposePrimary, "/var/lib/co-signer/primary", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig() error = %v", err)
	}
	if rendered := fmt.Sprintf("%+v", store); strings.Contains(rendered, "48 49 50 51") || strings.Contains(rendered, string(raw)) {
		t.Fatalf("formatted store config leaked key material: %s", rendered)
	}
}

func TestStoreConfigsRejectRelativeOrOverlappingDirectories(t *testing.T) {
	provider := testKeyProvider(t, "keyref-1")
	if _, err := NewStoreConfig(StorePurposePrimary, "relative", provider); err == nil {
		t.Fatal("NewStoreConfig() error = nil, want relative directory rejection")
	}
	primary, err := NewStoreConfig(StorePurposePrimary, "/var/lib/co-signer/stores", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(primary) error = %v", err)
	}
	recovery, err := NewStoreConfig(StorePurposeRecovery, "/var/lib/co-signer/stores/recovery", provider)
	if err != nil {
		t.Fatalf("NewStoreConfig(recovery) error = %v", err)
	}
	if err := ValidateStorePair(primary, recovery); err == nil {
		t.Fatal("ValidateStorePair() error = nil, want overlapping directory rejection")
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
