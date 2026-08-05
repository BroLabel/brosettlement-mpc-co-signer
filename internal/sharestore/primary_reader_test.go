package sharestore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
)

func TestPrimaryReaderRejectsArtifactStoredUnderAnotherKeyPath(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	artifactBytes, err := encodeArtifactV1(store.config, PublishInput{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PartyID:         primaryPartyID,
		DescriptorBytes: testDescriptor(t, testKeyID),
		CodecBlob:       testCodecBlob(t),
	}, bytes.Repeat([]byte{0x77}, artifactNonceBytes))
	if err != nil {
		t.Fatalf("encodeArtifactV1() error = %v", err)
	}

	requestedKeyID := "mpc_key_123e4567-e89b-42d3-a456-426614174099"
	wrongPath, err := store.finalPath(requestedKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(wrongPath, artifactBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	reader, err := NewPrimaryReader(store)
	if err != nil {
		t.Fatal(err)
	}
	share, err := reader.LoadShare(context.Background(), requestedKeyID)
	if share != nil {
		clear(share.Blob)
		t.Fatal("LoadShare() returned material for a mismatched descriptor key")
	}
	if !errors.Is(err, ErrArtifactBinding) {
		t.Fatalf("LoadShare() error = %v, want ErrArtifactBinding", err)
	}
}
