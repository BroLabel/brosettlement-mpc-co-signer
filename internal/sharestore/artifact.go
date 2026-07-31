package sharestore

import (
	"bytes"
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
	storeDirectoryPerm       = 0o700
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

func NewStore(config StoreConfig) (*Store, error) {
	if !publishPlatformSupported() {
		return nil, ErrUnsupportedPublishPlatform
	}
	return newStore(config)
}

// OpenStore retains the immutable purpose/key binding when only the
// provisioning filesystem capability is unavailable. Callers must keep DKG
// admission closed for the process lifetime when the returned error is non-nil.
func OpenStore(config StoreConfig) (*Store, error) {
	store, err := configuredStore(config)
	if err != nil {
		return nil, err
	}
	if !publishPlatformSupported() {
		return store, ErrUnsupportedPublishPlatform
	}
	if err := ensurePrivateStoreDirectory(config.Directory()); err != nil {
		return store, err
	}
	return store, nil
}

func newStore(config StoreConfig) (*Store, error) {
	store, err := configuredStore(config)
	if err != nil {
		return nil, err
	}
	if err := ensurePrivateStoreDirectory(config.Directory()); err != nil {
		return nil, err
	}
	return store, nil
}

func configuredStore(config StoreConfig) (*Store, error) {
	if config.keyProvider == nil {
		return nil, errors.New("artifact key provider is required")
	}
	if err := validatePurposeParty(config.Purpose(), config.PartyID()); err != nil {
		return nil, err
	}
	return &Store{
		config:           config,
		nonceSource:      rand.Reader,
		publishSupported: publishPlatformSupported,
		publishFile:      publishArtifactFile,
		readFile:         readArtifactFile,
	}, nil
}

func ensurePrivateStoreDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(directory, storeDirectoryPerm); err != nil {
			return fmt.Errorf("create artifact directory: %w", err)
		}
		info, err = os.Lstat(directory)
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
	path := filepath.Join(s.config.Directory(), keyID+"."+string(s.config.Purpose())+".json")
	relative, err := filepath.Rel(s.config.Directory(), path)
	if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." || len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", errors.New("artifact path escapes configured destination")
	}
	return path, nil
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

func decodeClosedJSON(raw []byte, destination any) error {
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	canonical, err := json.Marshal(destination)
	if err != nil {
		return err
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("JSON does not use the exact compact field representation")
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := walkJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing data")
	}
	return nil
}

func walkJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delim {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("duplicate JSON key %q", key)
			}
			seen[key] = struct{}{}
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated JSON object")
		}
	case '[':
		for decoder.More() {
			if err := walkJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated JSON array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}
