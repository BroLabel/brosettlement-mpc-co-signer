package main

import (
	"context"
	"encoding/base64"
	"encoding/hex"
	"testing"
	"time"
)

func TestDrainWorkersConsumesAllSemaphoreSlots(t *testing.T) {
	sem := make(chan struct{}, 2)
	sem <- struct{}{}

	go func() {
		time.Sleep(time.Millisecond)
		<-sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	if err := drainWorkers(ctx, sem); err != nil {
		t.Fatalf("drainWorkers() error = %v, want nil", err)
	}
}

func TestDecodePrivateKeyHex(t *testing.T) {
	key, err := decodePrivateKey(hex.EncodeToString(make([]byte, 64)))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if len(key) != 64 {
		t.Fatalf("decoded key length = %d, want 64", len(key))
	}
}

func TestDecodePrivateKeyBase64(t *testing.T) {
	key, err := decodePrivateKey(base64.StdEncoding.EncodeToString(make([]byte, 64)))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if len(key) != 64 {
		t.Fatalf("decoded key length = %d, want 64", len(key))
	}
}

func TestDecodePrivateKeyInvalid(t *testing.T) {
	if _, err := decodePrivateKey("not-a-key"); err == nil {
		t.Fatal("decodePrivateKey() error = nil, want non-nil")
	}
}
