package sharestore

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
)

const capabilityProbeEntropyBytes = 16

var (
	capabilityProbeFirstBytes  = []byte("mpc-artifact-capability-v1:first")
	capabilityProbeSecondBytes = []byte("mpc-artifact-capability-v1:second")
)

type publishTempFile interface {
	Name() string
	Chmod(os.FileMode) error
	Write([]byte) (int, error)
	Sync() error
	Close() error
}

type publishOperations interface {
	CreateTemp(directory, pattern string) (publishTempFile, error)
	Remove(path string) error
	RenameNoReplace(oldPath, newPath string) error
	SyncDirectory(directory string) error
}

func publishDurably(operations publishOperations, finalPath string, finalBytes []byte) error {
	_, err := publishDurablyTracked(operations, finalPath, finalBytes)
	return err
}

func publishDurablyTracked(operations publishOperations, finalPath string, finalBytes []byte) (published bool, err error) {
	directory := filepath.Dir(finalPath)
	temp, err := operations.CreateTemp(directory, "."+filepath.Base(finalPath)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create artifact temp file: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if !published {
			_ = operations.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(artifactFilePerm); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("set artifact temp mode: %w", err)
	}
	written, err := temp.Write(finalBytes)
	if err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("write artifact temp file: %w", err)
	}
	if written != len(finalBytes) {
		_ = temp.Close()
		return false, io.ErrShortWrite
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return false, fmt.Errorf("sync artifact temp file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return false, fmt.Errorf("close artifact temp file: %w", err)
	}
	if err := operations.RenameNoReplace(tempPath, finalPath); err != nil {
		return false, err
	}
	published = true
	if err := operations.SyncDirectory(directory); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Store) ProbePublishCapability(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s == nil || !s.publishSupported() {
		return ErrUnsupportedPublishPlatform
	}
	if err := ensurePrivateStoreDirectory(s.config.Directory()); err != nil {
		return err
	}
	if err := probePlatformPublishCapability(s.config.Directory()); err != nil {
		return fmt.Errorf("probe immutable artifact publication: %w", err)
	}
	return nil
}

func probePublishCapability(
	operations publishOperations,
	readFile func(string) ([]byte, error),
	directory string,
	entropy io.Reader,
) (returnErr error) {
	randomSuffix := make([]byte, capabilityProbeEntropyBytes)
	if _, err := io.ReadFull(entropy, randomSuffix); err != nil {
		return fmt.Errorf("generate capability probe name: %w", err)
	}
	finalPath := filepath.Join(directory, ".artifact-capability-probe-"+hex.EncodeToString(randomSuffix))
	clear(randomSuffix)

	ownsFinal := false
	defer func() {
		if !ownsFinal {
			return
		}
		removeErr := operations.Remove(finalPath)
		syncErr := operations.SyncDirectory(directory)
		if removeErr != nil {
			removeErr = fmt.Errorf("remove capability probe: %w", removeErr)
		}
		if syncErr != nil {
			syncErr = fmt.Errorf("sync capability probe cleanup: %w", syncErr)
		}
		returnErr = errors.Join(returnErr, removeErr, syncErr)
	}()

	published, err := publishDurablyTracked(operations, finalPath, capabilityProbeFirstBytes)
	ownsFinal = published
	if err != nil {
		return fmt.Errorf("publish capability probe: %w", err)
	}

	replaced, noReplaceErr := publishDurablyTracked(operations, finalPath, capabilityProbeSecondBytes)
	if replaced {
		ownsFinal = true
		return errors.New("filesystem publication replaced an existing capability probe")
	}
	if !errors.Is(noReplaceErr, ErrArtifactExists) {
		return fmt.Errorf("filesystem does not provide create-only rename: %w", noReplaceErr)
	}

	readback, err := readFile(finalPath)
	if err != nil {
		return fmt.Errorf("read capability probe: %w", err)
	}
	defer clear(readback)
	if !bytes.Equal(readback, capabilityProbeFirstBytes) {
		return errors.New("create-only capability probe changed existing bytes")
	}
	return nil
}

func (s *Store) PublishAndInspect(ctx context.Context, input PublishInput) (evidence ArtifactEvidence, returnErr error) {
	defer func() { metrics.ObserveArtifactPublish(returnErr == nil) }()
	if err := ctx.Err(); err != nil {
		return ArtifactEvidence{}, err
	}
	if !s.publishSupported() {
		return ArtifactEvidence{}, ErrUnsupportedPublishPlatform
	}
	path, err := s.finalPath(input.KeyID)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	nonce := make([]byte, artifactNonceBytes)
	if _, err := io.ReadFull(s.nonceSource, nonce); err != nil {
		clear(nonce)
		return ArtifactEvidence{}, fmt.Errorf("generate artifact nonce: %w", err)
	}
	finalBytes, err := encodeArtifactV1(s.config, input, nonce)
	clear(nonce)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	defer clear(finalBytes)
	if err := s.publishFile(path, finalBytes); err != nil {
		return ArtifactEvidence{}, err
	}
	return s.InspectExisting(ctx, ExpectedArtifactContext{
		SessionID:       input.SessionID,
		KeyID:           input.KeyID,
		DescriptorBytes: input.DescriptorBytes,
	})
}
