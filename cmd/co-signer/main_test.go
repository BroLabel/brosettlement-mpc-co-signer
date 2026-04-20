package main

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"strings"
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

func TestDecodePrivateKeyPEM(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("a", 64)))
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	key, err := decodePrivateKey(string(pemBytes))
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if string(key) != string(privateKey) {
		t.Fatal("decoded key does not match original private key")
	}
}

func TestDecodePrivateKeyPEMWithEscapedNewlines(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(strings.NewReader(strings.Repeat("b", 64)))
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	escaped := strings.ReplaceAll(string(pemBytes), "\n", "\\n")

	key, err := decodePrivateKey(escaped)
	if err != nil {
		t.Fatalf("decodePrivateKey() error = %v", err)
	}
	if string(key) != string(privateKey) {
		t.Fatal("decoded key does not match original private key")
	}
}

func TestDecodePrivateKeyPEMRejectsNonEd25519(t *testing.T) {
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey() error = %v", err)
	}

	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey() error = %v", err)
	}

	block := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})

	if _, err := decodePrivateKey(string(block)); err == nil {
		t.Fatal("decodePrivateKey() error = nil, want non-nil")
	}
}

func TestDecodePrivateKeyInvalid(t *testing.T) {
	if _, err := decodePrivateKey("not-a-key"); err == nil {
		t.Fatal("decodePrivateKey() error = nil, want non-nil")
	}
}
