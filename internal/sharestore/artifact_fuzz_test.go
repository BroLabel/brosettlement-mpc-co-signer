package sharestore

import (
	"bytes"
	"testing"
)

func FuzzInspectArtifactV1(f *testing.F) {
	store := testStore(f, StorePurposePrimary)
	descriptor := testDescriptor(f, testKeyID)
	valid, err := encodeArtifactV1(store.config, PublishInput{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PartyID:         primaryPartyID,
		DescriptorBytes: descriptor,
		CodecBlob:       testCodecBlob(f),
	}, bytes.Repeat([]byte{0x44}, artifactNonceBytes))
	if err != nil {
		f.Fatalf("encode seed artifact: %v", err)
	}
	f.Add(valid)
	f.Add([]byte{})
	f.Add([]byte(`{"version":1}`))

	expected := ExpectedArtifactContext{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		DescriptorBytes: descriptor,
	}
	f.Fuzz(func(t *testing.T, raw []byte) {
		if len(raw) > maxArtifactEnvelopeBytes+1 {
			return
		}
		stored, _, _ := loadValidatedRuntimeShare(store.config, &expected, raw)
		if stored != nil {
			clear(stored.Blob)
		}
	})
}
