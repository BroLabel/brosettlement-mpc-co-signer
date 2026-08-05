package sharestore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	tsscrypto "github.com/bnb-chain/tss-lib/crypto"
	ecdsakeygen "github.com/bnb-chain/tss-lib/ecdsa/keygen"
	tsslib "github.com/bnb-chain/tss-lib/tss"
)

const (
	testKeyID     = "mpc_key_123e4567-e89b-42d3-a456-426614174000"
	testSessionID = "dkg-session-123"
	testKeyRef    = "customer-co-signer-key-v1"
)

func TestArtifactV1GoldenEnvelopeAndEvidence(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	descriptor := testDescriptor(t, testKeyID)
	blob := testCodecBlob(t)
	nonce := bytes.Repeat([]byte{0x11}, artifactNonceBytes)

	finalBytes, err := encodeArtifactV1(store.config, PublishInput{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PartyID:         primaryPartyID,
		DescriptorBytes: descriptor,
		CodecBlob:       blob,
	}, nonce)
	if err != nil {
		t.Fatalf("encodeArtifactV1() error = %v", err)
	}
	golden, err := os.ReadFile(filepath.Join("..", "..", "testdata", "artifact-v1", "golden.primary.json"))
	if err != nil {
		t.Fatalf("read artifact golden vector: %v", err)
	}
	golden = bytes.TrimSuffix(golden, []byte{'\n'})
	if !bytes.Equal(finalBytes, golden) {
		t.Fatal("artifact-v1 final bytes differ from the co-signer-owned golden vector")
	}

	var envelope map[string]any
	if err := json.Unmarshal(finalBytes, &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	if len(envelope) != 2 || envelope["version"] != float64(1) {
		t.Fatalf("unexpected envelope fields: %#v", envelope)
	}
	encryption, ok := envelope["encryption"].(map[string]any)
	if !ok || len(encryption) != 5 {
		t.Fatalf("unexpected encryption fields: %#v", envelope["encryption"])
	}
	if encryption["algorithm"] != "AES-256-GCM" || encryption["keyRef"] != testKeyRef {
		t.Fatalf("unexpected encryption binding: %#v", encryption)
	}
	if encryption["nonce"] != "ERERERERERERERER" {
		t.Fatalf("nonce = %q", encryption["nonce"])
	}

	loaded, evidence, err := inspectArtifactBytes(store.config, ExpectedArtifactContext{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		DescriptorBytes: descriptor,
	}, finalBytes)
	if err != nil {
		t.Fatalf("inspectArtifactBytes() error = %v", err)
	}
	defer clear(loaded.Blob)
	if !bytes.Equal(loaded.Blob, blob) {
		t.Fatal("loaded codec blob differs")
	}
	if evidence.SessionID != testSessionID ||
		evidence.KeyID != testKeyID ||
		evidence.PartyID != primaryPartyID ||
		evidence.Purpose != StorePurposePrimary ||
		evidence.DescriptorFingerprint != mpc2of3.DescriptorFingerprintFor(descriptor) ||
		evidence.CodecVersion != 2 ||
		evidence.ArtifactFingerprint != mpc2of3.ArtifactFingerprintFor(finalBytes) {
		t.Fatalf("unexpected evidence: %#v", evidence)
	}
	wantChainCodeHash := mpc2of3.ChainCodeHashFor(bytes.Repeat([]byte{0x42}, 32))
	if evidence.ChainCodeHash != wantChainCodeHash {
		t.Fatalf("chain code hash = %q, want %q", evidence.ChainCodeHash, wantChainCodeHash)
	}
	wantPublicKey := []byte{
		0x02, 0x79, 0xbe, 0x66, 0x7e, 0xf9, 0xdc, 0xbb, 0xac, 0x55, 0xa0,
		0x62, 0x95, 0xce, 0x87, 0x0b, 0x07, 0x02, 0x9b, 0xfc, 0xdb, 0x2d,
		0xce, 0x28, 0xd9, 0x59, 0xf2, 0x81, 0x5b, 0x16, 0xf8, 0x17, 0x98,
	}
	if !bytes.Equal(evidence.AccountPublicKey, wantPublicKey) {
		t.Fatalf("public key = %x", evidence.AccountPublicKey)
	}
}

func TestArtifactV1RejectsCorruptionAndBindingMismatch(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	descriptor := testDescriptor(t, testKeyID)
	finalBytes, err := encodeArtifactV1(store.config, PublishInput{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PartyID:         primaryPartyID,
		DescriptorBytes: descriptor,
		CodecBlob:       testCodecBlob(t),
	}, bytes.Repeat([]byte{0x22}, artifactNonceBytes))
	if err != nil {
		t.Fatalf("encodeArtifactV1() error = %v", err)
	}

	tests := []struct {
		name     string
		config   StoreConfig
		expected ExpectedArtifactContext
		artifact []byte
		wantErr  error
	}{
		{
			name:     "ciphertext corruption",
			config:   store.config,
			expected: ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor},
			artifact: mutateJSONBinaryField(t, finalBytes, "ciphertext"),
			wantErr:  coretss.ErrInvalidSharePayload,
		},
		{
			name:     "wrong session",
			config:   store.config,
			expected: ExpectedArtifactContext{SessionID: "other-session", KeyID: testKeyID, DescriptorBytes: descriptor},
			artifact: finalBytes,
			wantErr:  ErrArtifactBinding,
		},
		{
			name:     "wrong descriptor bytes",
			config:   store.config,
			expected: ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: append(append([]byte(nil), descriptor...), ' ')},
			artifact: finalBytes,
			wantErr:  ErrArtifactBinding,
		},
		{
			name:     "recovery purpose mismatch",
			config:   testStore(t, StorePurposeRecovery).config,
			expected: ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor},
			artifact: finalBytes,
			wantErr:  ErrArtifactBinding,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded, _, err := inspectArtifactBytes(tt.config, tt.expected, tt.artifact)
			if loaded != nil {
				clear(loaded.Blob)
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("inspectArtifactBytes() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestArtifactV1StrictClosedJSONAndPaddedBase64(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	descriptor := testDescriptor(t, testKeyID)
	valid, err := encodeArtifactV1(store.config, PublishInput{
		SessionID:       testSessionID,
		KeyID:           testKeyID,
		PartyID:         primaryPartyID,
		DescriptorBytes: descriptor,
		CodecBlob:       testCodecBlob(t),
	}, bytes.Repeat([]byte{0x33}, artifactNonceBytes))
	if err != nil {
		t.Fatalf("encodeArtifactV1() error = %v", err)
	}
	expected := ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor}

	var envelope artifactEnvelopeV1
	if err := json.Unmarshal(valid, &envelope); err != nil {
		t.Fatal(err)
	}
	unpaddedTag := strings.TrimRight(envelope.Encryption.Tag, "=")

	tests := []struct {
		name string
		raw  []byte
	}{
		{name: "unknown outer key", raw: append(append([]byte(nil), valid[:len(valid)-1]...), []byte(`,"unknown":true}`)...)},
		{name: "duplicate version", raw: append([]byte(`{"version":1,`), valid[1:]...)},
		{name: "unpadded binary", raw: bytes.Replace(valid, []byte(envelope.Encryption.Tag), []byte(unpaddedTag), 1)},
		{name: "trailing whitespace", raw: append(append([]byte(nil), valid...), '\n')},
		{name: "oversized envelope", raw: bytes.Repeat([]byte{'x'}, maxArtifactEnvelopeBytes+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loaded, _, err := inspectArtifactBytes(store.config, expected, tt.raw)
			if loaded != nil {
				clear(loaded.Blob)
			}
			if err == nil {
				t.Fatal("inspectArtifactBytes() error = nil")
			}
		})
	}
}

func TestStoreFinalPathRequiresCanonicalKeyIDAndStaysInDestination(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	if got, err := store.finalPath(testKeyID); err != nil {
		t.Fatalf("finalPath(valid) error = %v", err)
	} else if got != filepath.Join(store.config.Directory(), testKeyID+".primary.json") {
		t.Fatalf("finalPath(valid) = %q", got)
	}
	for _, invalid := range []string{"", "../escape", "key", strings.ToUpper(testKeyID), testKeyID + "/x"} {
		if path, err := store.finalPath(invalid); err == nil {
			t.Fatalf("finalPath(%q) = %q, want error", invalid, path)
		}
	}
}

func TestStoreRejectsUnsafeExistingArtifactDirectoryWithoutChangingIt(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o750); err != nil {
		t.Fatal(err)
	}
	config, err := NewStoreConfig("deployment-1", StorePurposePrimary, primaryPartyID, directory, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(config); err == nil {
		t.Fatal("newStore() accepted group-readable artifact directory")
	}
	info, err := os.Stat(directory)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o750 {
		t.Fatalf("unsafe directory mode changed to %o", info.Mode().Perm())
	}
}

func TestStoreRejectsMissingArtifactDirectoryWithoutCreatingIt(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(t.TempDir(), "primary")
	config, err := NewStoreConfig("deployment-1", StorePurposePrimary, primaryPartyID, directory, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newStore(config); err == nil {
		t.Fatal("newStore() created a missing deployment-owned artifact directory")
	}
	if _, err := os.Lstat(directory); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing artifact directory changed, Lstat() error = %v", err)
	}
}

func TestStoreRejectsSymlinkAndNonDirectoryArtifactDestination(t *testing.T) {
	provider, err := NewKeyProvider(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)), testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(root, "regular")
	if err := os.WriteFile(regular, []byte("not-a-directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, destination := range []string{symlink, regular} {
		config, err := NewStoreConfig("deployment-1", StorePurposePrimary, primaryPartyID, destination, provider)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newStore(config); err == nil {
			t.Fatalf("newStore(%q) error = nil", destination)
		}
	}
}

func TestInspectExistingRejectsSymlinkAndNonregular(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	descriptor := testDescriptor(t, testKeyID)
	expected := ExpectedArtifactContext{SessionID: testSessionID, KeyID: testKeyID, DescriptorBytes: descriptor}
	finalPath, err := store.finalPath(testKeyID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(finalPath+".target", []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(finalPath+".target", finalPath); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectExisting(context.Background(), expected); err == nil {
		t.Fatal("InspectExisting(symlink) error = nil")
	}
	if err := os.Remove(finalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(finalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InspectExisting(context.Background(), expected); err == nil {
		t.Fatal("InspectExisting(directory) error = nil")
	}
}

func TestPrimaryReaderIsTheOnlyProductionShareReader(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	reader, err := NewPrimaryReader(store)
	if err != nil {
		t.Fatalf("NewPrimaryReader() error = %v", err)
	}
	var _ coretss.ShareReader = reader
	if _, ok := any(store).(coretss.ShareReader); ok {
		t.Fatal("artifact store unexpectedly exposes ShareReader")
	}
	recovery := testStore(t, StorePurposeRecovery)
	if _, err := NewPrimaryReader(recovery); err == nil {
		t.Fatal("NewPrimaryReader(recovery) error = nil")
	}
}

type fixtureT interface {
	Helper()
	Fatal(args ...any)
	Fatalf(format string, args ...any)
	TempDir() string
}

func testStore(t fixtureT, purpose StorePurpose) *Store {
	t.Helper()
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x01}, 32))
	provider, err := NewKeyProvider(key, testKeyRef)
	if err != nil {
		t.Fatal(err)
	}
	return testStoreWithProvider(t, purpose, provider)
}

func testStoreWithProvider(t fixtureT, purpose StorePurpose, provider *KeyProvider) *Store {
	t.Helper()
	partyID := primaryPartyID
	if purpose == StorePurposeRecovery {
		partyID = recoveryPartyID
	}
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	config, err := NewStoreConfig("deployment-1", purpose, partyID, directory, provider)
	if err != nil {
		t.Fatal(err)
	}
	store, err := newStore(config)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func testDescriptor(t fixtureT, keyID string) []byte {
	t.Helper()
	chainCodeHash := mpc2of3.ChainCodeHashFor(bytes.Repeat([]byte{0x42}, 32)).String()
	raw := []byte(`{"algorithm":"ECDSA","chainCodeHash":"` + chainCodeHash + `","curve":"secp256k1","derivationScheme":"bip32_secp256k1","descriptorKind":"mpc-key-descriptor","descriptorVersion":1,"keyId":"` + keyID + `","parties":[{"partyId":"mpc-signer","purpose":"platform"},{"partyId":"co-signer-primary","purpose":"primary"},{"partyId":"co-signer-recovery","purpose":"recovery"}],"protocolVersion":1,"publicKeyFormat":"compressed_sec1","threshold":2}`)
	if _, _, err := mpc2of3.ParseCanonicalDescriptor(raw); err != nil {
		t.Fatalf("test descriptor invalid: %v", err)
	}
	return raw
}

func testCodecBlob(t fixtureT) []byte {
	t.Helper()
	point := tsscrypto.ScalarBaseMult(tsslib.S256(), big.NewInt(1))
	blob, err := coretss.MarshalKeyMaterial(coretss.ECDSAKeyMaterial{
		Share:            ecdsakeygen.LocalPartySaveData{ECDSAPub: point},
		ChainCode:        bytes.Repeat([]byte{0x42}, 32),
		PublicKeyFormat:  "compressed_sec1",
		DerivationScheme: "bip32_secp256k1",
	})
	if err != nil {
		t.Fatalf("MarshalKeyMaterial() error = %v", err)
	}
	return blob
}

func mutateJSONBinaryField(t *testing.T, raw []byte, field string) []byte {
	t.Helper()
	var envelope artifactEnvelopeV1
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	var encoded *string
	switch field {
	case "ciphertext":
		encoded = &envelope.Encryption.Ciphertext
	default:
		t.Fatalf("unsupported field %q", field)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(*encoded)
	if err != nil {
		t.Fatal(err)
	}
	decoded[0] ^= 0xff
	*encoded = base64.StdEncoding.EncodeToString(decoded)
	mutated, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

func TestArtifactFingerprintUsesExactFinalBytes(t *testing.T) {
	raw := []byte(`{"version":1}`)
	want := sha256.Sum256(raw)
	if got := mpc2of3.ArtifactFingerprintFor(raw); got != mpc2of3.ArtifactFingerprint(want) {
		t.Fatalf("artifact fingerprint = %q", got)
	}
}

func TestStoreExistsAddressesOnlyTheCanonicalFinalPath(t *testing.T) {
	store := testStore(t, StorePurposePrimary)
	exists, err := store.Exists(context.Background(), testKeyID)
	if err != nil {
		t.Fatalf("Exists(absent) error = %v", err)
	}
	if exists {
		t.Fatal("Exists(absent) = true")
	}
	finalPath, err := store.finalPath(testKeyID)
	if err != nil {
		t.Fatalf("finalPath() error = %v", err)
	}
	if err := os.WriteFile(finalPath, []byte("addressed"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if exists, err = store.Exists(context.Background(), testKeyID); err != nil || !exists {
		t.Fatalf("Exists(present) = %v, %v, want true, nil", exists, err)
	}
	if _, err := store.Exists(context.Background(), "../other"); err == nil {
		t.Fatal("Exists(noncanonical key) error = nil")
	}
}

func TestOpenStoreRetainsPurposeBoundCapabilityWhenProvisioningProbeFails(t *testing.T) {
	provider := testKeyProvider(t, testKeyRef)
	blockedPath := filepath.Join(t.TempDir(), "recovery")
	if err := os.WriteFile(blockedPath, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	config, err := NewStoreConfig(
		"deployment-1",
		StorePurposeRecovery,
		recoveryPartyID,
		blockedPath,
		provider,
	)
	if err != nil {
		t.Fatalf("NewStoreConfig() error = %v", err)
	}

	store, capabilityErr := OpenStore(config)
	if capabilityErr == nil {
		t.Fatal("OpenStore() capability error = nil")
	}
	if store == nil || store.config.Purpose() != StorePurposeRecovery {
		t.Fatalf("OpenStore() store = %#v, want retained recovery capability", store)
	}
}
