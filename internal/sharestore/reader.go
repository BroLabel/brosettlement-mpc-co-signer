package sharestore

import (
	"context"
	"errors"
	"fmt"
	"os"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func (s *Store) Exists(ctx context.Context, keyID string) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	path, err := s.finalPath(keyID)
	if err != nil {
		return false, err
	}
	_, err = os.Lstat(path)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, os.ErrNotExist):
		return false, nil
	default:
		return false, fmt.Errorf("inspect addressed artifact path: %w", err)
	}
}

func (s *Store) InspectExisting(ctx context.Context, expected ExpectedArtifactContext) (ArtifactEvidence, error) {
	if err := ctx.Err(); err != nil {
		return ArtifactEvidence{}, err
	}
	path, err := s.finalPath(expected.KeyID)
	if err != nil {
		return ArtifactEvidence{}, err
	}
	finalBytes, err := s.readFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ArtifactEvidence{}, coretss.ErrShareNotFound
		}
		return ArtifactEvidence{}, fmt.Errorf("read immutable artifact: %w", err)
	}
	stored, evidence, err := inspectArtifactBytes(s.config, expected, finalBytes)
	if stored != nil {
		clear(stored.Blob)
	}
	clear(finalBytes)
	return evidence, err
}
