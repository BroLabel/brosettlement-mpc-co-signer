//go:build mpc_recovery_test

package sharestore

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"github.com/bnb-chain/tss-lib/common"
	tsscrypto "github.com/bnb-chain/tss-lib/crypto"
	ecdsakeygen "github.com/bnb-chain/tss-lib/ecdsa/keygen"
	tsslib "github.com/bnb-chain/tss-lib/tss"
	"github.com/btcsuite/btcd/btcec"
	"golang.org/x/sync/errgroup"
)

const (
	recoveryProofInputEnv     = "MPC_RECOVERY_PROOF_INPUT"
	recoveryProofInputVersion = 1
	recoveryProofKeyID        = "mpc_key_123e4567-e89b-42d3-a456-426614174000"
	recoveryProofOtherKeyID   = "mpc_key_aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	recoveryProofKeyRef       = "recovery-proof-key-v1"
	recoveryProofDeploymentID = "recovery-proof"
	recoveryProofPartyA       = "mpc-signer"
	recoveryProofInputLimit   = 32 << 10
)

type recoveryProofReader struct {
	store    *Store
	expected ExpectedArtifactContext
}

func (r *recoveryProofReader) LoadShare(ctx context.Context, keyID string) (*coretss.StoredShare, error) {
	if r == nil || r.store == nil || keyID != r.expected.KeyID {
		return nil, coretss.ErrShareNotFound
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := r.store.finalPath(keyID)
	if err != nil {
		return nil, err
	}
	finalBytes, err := r.store.readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, coretss.ErrShareNotFound
		}
		return nil, errors.New("read recovery proof artifact")
	}
	defer clear(finalBytes)
	stored, _, err := loadValidatedRuntimeShare(r.store.config, &r.expected, finalBytes)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

var _ coretss.ShareReader = (*recoveryProofReader)(nil)

type recoveryProofInput struct {
	Version           int    `json:"version"`
	DeploymentID      string `json:"deploymentId"`
	KeyRef            string `json:"keyRef"`
	EncodedKey        string `json:"encodedKey"`
	KeyID             string `json:"keyId"`
	SessionID         string `json:"sessionId"`
	DescriptorBase64  string `json:"descriptorBase64"`
	PrimaryDirectory  string `json:"primaryDirectory"`
	RecoveryDirectory string `json:"recoveryDirectory"`
}

type recoveryProofFixture struct {
	encodedKey              string
	descriptor              []byte
	sessionID               string
	primaryArtifact         []byte
	recoveryArtifact        []byte
	wrongDescriptorArtifact []byte
	wrongPublicArtifact     []byte
	secretCanaries          []string
}

type recoveryProofCase struct {
	name             string
	wantSuccess      bool
	omitPrimary      bool
	omitRecovery     bool
	corruptPrimary   bool
	corruptRecovery  bool
	wrongKey         bool
	wrongKeyRef      bool
	recoveryArtifact []byte
}

func TestIsolatedRecoveryProof(t *testing.T) {
	if inputPath := os.Getenv(recoveryProofInputEnv); inputPath != "" {
		if err := runIsolatedRecoveryProof(inputPath); err != nil {
			t.Fatal("isolated recovery proof failed")
		}
		return
	}

	fixture := generateRecoveryProofFixture(t)
	t.Cleanup(func() {
		clear(fixture.descriptor)
		clear(fixture.primaryArtifact)
		clear(fixture.recoveryArtifact)
		clear(fixture.wrongDescriptorArtifact)
		clear(fixture.wrongPublicArtifact)
	})

	cases := []recoveryProofCase{
		{name: "real B+C signing", wantSuccess: true},
		{name: "wrong encryption key", wrongKey: true},
		{name: "wrong key reference", wrongKeyRef: true},
		{name: "missing primary artifact", omitPrimary: true},
		{name: "corrupt primary artifact", corruptPrimary: true},
		{name: "missing recovery artifact", omitRecovery: true},
		{name: "corrupt recovery artifact", corruptRecovery: true},
		{name: "descriptor mismatch", recoveryArtifact: fixture.wrongDescriptorArtifact},
		{name: "public output mismatch", recoveryArtifact: fixture.wrongPublicArtifact},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			inputPath := prepareIsolatedRecoveryProof(t, fixture, testCase)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
			defer cancel()
			command := exec.CommandContext(ctx, os.Args[0], "-test.run", "^TestIsolatedRecoveryProof$")
			command.Env = []string{recoveryProofInputEnv + "=" + inputPath}
			command.Dir = filepath.Dir(inputPath)
			output, err := command.CombinedOutput()
			assertRecoveryProofOutputRedacted(t, output, fixture.secretCanaries)
			if ctx.Err() != nil {
				t.Fatal("isolated recovery proof exceeded its bounded deadline")
			}
			if testCase.wantSuccess && err != nil {
				t.Fatal("isolated recovery proof rejected valid copied artifacts")
			}
			if !testCase.wantSuccess && err == nil {
				t.Fatal("isolated recovery proof accepted invalid copied artifacts")
			}
		})
	}
}

