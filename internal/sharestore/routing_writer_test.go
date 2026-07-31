package sharestore

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"testing"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func TestRoutingWriterFailsTypedBeforeFilesystemWithoutRegisteredPair(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	primary := testStoreWithProvider(t, StorePurposePrimary, provider)
	recovery := testStoreWithProvider(t, StorePurposeRecovery, provider)
	writer, err := NewRoutingWriter(NewActivePair(), primary, recovery)
	if err != nil {
		t.Fatalf("NewRoutingWriter() error = %v", err)
	}
	var _ coretss.ShareWriter = writer

	err = writer.SaveShare(context.Background(), coretss.SaveShareInput{
		SessionID:                   testSessionID,
		KeyID:                       testKeyID,
		LocalPartyID:                primaryPartyID,
		OpaqueDescriptorFingerprint: fingerprintBytes(testDescriptor(t, testKeyID)),
		CodecBlob:                   testCodecBlob(t),
	})
	if !errors.Is(err, ErrPairNotRegistered) {
		t.Fatalf("SaveShare() error = %v, want ErrPairNotRegistered", err)
	}
	for _, directory := range []string{primary.config.Directory(), recovery.config.Directory()} {
		entries, readErr := os.ReadDir(directory)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("unregistered writer mutated %q: %v", directory, entries)
		}
	}
}

func TestRoutingWriterRejectsInvalidPairBeforePublish(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	primary := testStoreWithProvider(t, StorePurposePrimary, provider)
	recovery := testStoreWithProvider(t, StorePurposeRecovery, provider)
	slot := NewActivePair()
	descriptor := testDescriptor(t, testKeyID)
	lease, err := slot.RegisterPair(PairRegistration{
		SessionID: testSessionID, KeyID: testKeyID,
		PrimaryPartyID: primaryPartyID, RecoveryPartyID: recoveryPartyID,
		DescriptorBytes: descriptor,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	writer, err := NewRoutingWriter(slot, primary, recovery)
	if err != nil {
		t.Fatal(err)
	}
	input := coretss.SaveShareInput{
		SessionID: testSessionID, KeyID: testKeyID, LocalPartyID: "mpc-signer",
		OpaqueDescriptorFingerprint: fingerprintBytes(descriptor), CodecBlob: testCodecBlob(t),
	}
	if err := writer.SaveShare(context.Background(), input); !errors.Is(err, ErrPersistenceContextMismatch) {
		t.Fatalf("SaveShare() error = %v", err)
	}
}
