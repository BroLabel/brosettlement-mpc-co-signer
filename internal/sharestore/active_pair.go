package sharestore

import (
	"bytes"
	"errors"
	"fmt"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

var (
	ErrPairNotRegistered          = errors.New("active persistence pair is not registered")
	ErrPairAlreadyRegistered      = errors.New("active persistence pair is already registered")
	ErrPersistenceContextMismatch = errors.New("core persistence context does not match active pair")
)

type PairRegistration struct {
	SessionID       string
	KeyID           string
	PrimaryPartyID  string
	RecoveryPartyID string
	DescriptorBytes []byte
}

type resolvedPersistenceContext struct {
	SessionID       string
	KeyID           string
	PartyID         string
	DescriptorBytes []byte
	CodecBlob       []byte
}

type activePairState struct {
	generation      uint64
	sessionID       string
	keyID           string
	primaryPartyID  string
	recoveryPartyID string
	descriptorBytes []byte
	fingerprint     [32]byte
}

type ActivePair struct {
	mu         sync.RWMutex
	generation uint64
	active     *activePairState
}

type PairLease struct {
	slot       *ActivePair
	generation uint64
}

func NewActivePair() *ActivePair { return &ActivePair{} }

func (s *ActivePair) RegisterPair(registration PairRegistration) (*PairLease, error) {
	if s == nil {
		return nil, errors.New("active pair is nil")
	}
	descriptor, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(registration.DescriptorBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid descriptor", ErrPersistenceContextMismatch)
	}
	if registration.SessionID == "" || descriptor.KeyID != registration.KeyID ||
		registration.PrimaryPartyID != primaryPartyID || registration.RecoveryPartyID != recoveryPartyID ||
		registration.PrimaryPartyID == registration.RecoveryPartyID {
		return nil, ErrPersistenceContextMismatch
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.active != nil {
		return nil, ErrPairAlreadyRegistered
	}
	s.generation++
	rawFingerprint := [32]byte(fingerprint)
	s.active = &activePairState{
		generation:      s.generation,
		sessionID:       registration.SessionID,
		keyID:           registration.KeyID,
		primaryPartyID:  registration.PrimaryPartyID,
		recoveryPartyID: registration.RecoveryPartyID,
		descriptorBytes: append([]byte(nil), registration.DescriptorBytes...),
		fingerprint:     rawFingerprint,
	}
	return &PairLease{slot: s, generation: s.generation}, nil
}

func (s *ActivePair) resolve(input coretss.SaveShareInput) (resolvedPersistenceContext, error) {
	if s == nil {
		return resolvedPersistenceContext{}, ErrPairNotRegistered
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.active == nil {
		return resolvedPersistenceContext{}, ErrPairNotRegistered
	}
	active := s.active
	partyAllowed := input.LocalPartyID == active.primaryPartyID || input.LocalPartyID == active.recoveryPartyID
	if input.SessionID != active.sessionID || input.KeyID != active.keyID || !partyAllowed ||
		!bytes.Equal(input.OpaqueDescriptorFingerprint, active.fingerprint[:]) {
		return resolvedPersistenceContext{}, ErrPersistenceContextMismatch
	}
	return resolvedPersistenceContext{
		SessionID:       active.sessionID,
		KeyID:           active.keyID,
		PartyID:         input.LocalPartyID,
		DescriptorBytes: append([]byte(nil), active.descriptorBytes...),
		CodecBlob:       append([]byte(nil), input.CodecBlob...),
	}, nil
}

func (l *PairLease) Release() error {
	if l == nil || l.slot == nil {
		return nil
	}
	l.slot.mu.Lock()
	defer l.slot.mu.Unlock()
	if l.slot.active != nil && l.slot.active.generation == l.generation {
		clear(l.slot.active.descriptorBytes)
		l.slot.active = nil
	}
	return nil
}