func generateRecoveryProofFixture(t *testing.T) recoveryProofFixture {
	t.Helper()
	key := randomRecoveryProofBytes(t, encryptionKeyBytes)
	chainCode := randomRecoveryProofBytes(t, 32)
	defer clear(key)
	defer clear(chainCode)
	encodedKey := base64.StdEncoding.EncodeToString(key)
	chainCodeHex := hex.EncodeToString(chainCode)
	descriptor := recoveryProofDescriptor(t, recoveryProofKeyID, chainCode)
	otherDescriptor := recoveryProofDescriptor(t, recoveryProofOtherKeyID, chainCode)

	writer := &recoveryProofShareWriter{shares: make(map[string][]byte)}
	preParams := generateRecoveryProofPreParams(t, 3)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	serviceA := coretss.NewBnbService(logger,
		coretss.WithPreParamsSource(newRecoveryProofPreParamsSource(preParams[0])),
		coretss.WithShareWriter(writer),
	)
	serviceBC := coretss.NewBnbService(logger,
		coretss.WithPreParamsSource(newRecoveryProofPreParamsSource(preParams[1], preParams[2])),
		coretss.WithShareWriter(writer),
	)
	parties := []string{recoveryProofPartyA, primaryPartyID, recoveryPartyID}
	services := map[string]*coretss.Service{
		recoveryProofPartyA: serviceA,
		primaryPartyID:      serviceBC,
		recoveryPartyID:     serviceBC,
	}
	_, transports := newRecoveryProofFrameBus(parties)
	sessionID := "recovery-proof-dkg-" + hex.EncodeToString(randomRecoveryProofBytes(t, 8))
	_, descriptorFingerprint, err := mpc2of3.ParseCanonicalDescriptor(descriptor)
	if err != nil {
		t.Fatal("recovery proof descriptor setup failed")
	}
	fingerprintBytes := [32]byte(descriptorFingerprint)

	dkgCtx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	group, groupCtx := errgroup.WithContext(dkgCtx)
	for _, partyID := range parties {
		partyID := partyID
		group.Go(func() error {
			_, runErr := services[partyID].RunDKGSession(groupCtx, coretss.DKGSessionRequest{
				Session: coretss.DKGSessionDescriptor{
					SessionID: sessionID,
					OrgID:     "recovery-proof",
					KeyID:     recoveryProofKeyID,
					Parties:   parties,
					Threshold: 2,
					Algorithm: coretss.AlgorithmECDSA,
					Curve:     coretss.CurveSecp256k1,
				},
				LocalPartyID:                partyID,
				OpaqueDescriptorFingerprint: fingerprintBytes[:],
				DerivationMaterial: &coretss.DKGDerivationMaterial{
					ChainCode:        chainCodeHex,
					DerivationScheme: coretss.DerivationSchemeBIP32Secp256k1,
				},
				Transport: transports[partyID],
			})
			if runErr != nil {
				return errors.New("recovery proof DKG party failed")
			}
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal("recovery proof DKG failed")
	}

	primaryBlob := writer.share(primaryPartyID)
	recoveryBlob := writer.share(recoveryPartyID)
	defer clear(primaryBlob)
	defer clear(recoveryBlob)
	if len(primaryBlob) == 0 || len(recoveryBlob) == 0 || bytes.Equal(primaryBlob, recoveryBlob) {
		t.Fatal("recovery proof DKG did not produce distinct B and C material")
	}
	primaryEvidence, err := coretss.InspectEncodedECDSAKeyMaterial(primaryBlob)
	if err != nil {
		t.Fatal("recovery proof B inspection failed")
	}
	recoveryEvidence, err := coretss.InspectEncodedECDSAKeyMaterial(recoveryBlob)
	if err != nil || !bytes.Equal(primaryEvidence.AccountPublicKey, recoveryEvidence.AccountPublicKey) ||
		primaryEvidence.ChainCodeHash != recoveryEvidence.ChainCodeHash {
		t.Fatal("recovery proof DKG public evidence mismatch")
	}

	provider, err := NewKeyProvider(encodedKey, recoveryProofKeyRef)
	if err != nil {
		t.Fatal("recovery proof key setup failed")
	}
	primaryConfig := recoveryProofStoreConfig(t, StorePurposePrimary, t.TempDir(), provider)
	recoveryConfig := recoveryProofStoreConfig(t, StorePurposeRecovery, t.TempDir(), provider)
	primaryArtifact := encodeRecoveryProofArtifact(t, primaryConfig, sessionID, recoveryProofKeyID, descriptor, primaryBlob)
	recoveryArtifact := encodeRecoveryProofArtifact(t, recoveryConfig, sessionID, recoveryProofKeyID, descriptor, recoveryBlob)
	wrongDescriptorArtifact := encodeRecoveryProofArtifact(t, recoveryConfig, sessionID, recoveryProofOtherKeyID, otherDescriptor, recoveryBlob)
	wrongPublicBlob := mismatchedRecoveryProofPublicBlob(t, recoveryBlob)
	defer clear(wrongPublicBlob)
	wrongPublicArtifact := encodeRecoveryProofArtifact(t, recoveryConfig, sessionID, recoveryProofKeyID, descriptor, wrongPublicBlob)
	secretCanaries := []string{
		encodedKey,
		chainCodeHex,
		base64.StdEncoding.EncodeToString(primaryBlob),
		base64.StdEncoding.EncodeToString(recoveryBlob),
	}
	for _, artifact := range [][]byte{primaryArtifact, recoveryArtifact} {
		var envelope artifactEnvelopeV1
		if err := json.Unmarshal(artifact, &envelope); err != nil {
			t.Fatal("inspect recovery proof redaction canaries")
		}
		secretCanaries = append(secretCanaries,
			envelope.Encryption.Nonce,
			envelope.Encryption.Ciphertext,
			envelope.Encryption.Tag,
		)
	}

	return recoveryProofFixture{
		encodedKey:              encodedKey,
		descriptor:              descriptor,
		sessionID:               sessionID,
		primaryArtifact:         primaryArtifact,
		recoveryArtifact:        recoveryArtifact,
		wrongDescriptorArtifact: wrongDescriptorArtifact,
		wrongPublicArtifact:     wrongPublicArtifact,
		secretCanaries:          secretCanaries,
	}
}

func prepareIsolatedRecoveryProof(t *testing.T, fixture recoveryProofFixture, testCase recoveryProofCase) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal("prepare isolated recovery proof directory")
	}
	primaryDirectory := filepath.Join(root, "primary")
	recoveryDirectory := filepath.Join(root, "recovery")
	for _, directory := range []string{primaryDirectory, recoveryDirectory} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal("prepare isolated recovery proof store")
		}
	}

	primaryArtifact := fixture.primaryArtifact
	recoveryArtifact := fixture.recoveryArtifact
	if testCase.recoveryArtifact != nil {
		recoveryArtifact = testCase.recoveryArtifact
	}
	if testCase.corruptPrimary {
		primaryArtifact = corruptRecoveryProofArtifact(primaryArtifact)
	}
	if testCase.corruptRecovery {
		recoveryArtifact = corruptRecoveryProofArtifact(recoveryArtifact)
	}
	if !testCase.omitPrimary {
		writeRecoveryProofFile(t, filepath.Join(primaryDirectory, recoveryProofKeyID+".primary.json"), primaryArtifact)
	}
	if !testCase.omitRecovery {
		writeRecoveryProofFile(t, filepath.Join(recoveryDirectory, recoveryProofKeyID+".recovery.json"), recoveryArtifact)
	}

	encodedKey := fixture.encodedKey
	if testCase.wrongKey {
		encodedKey = base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa5}, encryptionKeyBytes))
	}
	keyRef := recoveryProofKeyRef
	if testCase.wrongKeyRef {
		keyRef = "wrong-recovery-proof-key-ref"
	}
	inputBytes, err := json.Marshal(recoveryProofInput{
		Version:           recoveryProofInputVersion,
		DeploymentID:      recoveryProofDeploymentID,
		KeyRef:            keyRef,
		EncodedKey:        encodedKey,
		KeyID:             recoveryProofKeyID,
		SessionID:         fixture.sessionID,
		DescriptorBase64:  base64.StdEncoding.EncodeToString(fixture.descriptor),
		PrimaryDirectory:  primaryDirectory,
		RecoveryDirectory: recoveryDirectory,
	})
	if err != nil {
		t.Fatal("encode isolated recovery proof input")
	}
	defer clear(inputBytes)
	inputPath := filepath.Join(root, "proof-input.json")
	writeRecoveryProofFile(t, inputPath, inputBytes)
	return inputPath
}

