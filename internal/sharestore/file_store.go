package sharestore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const (
	sharesDirPerm os.FileMode = 0o700
	shareFilePerm os.FileMode = 0o600
)

// onDiskShare is the JSON envelope stored per key share file.
type onDiskShare struct {
	KeyID      string    `json:"key_id"`
	OrgID      string    `json:"org_id"`
	Algorithm  string    `json:"algorithm"`
	Curve      string    `json:"curve"`
	Version    uint32    `json:"version"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Ciphertext []byte    `json:"ciphertext"`
}

// FileStore implements tss.ShareStore using the filesystem.
// Each key share is stored as a separate AES-256-GCM encrypted JSON file under dir/<keyID>.json.
type FileStore struct {
	dir string
	key []byte // 32-byte AES-256 key
}

var _ coretss.ShareStore = (*FileStore)(nil)

// NewFileStore creates a FileStore. key must be exactly 32 bytes (AES-256).
// The directory is created if it does not exist.
func NewFileStore(dir string, key []byte) (*FileStore, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("shares dir is required")
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("share encryption key must be 32 bytes, got %d", len(key))
	}
	if err := os.MkdirAll(dir, sharesDirPerm); err != nil {
		return nil, fmt.Errorf("create shares dir: %w", err)
	}

	copiedKey := make([]byte, len(key))
	copy(copiedKey, key)

	return &FileStore{
		dir: dir,
		key: copiedKey,
	}, nil
}

func (s *FileStore) path(keyID string) string {
	return filepath.Join(s.dir, keyID+".json")
}

func (s *FileStore) validateKeyID(keyID string) error {
	k := strings.TrimSpace(keyID)
	if k == "" {
		return errors.New("keyID is required")
	}
	if k == "." || k == ".." {
		return errors.New("keyID must not be relative path")
	}
	if filepath.Base(k) != k || strings.Contains(k, string(filepath.Separator)) {
		return errors.New("keyID must not contain path separators")
	}
	return nil
}

func (s *FileStore) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}

	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}

	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *FileStore) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func (s *FileStore) SaveShare(ctx context.Context, keyID string, blob []byte, meta coretss.ShareMeta) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateKeyID(keyID); err != nil {
		return err
	}

	ciphertext, err := s.encrypt(blob)
	if err != nil {
		return fmt.Errorf("encrypt share: %w", err)
	}

	status := meta.Status
	if strings.TrimSpace(status) == "" {
		status = coretss.ShareStatusActive
	}

	disk := onDiskShare{
		KeyID:      keyID,
		OrgID:      meta.OrgID,
		Algorithm:  meta.Algorithm,
		Curve:      meta.Curve,
		Version:    meta.Version,
		Status:     status,
		CreatedAt:  meta.CreatedAt,
		Ciphertext: ciphertext,
	}

	encoded, err := json.Marshal(disk)
	if err != nil {
		return fmt.Errorf("encode share: %w", err)
	}

	if err := s.atomicWrite(s.path(keyID), encoded); err != nil {
		return fmt.Errorf("write share: %w", err)
	}
	return nil
}

func (s *FileStore) LoadShare(ctx context.Context, keyID string) (*coretss.StoredShare, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.validateKeyID(keyID); err != nil {
		return nil, err
	}

	encoded, err := os.ReadFile(s.path(keyID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, coretss.ErrShareNotFound
		}
		return nil, fmt.Errorf("read share: %w", err)
	}

	var disk onDiskShare
	if err := json.Unmarshal(encoded, &disk); err != nil {
		return nil, fmt.Errorf("%w: decode share: %v", coretss.ErrInvalidSharePayload, err)
	}
	if strings.EqualFold(strings.TrimSpace(disk.Status), coretss.ShareStatusDisabled) {
		return nil, coretss.ErrShareDisabled
	}

	plaintext, err := s.decrypt(disk.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt share: %w", err)
	}

	return &coretss.StoredShare{
		Blob: plaintext,
		Meta: coretss.ShareMeta{
			KeyID:     disk.KeyID,
			OrgID:     disk.OrgID,
			Algorithm: disk.Algorithm,
			Curve:     disk.Curve,
			Version:   disk.Version,
			Status:    disk.Status,
			CreatedAt: disk.CreatedAt,
		},
	}, nil
}

func (s *FileStore) DisableShare(ctx context.Context, keyID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.validateKeyID(keyID); err != nil {
		return err
	}

	encoded, err := os.ReadFile(s.path(keyID))
	if err != nil {
		if os.IsNotExist(err) {
			return coretss.ErrShareNotFound
		}
		return fmt.Errorf("read share: %w", err)
	}

	var disk onDiskShare
	if err := json.Unmarshal(encoded, &disk); err != nil {
		return fmt.Errorf("%w: decode share: %v", coretss.ErrInvalidSharePayload, err)
	}

	disk.Status = coretss.ShareStatusDisabled

	updated, err := json.Marshal(disk)
	if err != nil {
		return fmt.Errorf("encode share: %w", err)
	}
	if err := s.atomicWrite(s.path(keyID), updated); err != nil {
		return fmt.Errorf("disable share: %w", err)
	}
	return nil
}

func (s *FileStore) atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, sharesDirPerm); err != nil {
		return fmt.Errorf("create parent dir: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()

	cleanup := func() {
		_ = os.Remove(tmpPath)
	}

	if err := tmp.Chmod(shareFilePerm); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("set temp file mode: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return fmt.Errorf("close temp file: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		cleanup()
		return fmt.Errorf("rename temp file: %w", err)
	}
	if err := os.Chmod(path, shareFilePerm); err != nil {
		return fmt.Errorf("set final file mode: %w", err)
	}
	return nil
}
