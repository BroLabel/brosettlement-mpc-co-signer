package sharestore

import (
	"bytes"
	"errors"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func TestActivePairCopiesDescriptorAndUsesGenerationLease(t *testing.T) {
	slot := NewActivePair()
	descriptor := testDescriptor(t, testKeyID)
	lease, err := slot.RegisterPair(PairRegistration{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PrimaryPartyID:  primaryPartyID,
		RecoveryPartyID: recoveryPartyID,
		DescriptorBytes: descriptor,
	})
	if err != nil {
		t.Fatalf("RegisterPair() error = %v", err)
	}
	descriptor[0] ^= 0xff

	resolved, err := slot.resolve(coretss.SaveShareInput{
		SessionID:                   testSessionID,
		KeyID:                       testKeyID,
		LocalPartyID:                primaryPartyID,
		OpaqueDescriptorFingerprint: fingerprintBytes(testDescriptor(t, testKeyID)),
		CodecBlob:                   []byte("secret"),
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if !bytes.Equal(resolved.DescriptorBytes, testDescriptor(t, testKeyID)) {
		t.Fatal("active pair retained caller-owned descriptor bytes")
	}
	resolved.DescriptorBytes[0] ^= 0xff
	resolvedAgain, err := slot.resolve(coretss.SaveShareInput{
		SessionID:                   testSessionID,
		KeyID:                       testKeyID,
		LocalPartyID:                recoveryPartyID,
		OpaqueDescriptorFingerprint: fingerprintBytes(testDescriptor(t, testKeyID)),
	})
	if err != nil {
		t.Fatalf("Resolve() second error = %v", err)
	}
	if !bytes.Equal(resolvedAgain.DescriptorBytes, testDescriptor(t, testKeyID)) {
		t.Fatal("Resolve exposed mutable slot descriptor")
	}

	if err := lease.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatalf("idempotent Release() error = %v", err)
	}
	if _, err := slot.resolve(coretss.SaveShareInput{}); !errors.Is(err, ErrPairNotRegistered) {
		t.Fatalf("Resolve() after release error = %v", err)
	}

	newLease, err := slot.RegisterPair(PairRegistration{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PrimaryPartyID:  primaryPartyID,
		RecoveryPartyID: recoveryPartyID,
		DescriptorBytes: testDescriptor(t, testKeyID),
	})
	if err != nil {
		t.Fatalf("second RegisterPair() error = %v", err)
	}
	defer newLease.Release()
	if err := lease.Release(); err != nil {
		t.Fatalf("stale Release() error = %v", err)
	}
	if _, err := slot.resolve(coretss.SaveShareInput{
		SessionID:                   testSessionID,
		KeyID:                       testKeyID,
		LocalPartyID:                primaryPartyID,
		OpaqueDescriptorFingerprint: fingerprintBytes(testDescriptor(t, testKeyID)),
	}); err != nil {
		t.Fatalf("stale release cleared newer generation: %v", err)
	}
}

func TestActivePairRejectsCollisionAndMismatchedCoreContext(t *testing.T) {
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
	if _, err := slot.RegisterPair(PairRegistration{
		SessionID: "other", KeyID: testKeyID,
		PrimaryPartyID: primaryPartyID, RecoveryPartyID: recoveryPartyID,
		DescriptorBytes: descriptor,
	}); !errors.Is(err, ErrPairAlreadyRegistered) {
		t.Fatalf("colliding RegisterPair() error = %v", err)
	}

	valid := coretss.SaveShareInput{
		SessionID: testSessionID, KeyID: testKeyID, LocalPartyID: primaryPartyID,
		OpaqueDescriptorFingerprint: fingerprintBytes(descriptor),
	}
	tests := []struct {
		name string
		edit func(*coretss.SaveShareInput)
	}{
		{name: "session", edit: func(in *coretss.SaveShareInput) { in.SessionID = "other" }},
		{name: "key", edit: func(in *coretss.SaveShareInput) { in.KeyID = "other" }},
		{name: "party", edit: func(in *coretss.SaveShareInput) { in.LocalPartyID = "mpc-signer" }},
		{name: "fingerprint", edit: func(in *coretss.SaveShareInput) { in.OpaqueDescriptorFingerprint[0] ^= 0xff }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			input := valid
			input.OpaqueDescriptorFingerprint = append([]byte(nil), valid.OpaqueDescriptorFingerprint...)
			tt.edit(&input)
			if _, err := slot.resolve(input); !errors.Is(err, ErrPersistenceContextMismatch) {
				t.Fatalf("Resolve() error = %v", err)
			}
		})
	}
}

func fingerprintBytes(descriptor []byte) []byte {
	fingerprint := mpc2of3.DescriptorFingerprintFor(descriptor)
	return append([]byte(nil), fingerprint[:]...)
}
