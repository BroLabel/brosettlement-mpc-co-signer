package sharestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"github.com/btcsuite/btcd/btcec/v2"
)

// The tagged recovery test binary also runs outside the repository/package
// directory. Embed only public frozen fixtures, never local custody paths.
//
//go:embed testdata/legacy-v1/manifest.json testdata/legacy-v1/core-manifest.json testdata/legacy-v1/descriptor.json testdata/legacy-v1/primary/*.json testdata/legacy-v1/recovery/*.json
var legacyArtifactFixtures embed.FS

type legacyArtifactManifest struct {
	Warning, CoSignerRevision, CoreVersion, TSSVersion             string
	KeyID, SessionID, KeyRef, EncodedKey                           string
	DescriptorSHA256, CoreManifestSHA256, RootPublicKey, ChainCode string
	Children                                                       []struct{ Path, PublicKey string }
	Artifacts                                                      []struct{ PartyID, Purpose, File, SHA256, CiphertextSHA256, BlobSHA256, Fingerprint string }
}

func legacySHA256(b []byte) string { hash := sha256.Sum256(b); return hex.EncodeToString(hash[:]) }

func readLegacyFixture(t *testing.T) (legacyArtifactManifest, []byte, [][]byte) {
	t.Helper()
	root := filepath.Join("testdata", "legacy-v1")
	read := func(name string) []byte {
		t.Helper()
		b, err := legacyArtifactFixtures.ReadFile(filepath.ToSlash(filepath.Join(root, name)))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	var manifest legacyArtifactManifest
	if err := json.Unmarshal(read("manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.Warning != "TEST ONLY — NEVER FUND" || manifest.CoSignerRevision != "cf48fe4a51cc1adcff0c3994d124a0d59f7ae12c" || manifest.CoreVersion != "v0.5.0" || manifest.TSSVersion != "v1.5.0" {
		t.Fatal("legacy producer identity mismatch")
	}
	descriptor := read("descriptor.json")
	if legacySHA256(descriptor) != manifest.DescriptorSHA256 || legacySHA256(read("core-manifest.json")) != manifest.CoreManifestSHA256 {
		t.Fatal("legacy descriptor/source manifest changed")
	}
	if len(manifest.Artifacts) != 2 {
		t.Fatal("legacy B/C fixture pair missing")
	}
	artifacts := make([][]byte, 2)
	for i, item := range manifest.Artifacts {
		artifacts[i] = read(item.File)
		if legacySHA256(artifacts[i]) != item.SHA256 {
			t.Fatal("frozen legacy artifact hash mismatch")
		}
		var envelope artifactEnvelopeV1
		if err := json.Unmarshal(artifacts[i], &envelope); err != nil {
			t.Fatal(err)
		}
		ciphertext, err := base64.StdEncoding.DecodeString(envelope.Encryption.Ciphertext)
		if err != nil || legacySHA256(ciphertext) != item.CiphertextSHA256 || envelope.Encryption.KeyRef != manifest.KeyRef {
			t.Fatal("frozen ciphertext/key reference changed")
		}
	}
	return manifest, descriptor, artifacts
}

func legacyProvider(t *testing.T, encodedKey, keyRef string) *KeyProvider {
	t.Helper()
	provider, err := NewKeyProvider(encodedKey, keyRef)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func legacyStore(t *testing.T, purpose StorePurpose, provider *KeyProvider, artifact []byte, keyID string) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	cfg, err := NewStoreConfig(purpose, dir, provider)
	if err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(cfg)
	if err != nil && !errors.Is(err, ErrUnsupportedPublishPlatform) {
		t.Fatal(err)
	}
	path, err := store.finalPath(keyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, artifact, 0600); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestLegacyArtifactsReadWithoutRewrite(t *testing.T) {
	manifest, descriptor, artifacts := readLegacyFixture(t)
	rootBytes, err := hex.DecodeString(manifest.RootPublicKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err := btcec.ParsePubKey(rootBytes)
	if err != nil {
		t.Fatal(err)
	}
	chain, err := hex.DecodeString(manifest.ChainCode)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range manifest.Artifacts {
		t.Run(item.PartyID, func(t *testing.T) {
			store := legacyStore(t, StorePurpose(item.Purpose), legacyProvider(t, manifest.EncodedKey, manifest.KeyRef), artifacts[i], manifest.KeyID)
			writes := 0
			store.publishFile = func(string, []byte) error { writes++; return errors.New("unexpected rewrite") }
			expected := ExpectedArtifactContext{SessionID: manifest.SessionID, KeyID: manifest.KeyID, DescriptorBytes: descriptor}
			evidence, err := store.InspectExisting(context.Background(), expected)
			if err != nil {
				t.Fatal(err)
			}
			stored, _, err := loadValidatedRuntimeShare(store.config, &expected, artifacts[i])
			if err != nil {
				t.Fatal(err)
			}
			defer clear(stored.Blob)
			if item.Purpose == "primary" {
				reader, err := NewPrimaryReader(store)
				if err != nil {
					t.Fatal(err)
				}
				loaded, err := reader.LoadShare(context.Background(), manifest.KeyID)
				if err != nil {
					t.Fatal(err)
				}
				defer clear(loaded.Blob)
				if !bytes.Equal(loaded.Blob, stored.Blob) {
					t.Fatal("runtime primary reader changed K01 share")
				}
			}
			if legacySHA256(stored.Blob) != item.BlobSHA256 || evidence.ArtifactFingerprint.String() != item.Fingerprint || evidence.PartyID != item.PartyID || evidence.CodecVersion != 2 || !bytes.Equal(evidence.AccountPublicKey, root.SerializeCompressed()) || evidence.ChainCodeHash != mpc2of3.ChainCodeHashFor(chain) {
				t.Fatal("legacy share/public identity changed")
			}
			material, err := coretss.UnmarshalKeyMaterial(stored.Blob)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(material.ChainCode, chain) {
				t.Fatal("legacy chain code changed")
			}
			for _, child := range manifest.Children {
				got, err := coretss.DeriveECDSAChildPublicKey(manifest.RootPublicKey, material.ChainCode, coretss.DerivationContext{ProfileID: "legacy-test", Chain: "ethereum", Algorithm: coretss.AlgorithmECDSA, Curve: coretss.CurveSecp256k1, Scheme: coretss.DerivationSchemeBIP32Secp256k1, PublicKeyFormat: coretss.PublicKeyFormatUncompressedHex, AccountPath: "m/44'/60'/0'", ChildPath: child.Path})
				if err != nil || got != child.PublicKey {
					t.Fatal("K01 derived identity changed")
				}
			}
			path, _ := store.finalPath(manifest.KeyID)
			after, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if writes != 0 || legacySHA256(after) != item.SHA256 {
				t.Fatal("legacy runtime loading rewrote artifact")
			}
		})
	}
}

func TestLegacyArtifactsRejectInvalidBindingsAndCorruption(t *testing.T) {
	manifest, descriptor, artifacts := readLegacyFixture(t)
	expected := ExpectedArtifactContext{SessionID: manifest.SessionID, KeyID: manifest.KeyID, DescriptorBytes: descriptor}
	corrupt := append([]byte(nil), artifacts[0]...)
	corrupt[len(corrupt)/2] ^= 1
	for _, tc := range []struct {
		name        string
		purpose     StorePurpose
		key, keyRef string
		artifact    []byte
		expected    ExpectedArtifactContext
		want        error
	}{
		{"wrong encryption key", StorePurposePrimary, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, 32)), manifest.KeyRef, artifacts[0], expected, coretss.ErrInvalidSharePayload},
		{"wrong key reference", StorePurposePrimary, manifest.EncodedKey, "wrong-key-ref", artifacts[0], expected, ErrArtifactBinding},
		{"wrong party", StorePurposePrimary, manifest.EncodedKey, manifest.KeyRef, artifacts[1], expected, ErrArtifactBinding},
		{"wrong key ID", StorePurposePrimary, manifest.EncodedKey, manifest.KeyRef, artifacts[0], ExpectedArtifactContext{SessionID: manifest.SessionID, KeyID: "mpc_key_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", DescriptorBytes: descriptor}, ErrArtifactBinding},
		{"wrong session", StorePurposePrimary, manifest.EncodedKey, manifest.KeyRef, artifacts[0], ExpectedArtifactContext{SessionID: "wrong", KeyID: manifest.KeyID, DescriptorBytes: descriptor}, ErrArtifactBinding},
		{"corrupt", StorePurposePrimary, manifest.EncodedKey, manifest.KeyRef, corrupt, expected, coretss.ErrInvalidSharePayload},
		{"truncated", StorePurposePrimary, manifest.EncodedKey, manifest.KeyRef, artifacts[0][:len(artifacts[0])/2], expected, coretss.ErrInvalidSharePayload},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := legacyStore(t, tc.purpose, legacyProvider(t, tc.key, tc.keyRef), tc.artifact, manifest.KeyID)
			stored, _, err := loadValidatedRuntimeShare(store.config, &tc.expected, tc.artifact)
			if stored != nil || !errors.Is(err, tc.want) {
				t.Fatalf("got share=%t, err=%v; want %v", stored != nil, err, tc.want)
			}
		})
	}
}

