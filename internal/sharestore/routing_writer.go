package sharestore

import (
	"context"
	"errors"

	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type RoutingWriter struct {
	activePair *ActivePair
	primary    *Store
	recovery   *Store
}

func NewRoutingWriter(activePair *ActivePair, primary, recovery *Store) (*RoutingWriter, error) {
	if activePair == nil || primary == nil || recovery == nil {
		return nil, errors.New("routing writer requires active pair and both stores")
	}
	if err := ValidateStorePair(primary.config, recovery.config); err != nil {
		return nil, err
	}
	return &RoutingWriter{activePair: activePair, primary: primary, recovery: recovery}, nil
}

func (w *RoutingWriter) SaveShare(ctx context.Context, input coretss.SaveShareInput) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	resolved, err := w.activePair.resolve(input)
	if err != nil {
		return err
	}
	defer clear(resolved.CodecBlob)
	store := w.primary
	if resolved.PartyID == w.recovery.config.PartyID() {
		store = w.recovery
	} else if resolved.PartyID != w.primary.config.PartyID() {
		return ErrPersistenceContextMismatch
	}
	_, err = store.PublishAndInspect(ctx, PublishInput{
		SessionID:       resolved.SessionID,
		KeyID:           resolved.KeyID,
		PartyID:         resolved.PartyID,
		DescriptorBytes: resolved.DescriptorBytes,
		CodecBlob:       resolved.CodecBlob,
	})
	return err
}

var _ coretss.ShareWriter = (*RoutingWriter)(nil)