func runIsolatedRecoveryProof(inputPath string) error {
	inputBytes, err := readPrivateRecoveryProofInput(inputPath)
	if err != nil {
		return err
	}
	defer clear(inputBytes)
	var input recoveryProofInput
	decoder := json.NewDecoder(bytes.NewReader(inputBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return errors.New("decode recovery proof input")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode recovery proof input")
	}
	if input.Version != recoveryProofInputVersion || input.KeyID == "" || input.SessionID == "" {
		return errors.New("validate recovery proof input")
	}
	descriptor, err := strictBase64("descriptor", input.DescriptorBase64, recoveryProofInputLimit)
	if err != nil {
		return errors.New("validate recovery proof descriptor")
	}
	defer clear(descriptor)
	provider, err := NewKeyProvider(input.EncodedKey, input.KeyRef)
	if err != nil {
		return errors.New("validate recovery proof key")
	}
	primaryConfig, err := NewStoreConfig(input.DeploymentID, StorePurposePrimary, primaryPartyID, input.PrimaryDirectory, provider)
	if err != nil {
		return errors.New("validate recovery proof primary binding")
	}
	recoveryConfig, err := NewStoreConfig(input.DeploymentID, StorePurposeRecovery, recoveryPartyID, input.RecoveryDirectory, provider)
	if err != nil || ValidateStorePair(primaryConfig, recoveryConfig) != nil {
		return errors.New("validate recovery proof store pair")
	}
	primaryStore, err := configuredStore(primaryConfig)
	if err != nil {
		return errors.New("open recovery proof primary store")
	}
	recoveryStore, err := configuredStore(recoveryConfig)
	if err != nil {
		return errors.New("open recovery proof recovery store")
	}
	expected := ExpectedArtifactContext{SessionID: input.SessionID, KeyID: input.KeyID, DescriptorBytes: descriptor}
	primaryEvidence, err := primaryStore.InspectExisting(context.Background(), expected)
	if err != nil {
		return errors.New("inspect recovery proof primary artifact")
	}
	recoveryEvidence, err := recoveryStore.InspectExisting(context.Background(), expected)
	if err != nil {
		return errors.New("inspect recovery proof recovery artifact")
	}
	if err := compareRecoveryProofEvidence(primaryEvidence, recoveryEvidence); err != nil {
		return err
	}
	primaryReader, err := NewPrimaryReader(primaryStore)
	if err != nil {
		return errors.New("open recovery proof primary reader")
	}
	recoveryReader := &recoveryProofReader{store: recoveryStore, expected: expected}
	primaryShare, err := primaryReader.LoadShare(context.Background(), input.KeyID)
	if err != nil {
		return errors.New("load recovery proof primary share")
	}
	chainCode, err := recoveryProofChainCode(primaryShare)
	clear(primaryShare.Blob)
	if err != nil {
		return errors.New("load recovery proof derivation material")
	}
	defer clear(chainCode)
	recoveryShare, err := recoveryReader.LoadShare(context.Background(), input.KeyID)
	if err != nil {
		return errors.New("load recovery proof recovery share")
	}
	clear(recoveryShare.Blob)
	return proveRecoveryProofSignatures(input.KeyID, primaryEvidence.AccountPublicKey, chainCode, primaryReader, recoveryReader)
}

func proveRecoveryProofSignatures(keyID string, accountPublicKey, chainCode []byte, primaryReader, recoveryReader coretss.ShareReader) error {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	primaryService := coretss.NewBnbService(logger, coretss.WithShareReader(primaryReader))
	recoveryService := coretss.NewBnbService(logger, coretss.WithShareReader(recoveryReader))
	accountPoint, err := btcec.ParsePubKey(accountPublicKey, btcec.S256())
	if err != nil {
		return errors.New("validate recovery proof account public output")
	}
	accountPublicKeyHex := hex.EncodeToString(accountPoint.SerializeUncompressed())
	paths := []string{"/0/0", "/1/7"}
	for _, childPath := range paths {
		derivationContext := coretss.DerivationContext{
			ProfileID:       "recovery-proof",
			Chain:           "ethereum",
			Algorithm:       coretss.AlgorithmECDSA,
			Curve:           coretss.CurveSecp256k1,
			Scheme:          coretss.DerivationSchemeBIP32Secp256k1,
			PublicKeyFormat: coretss.PublicKeyFormatUncompressedHex,
			AccountPath:     "m/44'/60'/0'",
			ChildPath:       childPath,
		}
		derivedPublicKey, err := coretss.DeriveECDSAChildPublicKey(accountPublicKeyHex, chainCode, derivationContext)
		if err != nil {
			return errors.New("derive recovery proof public output")
		}
		derivationContext.DerivedPublicKey = derivedPublicKey
		digest := make([]byte, 32)
		if _, err := io.ReadFull(rand.Reader, digest); err != nil {
			return errors.New("generate recovery proof signing input")
		}
		sessionRandom := make([]byte, 8)
		if _, err := io.ReadFull(rand.Reader, sessionRandom); err != nil {
			clear(digest)
			return errors.New("generate recovery proof signing session")
		}
		sessionID := "recovery-proof-sign-" + hex.EncodeToString(sessionRandom)
		clear(sessionRandom)
		parties := []string{primaryPartyID, recoveryPartyID}
		_, transports := newRecoveryProofFrameBus(parties)
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
		group, groupCtx := errgroup.WithContext(ctx)
		for _, participant := range []struct {
			partyID string
			service *coretss.Service
		}{
			{partyID: primaryPartyID, service: primaryService},
			{partyID: recoveryPartyID, service: recoveryService},
		} {
			participant := participant
			group.Go(func() error {
				if err := participant.service.RunSignSession(groupCtx, coretss.SignSessionRequest{
					Session: coretss.SignSessionDescriptor{
						SessionID: sessionID,
						OrgID:     "recovery-proof",
						KeyID:     keyID,
						Parties:   parties,
						Threshold: 2,
						Algorithm: coretss.AlgorithmECDSA,
						Curve:     coretss.CurveSecp256k1,
						Chain:     "ethereum",
					},
					LocalPartyID:      participant.partyID,
					Digest:            digest,
					DerivationContext: &derivationContext,
					Transport:         transports[participant.partyID],
				}); err != nil {
					return errors.New("recovery proof signing party failed")
				}
				return nil
			})
		}
		err = group.Wait()
		cancel()
		if err != nil {
			clear(digest)
			return err
		}
		signature, err := primaryService.ExportECDSASignature(sessionID)
		if err != nil {
			clear(digest)
			return errors.New("export recovery proof signature")
		}
		if err := verifyRecoveryProofSignature(&signature, derivedPublicKey, digest); err != nil {
			clear(digest)
			return err
		}
		clear(digest)
	}
	return nil
}

func verifyRecoveryProofSignature(signature *common.SignatureData, publicKeyHex string, digest []byte) error {
	publicKeyBytes, err := hex.DecodeString(publicKeyHex)
	if err != nil {
		return errors.New("decode recovery proof public output")
	}
	publicKey, err := btcec.ParsePubKey(publicKeyBytes, btcec.S256())
	if err != nil {
		return errors.New("parse recovery proof public output")
	}
	r := new(big.Int).SetBytes(signature.GetR())
	s := new(big.Int).SetBytes(signature.GetS())
	if r.Sign() <= 0 || s.Sign() <= 0 || !bytes.Equal(signature.GetM(), digest) ||
		!ecdsa.Verify(publicKey.ToECDSA(), digest, r, s) {
		return errors.New("independent recovery proof signature verification failed")
	}
	modified := append([]byte(nil), digest...)
	modified[0] ^= 0x80
	defer clear(modified)
	if ecdsa.Verify(publicKey.ToECDSA(), modified, r, s) {
		return errors.New("recovery proof signature accepted a modified digest")
	}
	return nil
}

func compareRecoveryProofEvidence(primary, recovery ArtifactEvidence) error {
	if primary.SessionID != recovery.SessionID || primary.KeyID != recovery.KeyID ||
		primary.PartyID != primaryPartyID || recovery.PartyID != recoveryPartyID ||
		primary.Purpose != StorePurposePrimary || recovery.Purpose != StorePurposeRecovery ||
		primary.DescriptorFingerprint != recovery.DescriptorFingerprint ||
		primary.ChainCodeHash != recovery.ChainCodeHash || primary.CodecVersion != recovery.CodecVersion ||
		!bytes.Equal(primary.AccountPublicKey, recovery.AccountPublicKey) {
		return errors.New("recovery proof artifact evidence mismatch")
	}
	return nil
}

func recoveryProofChainCode(stored *coretss.StoredShare) ([]byte, error) {
	if stored == nil {
		return nil, coretss.ErrShareNotFound
	}
	material, err := coretss.UnmarshalKeyMaterial(stored.Blob)
	if err != nil {
		return nil, err
	}
	defer clear(material.ChainCode)
	if len(material.ChainCode) != 32 {
		return nil, coretss.ErrInvalidSharePayload
	}
	return append([]byte(nil), material.ChainCode...), nil
}

func readPrivateRecoveryProofInput(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() <= 0 || info.Size() > recoveryProofInputLimit {
		return nil, errors.New("recovery proof input must be a private regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open recovery proof input")
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return nil, errors.New("validate recovery proof input")
	}
	input, err := io.ReadAll(io.LimitReader(file, recoveryProofInputLimit+1))
	if err != nil || len(input) == 0 || len(input) > recoveryProofInputLimit {
		clear(input)
		return nil, errors.New("read recovery proof input")
	}
	return input, nil
}

func assertRecoveryProofOutputRedacted(t *testing.T, output []byte, canaries []string) {
	t.Helper()
	for _, canary := range canaries {
		if canary != "" && bytes.Contains(output, []byte(canary)) {
			t.Fatal("isolated recovery proof output exposed protected test material")
		}
	}
}

func recoveryProofDescriptor(t *testing.T, keyID string, chainCode []byte) []byte {
	t.Helper()
	chainCodeHash := mpc2of3.ChainCodeHashFor(chainCode).String()
	raw := []byte(`{"algorithm":"ECDSA","chainCodeHash":"` + chainCodeHash + `","curve":"secp256k1","derivationScheme":"bip32_secp256k1","descriptorKind":"mpc-key-descriptor","descriptorVersion":1,"keyId":"` + keyID + `","parties":[{"partyId":"mpc-signer","purpose":"platform"},{"partyId":"co-signer-primary","purpose":"primary"},{"partyId":"co-signer-recovery","purpose":"recovery"}],"protocolVersion":1,"publicKeyFormat":"compressed_sec1","threshold":2}`)
	if _, _, err := mpc2of3.ParseCanonicalDescriptor(raw); err != nil {
		t.Fatal("construct recovery proof descriptor")
	}
	return raw
}

func recoveryProofStoreConfig(t *testing.T, purpose StorePurpose, directory string, provider *KeyProvider) StoreConfig {
	t.Helper()
	partyID := primaryPartyID
	if purpose == StorePurposeRecovery {
		partyID = recoveryPartyID
	}
	config, err := NewStoreConfig(recoveryProofDeploymentID, purpose, partyID, directory, provider)
	if err != nil {
		t.Fatal("construct recovery proof store binding")
	}
	return config
}

func encodeRecoveryProofArtifact(t *testing.T, config StoreConfig, sessionID, keyID string, descriptor, blob []byte) []byte {
	t.Helper()
	nonce := randomRecoveryProofBytes(t, artifactNonceBytes)
	defer clear(nonce)
	encoded, err := encodeArtifactV1(config, PublishInput{
		SessionID:       sessionID,
		KeyID:           keyID,
		PartyID:         config.PartyID(),
		DescriptorBytes: descriptor,
		CodecBlob:       blob,
	}, nonce)
	if err != nil {
		t.Fatal("encode recovery proof artifact")
	}
	return encoded
}

func mismatchedRecoveryProofPublicBlob(t *testing.T, blob []byte) []byte {
	t.Helper()
	material, err := coretss.UnmarshalKeyMaterial(blob)
	if err != nil {
		t.Fatal("prepare recovery proof public mismatch")
	}
	defer clear(material.ChainCode)
	material.Share.ECDSAPub = tsscrypto.ScalarBaseMult(tsslib.S256(), big.NewInt(1))
	encoded, err := coretss.MarshalKeyMaterial(material)
	if err != nil {
		t.Fatal("encode recovery proof public mismatch")
	}
	return encoded
}

func corruptRecoveryProofArtifact(artifact []byte) []byte {
	corrupt := append([]byte(nil), artifact...)
	if len(corrupt) > 0 {
		corrupt[len(corrupt)/2] ^= 0x01
	}
	return corrupt
}

func writeRecoveryProofFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal("write private recovery proof file")
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatal("validate private recovery proof file")
	}
}

