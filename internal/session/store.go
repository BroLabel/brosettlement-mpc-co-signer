package session

import (
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
)

type State int

const (
	StateRunning State = iota
	StateCompleted
	StateFailed
	StateTimedOut
)

type Session struct {
	ID        string
	OrgID     string
	Transport *transport.StreamTransport
	State     State
	StartedAt time.Time
	ExpiresAt time.Time
	doneAt    time.Time
}

func (s *Session) isTerminal() bool {
	return s.State == StateCompleted || s.State == StateFailed || s.State == StateTimedOut
}

type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{
		sessions: make(map[string]*Session),
	}
}

func (s *Store) Create(id, orgID string, expiresAt time.Time, tr *transport.StreamTransport) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()

	if existing, ok := s.sessions[id]; ok {
		return existing
	}

	sess := &Session{
		ID:        id,
		OrgID:     orgID,
		Transport: tr,
		State:     StateRunning,
		StartedAt: time.Now(),
		ExpiresAt: expiresAt,
	}

	s.sessions[id] = sess
	return sess
}

func (s *Store) Get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	return sess, ok
}

func (s *Store) SetState(id string, state State) {
	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[id]
	if !ok {
		return
	}

	sess.State = state
	if sess.isTerminal() {
		sess.doneAt = time.Now()
	}
}

func (s *Store) Cleanup(retention time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()

	cutoff := time.Now().Add(-retention)
	for id, sess := range s.sessions {
		if sess.isTerminal() && sess.doneAt.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}