func TestLegacyArtifactsDuplicateCreateAndConflictingPair(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("immutable publication requires Linux")
	}
	manifest, descriptor, artifacts := readLegacyFixture(t)
	provider := legacyProvider(t, manifest.EncodedKey, manifest.KeyRef)
	primary := legacyStore(t, StorePurposePrimary, provider, artifacts[0], manifest.KeyID)
	recovery := legacyStore(t, StorePurposeRecovery, provider, artifacts[1], manifest.KeyID)
	pair := NewActivePair()
	registration := PairRegistration{SessionID: manifest.SessionID, KeyID: manifest.KeyID, PrimaryPartyID: primaryPartyID, RecoveryPartyID: recoveryPartyID, DescriptorBytes: descriptor}
	lease, err := pair.RegisterPair(registration)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	registration.SessionID = "conflicting-session"
	if _, err := pair.RegisterPair(registration); !errors.Is(err, ErrPairAlreadyRegistered) {
		t.Fatalf("conflicting pair: %v", err)
	}
	writer, err := NewRoutingWriter(pair, primary, recovery)
	if err != nil {
		t.Fatal(err)
	}
	fp := [32]byte(mpc2of3.DescriptorFingerprintFor(descriptor))
	for i, store := range []*Store{primary, recovery} {
		stored, _, err := loadValidatedRuntimeShare(store.config, nil, artifacts[i])
		if err != nil {
			t.Fatal(err)
		}
		err = writer.SaveShare(context.Background(), coretss.SaveShareInput{SessionID: manifest.SessionID, KeyID: manifest.KeyID, LocalPartyID: store.config.PartyID(), OpaqueDescriptorFingerprint: fp[:], CodecBlob: stored.Blob})
		clear(stored.Blob)
		if !errors.Is(err, ErrArtifactExists) {
			t.Fatalf("duplicate publish: %v", err)
		}
		path, _ := store.finalPath(manifest.KeyID)
		after, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if legacySHA256(after) != manifest.Artifacts[i].SHA256 {
			t.Fatal("duplicate create changed legacy ciphertext")
		}
	}
}
