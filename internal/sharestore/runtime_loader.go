package sharestore

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"fmt"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func inspectArtifactBytes(config StoreConfig, expected ExpectedArtifactContext, finalBytes []byte) (*coretss.StoredShare, ArtifactEvidence, error) {
	return loadValidatedRuntimeShare(config, &expected, finalBytes)
}

func loadValidatedRuntimeShare(config StoreConfig, expected *ExpectedArtifactContext, finalBytes []byte) (*coretss.StoredShare, ArtifactEvidence, error) {
	if len(finalBytes) == 0 || len(finalBytes) > maxArtifactEnvelopeBytes {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: artifact envelope size", coretss.ErrInvalidSharePayload)
	}
	var envelope artifactEnvelopeV1
	if err := decodeClosedJSON(finalBytes, &envelope); err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: decode artifact envelope: %v", coretss.ErrInvalidSharePayload, err)
	}
	if envelope.Version != artifactVersion ||
		envelope.Encryption.Algorithm != artifactEncryption ||
		envelope.Encryption.KeyRef != config.KeyRef() {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: artifact encryption contract", ErrArtifactBinding)
	}
	nonce, err := strictBase64("nonce", envelope.Encryption.Nonce, artifactNonceBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, err
	}
	defer clear(nonce)
	if len(nonce) != artifactNonceBytes {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: nonce length", coretss.ErrInvalidSharePayload)
	}
	ciphertext, err := strictBase64("ciphertext", envelope.Encryption.Ciphertext, maxArtifactCipherBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, err
	}
	defer clear(ciphertext)
	tag, err := strictBase64("tag", envelope.Encryption.Tag, artifactTagBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, err
	}
	defer clear(tag)
	if len(tag) != artifactTagBytes || len(ciphertext)+len(tag) > maxArtifactCipherBytes {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: ciphertext or tag length", coretss.ErrInvalidSharePayload)
	}

	block, err := aes.NewCipher(config.keyProvider.key[:])
	if err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("initialize artifact cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("initialize artifact GCM: %w", err)
	}
	sealed := make([]byte, 0, len(ciphertext)+len(tag))
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	defer clear(sealed)
	payloadBytes, err := gcm.Open(nil, nonce, sealed, nil)
	if err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: decrypt artifact", coretss.ErrInvalidSharePayload)
	}
	defer clear(payloadBytes)
	if len(payloadBytes) == 0 || len(payloadBytes) > maxArtifactPayloadBytes {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: artifact payload size", coretss.ErrInvalidSharePayload)
	}
	var payload artifactPayloadV1
	if err := decodeClosedJSON(payloadBytes, &payload); err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: decode artifact payload: %v", coretss.ErrInvalidSharePayload, err)
	}
	if payload.ArtifactPayloadVersion != artifactPayloadVersion || payload.SessionID == "" || payload.PartyID != config.PartyID() {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: artifact payload contract", ErrArtifactBinding)
	}
	descriptorBytes, err := strictBase64("descriptorBytesBase64", payload.DescriptorBytesBase64, maxArtifactPayloadBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, err
	}
	defer clear(descriptorBytes)
	descriptor, descriptorFingerprint, err := mpc2of3.ParseCanonicalDescriptor(descriptorBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: descriptor: %v", coretss.ErrInvalidSharePayload, err)
	}
	if descriptor.KeyID == "" || !canonicalKeyIDPattern.MatchString(descriptor.KeyID) {
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: descriptor key ID", ErrArtifactBinding)
	}
	if expected != nil {
		if expected.SessionID == "" || expected.KeyID != descriptor.KeyID || payload.SessionID != expected.SessionID ||
			!bytes.Equal(descriptorBytes, expected.DescriptorBytes) {
			return nil, ArtifactEvidence{}, fmt.Errorf("%w: expected artifact context", ErrArtifactBinding)
		}
		if _, expectedFingerprint, parseErr := mpc2of3.ParseCanonicalDescriptor(expected.DescriptorBytes); parseErr != nil || expectedFingerprint != descriptorFingerprint {
			return nil, ArtifactEvidence{}, fmt.Errorf("%w: expected descriptor", ErrArtifactBinding)
		}
	}

	shareBlob, err := strictBase64("shareBlob", payload.ShareBlob, maxArtifactPayloadBytes)
	if err != nil {
		return nil, ArtifactEvidence{}, err
	}
	inspected, err := coretss.InspectEncodedECDSAKeyMaterial(shareBlob)
	if err != nil {
		clear(shareBlob)
		return nil, ArtifactEvidence{}, err
	}
	descriptorChainCodeHash, err := mpc2of3.ParseChainCodeHash(descriptor.ChainCodeHash)
	if err != nil || descriptorChainCodeHash != mpc2of3.ChainCodeHash(inspected.ChainCodeHash) {
		clear(shareBlob)
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: chain code hash", ErrArtifactBinding)
	}
	if descriptor.Algorithm != "ECDSA" || descriptor.Curve != "secp256k1" ||
		descriptor.PublicKeyFormat != "compressed_sec1" || descriptor.DerivationScheme != "bip32_secp256k1" {
		clear(shareBlob)
		return nil, ArtifactEvidence{}, fmt.Errorf("%w: descriptor product contract", ErrArtifactBinding)
	}

	stored := &coretss.StoredShare{
		Blob: shareBlob,
		Meta: coretss.ShareMeta{
			Algorithm:        descriptor.Algorithm,
			Curve:            descriptor.Curve,
			Version:          inspected.CodecVersion,
			ChainCodePresent: true,
			PublicKeyFormat:  descriptor.PublicKeyFormat,
			DerivationScheme: descriptor.DerivationScheme,
		},
	}
	return stored, ArtifactEvidence{
		SessionID:             payload.SessionID,
		KeyID:                 descriptor.KeyID,
		PartyID:               payload.PartyID,
		Purpose:               config.Purpose(),
		DescriptorFingerprint: descriptorFingerprint,
		AccountPublicKey:      append([]byte(nil), inspected.AccountPublicKey...),
		ChainCodeHash:         mpc2of3.ChainCodeHash(inspected.ChainCodeHash),
		CodecVersion:          inspected.CodecVersion,
		ArtifactFingerprint:   mpc2of3.ArtifactFingerprintFor(finalBytes),
	}, nil
}
