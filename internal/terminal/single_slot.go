package terminal

import (
	"context"
	"errors"
	"sync"
)

var (
	ErrPublisherNotStarted     = errors.New("terminal publisher is not started")
	ErrPublisherAlreadyStarted = errors.New("terminal publisher is already started")
	ErrPublisherSlotOccupied   = errors.New("terminal publisher slot is occupied")
)

type PublishResult struct {
	Outcome Outcome
	Err     error
}

type SingleSlot struct {
	publisher *Publisher

	mu       sync.Mutex
	started  bool
	occupied bool
	ctx      context.Context
	wg       sync.WaitGroup
}

func NewSingleSlot(publisher *Publisher) (*SingleSlot, error) {
	if publisher == nil || publisher.sender == nil {
		return nil, errors.New("single-slot terminal publisher requires a publisher")
	}
	return &SingleSlot{publisher: publisher}, nil
}

func (s *SingleSlot) Start(ctx context.Context) error {
	if ctx == nil {
		return errors.New("single-slot terminal publisher requires lifecycle context")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return ErrPublisherAlreadyStarted
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.ctx = ctx
	s.started = true
	return nil
}

func (s *SingleSlot) Handoff(ctx context.Context, job Job, done func(PublishResult)) error {
	if !job.valid() {
		return errors.New("terminal publication job is invalid")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return ErrPublisherNotStarted
	}
	if err := s.ctx.Err(); err != nil {
		s.mu.Unlock()
		return err
	}
	if s.occupied {
		s.mu.Unlock()
		return ErrPublisherSlotOccupied
	}
	s.occupied = true
	lifecycleCtx := s.ctx
	started := make(chan struct{})
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		close(started)
		outcome, err := s.publisher.Publish(lifecycleCtx, job)
		s.mu.Lock()
		s.occupied = false
		s.mu.Unlock()
		if done != nil {
			done(PublishResult{Outcome: outcome, Err: err})
		}
	}()
	s.mu.Unlock()

	<-started
	return nil
}

func (s *SingleSlot) Wait() {
	if s != nil {
		s.wg.Wait()
	}
}
