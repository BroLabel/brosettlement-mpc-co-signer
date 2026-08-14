package sharestore

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	artifactVersion          = 1
	artifactPayloadVersion   = 1
	artifactEncryption       = "AES-256-GCM"
	artifactNonceBytes       = 12
	artifactTagBytes         = 16
	maxArtifactEnvelopeBytes = 16 << 20
	maxArtifactCipherBytes   = 12 << 20
	maxArtifactPayloadBytes  = 8 << 20
)

var canonicalKeyIDPattern = regexp.MustCompile(`^mpc_key_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var (
	ErrArtifactExists             = errors.New("immutable artifact already exists")
	ErrArtifactBinding            = errors.New("artifact binding mismatch")
	ErrUnsupportedPublishPlatform = errors.New("immutable artifact publication is unsupported on this platform")
)

type artifactEncryptionV1 struct {
	Algorithm  string `json:"algorithm"`
	KeyRef     string `json:"keyRef"`
	Nonce      string `json:"nonce"`
	Ciphertext string `json:"ciphertext"`
	Tag        string `json:"tag"`
}

type artifactEnvelopeV1 struct {
	Version    int                  `json:"version"`
	Encryption artifactEncryptionV1 `json:"encryption"`
}

type artifactPayloadV1 struct {
	ArtifactPayloadVersion int    `json:"artifactPayloadVersion"`
	SessionID              string `json:"sessionId"`
	PartyID                string `json:"partyId"`
	DescriptorBytesBase64  string `json:"descriptorBytesBase64"`
	ShareBlob              string `json:"shareBlob"`
}

type PublishInput struct {
	SessionID       string
	KeyID           string
	PartyID         string
	DescriptorBytes []byte
	CodecBlob       []byte
}

type ExpectedArtifactContext struct {
	SessionID       string
	KeyID           string
	DescriptorBytes []byte
}

type ArtifactEvidence struct {
	SessionID             string
	KeyID                 string
	PartyID               string
	Purpose               StorePurpose
	DescriptorFingerprint mpc2of3.DescriptorFingerprint
	AccountPublicKey      []byte
	ChainCodeHash         mpc2of3.ChainCodeHash
	CodecVersion          uint32
	ArtifactFingerprint   mpc2of3.ArtifactFingerprint
}

// Store is a purpose-bound encrypted artifact capability. It intentionally
// does not implement the core ShareReader or ShareWriter interfaces.
type Store struct {
	config           StoreConfig
	nonceSource      io.Reader
	publishSupported func() bool
	publishFile      func(string, []byte) error
	readFile         func(string) ([]byte, error)
}

// OpenStore retains the immutable purpose/key binding when only the
// provisioning filesystem capability is unavailable. Callers must keep DKG
// admission closed for the process lifetime when the returned error is non-nil.
func OpenStore(config StoreConfig) (*Store, error) {
	if config.keyProvider == nil {
		return nil, errors.New("artifact key provider is required")
	}
	if _, err := partyIDForPurpose(config.Purpose()); err != nil {
		return nil, err
	}
	store := &Store{
		config:           config,
		nonceSource:      rand.Reader,
		publishSupported: publishPlatformSupported,
		publishFile:      publishArtifactFile,
		readFile:         readArtifactFile,
	}
	if err := ensurePrivateStoreDirectory(config.Directory()); err != nil {
		return store, err
	}
	if !publishPlatformSupported() {
		return store, ErrUnsupportedPublishPlatform
	}
	return store, nil
}

func ensurePrivateStoreDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return errors.New("artifact destination must exist before startup")
	}
	if err != nil {
		return fmt.Errorf("inspect artifact directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("artifact destination must be a regular directory, not a symlink")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("artifact directory permissions must be private, got %04o", info.Mode().Perm())
	}
	if err := validateStoreDirectoryOwner(info); err != nil {
		return err
	}
	return nil
}

func (s *Store) finalPath(keyID string) (string, error) {
	if s == nil {
		return "", errors.New("artifact store is nil")
	}
	if !canonicalKeyIDPattern.MatchString(keyID) {
		return "", errors.New("artifact key ID is not canonical")
	}
	return filepath.Join(s.config.Directory(), keyID+"."+string(s.config.Purpose())+".json"), nil
}

func encodeArtifactV1(config StoreConfig, input PublishInput, nonce []byte) ([]byte, error) {
	if len(nonce) != artifactNonceBytes {
		return nil, fmt.Errorf("artifact nonce must be %d bytes", artifactNonceBytes)
	}
	if err := validatePublishInput(config, input); err != nil {
		return nil, err
	}
	payloadBytes, err := json.Marshal(artifactPayloadV1{
		ArtifactPayloadVersion: artifactPayloadVersion,
		SessionID:              input.SessionID,
		PartyID:                input.PartyID,
		DescriptorBytesBase64:  base64.StdEncoding.EncodeToString(input.DescriptorBytes),
		ShareBlob:              base64.StdEncoding.EncodeToString(input.CodecBlob),
	})
	if err != nil {
		return nil, fmt.Errorf("encode artifact payload: %w", err)
	}
	if len(payloadBytes) > maxArtifactPayloadBytes {
		return nil, fmt.Errorf("%w: decrypted payload exceeds limit", coretss.ErrInvalidSharePayload)
	}

	block, err := aes.NewCipher(config.keyProvider.key[:])
	if err != nil {
		return nil, fmt.Errorf("initialize artifact cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("initialize artifact GCM: %w", err)
	}
	sealed := gcm.Seal(nil, nonce, payloadBytes, nil)
	ciphertext := sealed[:len(sealed)-artifactTagBytes]
	tag := sealed[len(sealed)-artifactTagBytes:]
	finalBytes, err := json.Marshal(artifactEnvelopeV1{
		Version: artifactVersion,
		Encryption: artifactEncryptionV1{
			Algorithm:  artifactEncryption,
			KeyRef:     config.KeyRef(),
			Nonce:      base64.StdEncoding.EncodeToString(nonce),
			Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
			Tag:        base64.StdEncoding.EncodeToString(tag),
		},
	})
	clear(payloadBytes)
	clear(sealed)
	if err != nil {
		return nil, fmt.Errorf("encode artifact envelope: %w", err)
	}
	if len(finalBytes) > maxArtifactEnvelopeBytes {
		return nil, fmt.Errorf("%w: artifact envelope exceeds limit", coretss.ErrInvalidSharePayload)
	}
	return finalBytes, nil
}

func validatePublishInput(config StoreConfig, input PublishInput) error {
	if input.SessionID == "" {
		return fmt.Errorf("%w: session ID is required", ErrArtifactBinding)
	}
	if input.PartyID != config.PartyID() {
		return fmt.Errorf("%w: party ID", ErrArtifactBinding)
	}
	descriptor, _, err := mpc2of3.ParseCanonicalDescriptor(input.DescriptorBytes)
	if err != nil {
		return fmt.Errorf("%w: descriptor: %v", coretss.ErrInvalidSharePayload, err)
	}
	if descriptor.KeyID != input.KeyID {
		return fmt.Errorf("%w: key ID", ErrArtifactBinding)
	}
	if !canonicalKeyIDPattern.MatchString(input.KeyID) {
		return fmt.Errorf("%w: key ID", ErrArtifactBinding)
	}
	if len(input.CodecBlob) == 0 || len(input.CodecBlob) > maxArtifactPayloadBytes {
		return fmt.Errorf("%w: codec blob size", coretss.ErrInvalidSharePayload)
	}
	if _, err := coretss.InspectEncodedECDSAKeyMaterial(input.CodecBlob); err != nil {
		return err
	}
	return nil
}

func strictBase64(field string, encoded string, maximum int) ([]byte, error) {
	if encoded == "" || len(encoded) > base64.StdEncoding.EncodedLen(maximum) {
		return nil, fmt.Errorf("%w: %s size", coretss.ErrInvalidSharePayload, field)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(decoded) > maximum || base64.StdEncoding.EncodeToString(decoded) != encoded {
		clear(decoded)
		return nil, fmt.Errorf("%w: %s must be canonical padded base64", coretss.ErrInvalidSharePayload, field)
	}
	return decoded, nil
}
