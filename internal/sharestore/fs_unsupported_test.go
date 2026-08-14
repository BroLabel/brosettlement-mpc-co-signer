//go:build !linux && !darwin

package sharestore

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"testing"
)

func TestOpenStoreRetainsBindingAndReportsUnsupportedPublishCapability(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := NewStoreConfig(StorePurposePrimary, directory, provider)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(config)
	if !errors.Is(err, ErrUnsupportedPublishPlatform) {
		t.Fatalf("OpenStore() error = %v, want ErrUnsupportedPublishPlatform", err)
	}
	if store == nil || store.config.Purpose() != StorePurposePrimary {
		t.Fatalf("OpenStore() store = %#v, want retained primary binding", store)
	}
}