func randomRecoveryProofBytes(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	if _, err := io.ReadFull(rand.Reader, value); err != nil {
		t.Fatal("read recovery proof randomness")
	}
	return value
}

type recoveryProofShareWriter struct {
	mu     sync.RWMutex
	shares map[string][]byte
}

func (w *recoveryProofShareWriter) SaveShare(_ context.Context, input coretss.SaveShareInput) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if _, exists := w.shares[input.LocalPartyID]; exists {
		return errors.New("duplicate recovery proof share")
	}
	w.shares[input.LocalPartyID] = append([]byte(nil), input.CodecBlob...)
	return nil
}

func (w *recoveryProofShareWriter) share(partyID string) []byte {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return append([]byte(nil), w.shares[partyID]...)
}

type recoveryProofPreParamsSource struct {
	items chan *ecdsakeygen.LocalPreParams
}

func newRecoveryProofPreParamsSource(items ...*ecdsakeygen.LocalPreParams) *recoveryProofPreParamsSource {
	source := &recoveryProofPreParamsSource{items: make(chan *ecdsakeygen.LocalPreParams, len(items))}
	for _, item := range items {
		source.items <- item
	}
	return source
}

func (s *recoveryProofPreParamsSource) Acquire(ctx context.Context) (*ecdsakeygen.LocalPreParams, error) {
	select {
	case item := <-s.items:
		return item, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func generateRecoveryProofPreParams(t *testing.T, count int) []*ecdsakeygen.LocalPreParams {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	items := make([]*ecdsakeygen.LocalPreParams, count)
	group, groupCtx := errgroup.WithContext(ctx)
	for index := range items {
		index := index
		group.Go(func() error {
			item, err := ecdsakeygen.GeneratePreParamsWithContext(groupCtx, 2)
			if err != nil {
				return errors.New("generate recovery proof pre-parameters")
			}
			items[index] = item
			return nil
		})
	}
	if err := group.Wait(); err != nil {
		t.Fatal("generate recovery proof pre-parameters")
	}
	return items
}

type recoveryProofFrameBus struct {
	mu        sync.RWMutex
	endpoints map[string]chan protocol.Frame
}

type recoveryProofTransport struct {
	bus     *recoveryProofFrameBus
	inbound <-chan protocol.Frame
}

func newRecoveryProofFrameBus(parties []string) (*recoveryProofFrameBus, map[string]coretss.Transport) {
	bus := &recoveryProofFrameBus{endpoints: make(map[string]chan protocol.Frame, len(parties))}
	transports := make(map[string]coretss.Transport, len(parties))
	for _, partyID := range parties {
		inbound := make(chan protocol.Frame, 256)
		bus.endpoints[partyID] = inbound
		transports[partyID] = &recoveryProofTransport{bus: bus, inbound: inbound}
	}
	return bus, transports
}

func (t *recoveryProofTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	t.bus.mu.RLock()
	defer t.bus.mu.RUnlock()
	if frame.IsBroadcast() {
		for partyID, inbound := range t.bus.endpoints {
			if partyID == frame.FromParty {
				continue
			}
			if err := sendRecoveryProofFrame(ctx, inbound, frame); err != nil {
				return err
			}
		}
		return nil
	}
	inbound, ok := t.bus.endpoints[frame.ToParty]
	if !ok {
		return errors.New("recovery proof transport target unavailable")
	}
	return sendRecoveryProofFrame(ctx, inbound, frame)
}

func (t *recoveryProofTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	select {
	case frame := <-t.inbound:
		return frame, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func sendRecoveryProofFrame(ctx context.Context, inbound chan<- protocol.Frame, frame protocol.Frame) error {
	copyFrame := frame
	copyFrame.Payload = append([]byte(nil), frame.Payload...)
	select {
	case inbound <- copyFrame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
