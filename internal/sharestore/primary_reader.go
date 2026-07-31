package sharestore

import (
	"context"
	"errors"
	"fmt"
	"os"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type PrimaryReader struct {
	store *Store
}

func NewPrimaryReader(store *Store) (*PrimaryReader, error) {
	if store == nil || store.config.Purpose() != StorePurposePrimary {
		return nil, errors.New("primary reader requires a primary artifact store")
	}
	return &PrimaryReader{store: store}, nil
}

func (r *PrimaryReader) LoadShare(ctx context.Context, keyID string) (*coretss.StoredShare, error) {
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
		return nil, fmt.Errorf("read primary artifact: %w", err)
	}
	defer clear(finalBytes)
	stored, _, err := loadValidatedRuntimeShare(r.store.config, nil, finalBytes)
	if err != nil {
		return nil, err
	}
	return stored, nil
}

var _ coretss.ShareReader = (*PrimaryReader)(nil)
