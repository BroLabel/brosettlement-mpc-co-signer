//go:build !linux

package sharestore

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

func TestNewStoreFailsUnsupportedPublishCapability(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	config, err := NewStoreConfig(StorePurposePrimary, primaryPartyID, t.TempDir(), provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(config); !errors.Is(err, ErrUnsupportedPublishPlatform) {
		t.Fatalf("NewStore() error = %v, want ErrUnsupportedPublishPlatform", err)
	}
}
