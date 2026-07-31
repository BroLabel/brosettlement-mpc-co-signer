package worker

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/preparams"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

func TestDKGCoordinatorDiscardsFirstPreParamsHandleWhenSecondAcquireFails(t *testing.T) {
	intent := coordinatorIntent(t)
	secondAcquireErr := errors.New("second acquire failed")
	service := newScriptedCoordinatorService()
	service.acquireErrors[2] = secondAcquireErr
	activePair := sharestore.NewActivePair()
	coordinator := mustNewHandleCoordinator(t, service, activePair, intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, secondAcquireErr) {
		t.Fatalf("Run() error = %v, want second acquire failure", err)
	}
	if got := service.AcquireOrder(); len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("acquisition order = %v, want B then C", got)
	}
	if got := service.Handle(1).State(); got != handleDiscarded {
		t.Fatalf("B handle state = %s, want discarded", got)
	}
	if service.HandleRunCount() != 0 {
		t.Fatalf("handle-aware runtime calls = %d, want zero", service.HandleRunCount())
	}
	assertPairAvailable(t, activePair, intent)
	if service.FinishCount() != 1 {
		t.Fatalf("refill finishes = %d, want 1", service.FinishCount())
	}
}

func TestDKGCoordinatorResumesRefillWhenFirstAcquireFails(t *testing.T) {
	intent := coordinatorIntent(t)
	firstAcquireErr := errors.New("first acquire failed")
	service := newScriptedCoordinatorService()
	service.acquireErrors[1] = firstAcquireErr
	coordinator := mustNewHandleCoordinator(t, service, sharestore.NewActivePair(), intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, firstAcquireErr) {
		t.Fatalf("Run() error = %v, want first acquire failure", err)
	}
	if got := service.AcquireOrder(); len(got) != 1 || got[0] != 1 {
		t.Fatalf("acquisition order = %v, want only B", got)
	}
	if service.FinishCount() != 1 {
		t.Fatalf("refill finishes = %d, want 1", service.FinishCount())
	}
}

func TestDKGCoordinatorDiscardsBothPreParamsHandlesWhenPairRegistrationFails(t *testing.T) {
	intent := coordinatorIntent(t)
	service := newScriptedCoordinatorService()
	activePair := sharestore.NewActivePair()
	occupied, err := activePair.RegisterPair(pairRegistration(intent))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Release()
	coordinator := mustNewHandleCoordinator(t, service, activePair, intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, sharestore.ErrPairAlreadyRegistered) {
		t.Fatalf("Run() error = %v, want pair registration failure", err)
	}
	if got := service.AcquireOrder(); len(got) != 2 {
		t.Fatalf("acquisitions = %v, want two before registration", got)
	}
	if service.Handle(1).State() != handleDiscarded || service.Handle(2).State() != handleDiscarded {
		t.Fatalf("handle states = B:%s C:%s, want both discarded", service.Handle(1).State(), service.Handle(2).State())
	}
	if service.HandleRunCount() != 0 {
		t.Fatalf("handle-aware runtime calls = %d, want zero", service.HandleRunCount())
	}
	if service.FinishCount() != 1 {
		t.Fatalf("refill finishes = %d, want 1", service.FinishCount())
	}
}

func TestDKGCoordinatorKeepsStartBarrierClosedUntilBothHandlesAndPairExist(t *testing.T) {
	intent := coordinatorIntent(t)
	service := newScriptedCoordinatorService()
	secondAcquireGate := make(chan struct{})
	service.acquireGates[2] = secondAcquireGate
	activePair := sharestore.NewActivePair()
	coordinator := mustNewHandleCoordinator(t, service, activePair, intent)

	result := make(chan error, 1)
	go func() {
		_, err := coordinator.Run(context.Background(), intent, &blockingTransport{})
		result <- err
	}()

	service.WaitForAcquire(t, 2)
	if service.HandleRunCount() != 0 {
		t.Fatalf("runtime calls before second handle = %d, want zero", service.HandleRunCount())
	}
	assertPairAvailable(t, activePair, intent)

	close(secondAcquireGate)
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if service.Handle(1).State() != handleConsumed || service.Handle(2).State() != handleConsumed {
		t.Fatalf("handle states = B:%s C:%s, want exactly consumed", service.Handle(1).State(), service.Handle(2).State())
	}
	if service.RunHandleForParty(coordinatorPrimaryParty) != 1 ||
		service.RunHandleForParty(coordinatorRecoveryParty) != 2 {
		t.Fatalf(
			"party handle mapping = B:%d C:%d, want acquired B:1 then C:2",
			service.RunHandleForParty(coordinatorPrimaryParty),
			service.RunHandleForParty(coordinatorRecoveryParty),
		)
	}
	if service.FinishCount() != 1 || service.ActiveRunsAtFinish() != 0 {
		t.Fatalf("refill finish count/active runs = %d/%d, want 1/0", service.FinishCount(), service.ActiveRunsAtFinish())
	}
}

func TestDKGCoordinatorCancelsSiblingAndKeepsPairUntilBothHandleRunsReturnBeforeRefill(t *testing.T) {
	intent := coordinatorIntent(t)
	primaryErr := errors.New("primary runtime failed")
	service := newScriptedCoordinatorService()
	recoveryCleanup := make(chan struct{})
	recoveryCanceled := make(chan struct{})
	service.run = func(ctx context.Context, request coretss.DKGSessionRequest) error {
		if request.LocalPartyID == coordinatorPrimaryParty {
			return primaryErr
		}
		<-ctx.Done()
		close(recoveryCanceled)
		<-recoveryCleanup
		return ctx.Err()
	}
	activePair := sharestore.NewActivePair()
	coordinator := mustNewHandleCoordinator(t, service, activePair, intent)

	result := make(chan error, 1)
	go func() {
		_, err := coordinator.Run(context.Background(), intent, &blockingTransport{})
		result <- err
	}()
	waitForSignal(t, recoveryCanceled, "recovery runtime did not observe sibling cancellation")

	if lease, err := activePair.RegisterPair(pairRegistration(intent)); !errors.Is(err, sharestore.ErrPairAlreadyRegistered) {
		if err == nil {
			_ = lease.Release()
		}
		t.Fatalf("pair registration while recovery cleanup active error = %v, want collision", err)
	}
	select {
	case err := <-result:
		t.Fatalf("Run returned before recovery SaveShare cleanup: %v", err)
	default:
	}
	if service.FinishCount() != 0 {
		t.Fatal("refill resumed before both runtimes returned")
	}

	close(recoveryCleanup)
	if err := <-result; !errors.Is(err, primaryErr) {
		t.Fatalf("Run() error = %v, want primary failure", err)
	}
	assertPairAvailable(t, activePair, intent)
	if service.FinishCount() != 1 || service.ActiveRunsAtFinish() != 0 {
		t.Fatalf("refill finish count/active runs = %d/%d, want 1/0", service.FinishCount(), service.ActiveRunsAtFinish())
	}
}

func TestDKGCoordinatorKeepsPreParamsBarrierWithinIntentDeadline(t *testing.T) {
	intent := coordinatorIntent(t)
	intent.ExpiresAt = time.Now().Add(100 * time.Millisecond)
	service := newScriptedCoordinatorService()
	service.acquireWaitForContext[2] = true
	activePair := sharestore.NewActivePair()
	coordinator := mustNewHandleCoordinator(t, service, activePair, intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want authoritative deadline", err)
	}
	for acquire, deadline := range service.AcquireDeadlines() {
		if !deadline.Equal(intent.ExpiresAt) {
			t.Fatalf("acquire %d deadline = %s, want %s", acquire, deadline, intent.ExpiresAt)
		}
	}
	if service.Handle(1).State() != handleDiscarded {
		t.Fatalf("B handle state = %s, want discarded after deadline", service.Handle(1).State())
	}
	if service.HandleRunCount() != 0 {
		t.Fatalf("runtime calls after deadline = %d, want zero", service.HandleRunCount())
	}
	assertPairAvailable(t, activePair, intent)
}

func TestDKGCoordinatorKeepsBarrierClosedForDivergentJustInTimeChainCodeBinding(t *testing.T) {
	intent := coordinatorIntent(t)
	intent.Payload.ChainCodeHash = "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw"
	service := newScriptedCoordinatorService()
	coordinator := mustNewHandleCoordinator(t, service, sharestore.NewActivePair(), intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, ErrInvalidDKGContext) {
		t.Fatalf("Run() error = %v, want invalid chain-code binding", err)
	}
	if got := len(service.AcquireOrder()); got != 0 {
		t.Fatalf("acquisitions after invalid chain code = %d, want zero", got)
	}
	if service.BeginCount() != 0 {
		t.Fatal("refill was paused for an invalid chain-code binding")
	}
}

func TestDKGCoordinatorKeepsBarrierClosedForMissingJustInTimeChainCode(t *testing.T) {
	intent := coordinatorIntent(t)
	intent.Payload.ChainCode = ""
	service := newScriptedCoordinatorService()
	coordinator := mustNewHandleCoordinator(t, service, sharestore.NewActivePair(), intent)

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, ErrInvalidDKGContext) {
		t.Fatalf("Run() error = %v, want missing chain-code rejection", err)
	}
	if got := len(service.AcquireOrder()); got != 0 {
		t.Fatalf("acquisitions after missing chain code = %d, want zero", got)
	}
}

func TestDKGCoordinatorResumesRefillAfterDivergentPostRuntimeChainCodeEvidence(t *testing.T) {
	intent := coordinatorIntent(t)
	service := newScriptedCoordinatorService()
	primary := coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)
	recovery := coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)
	recovery.ChainCodeHash = [32]byte{1}
	coordinator, err := NewDKGCoordinator(
		service,
		sharestore.NewActivePair(),
		&recordingArtifactInspector{evidence: primary},
		&recordingArtifactInspector{evidence: recovery},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, ErrDKGEvidenceMismatch) {
		t.Fatalf("Run() error = %v, want divergent chain-code evidence rejection", err)
	}
	if service.FinishCount() != 1 {
		t.Fatalf("refill finishes = %d, want one after evidence mismatch", service.FinishCount())
	}
}

func TestDKGCoordinatorPreParamsHandlesProduceDistinctArtifactsWithEqualPublicEvidence(t *testing.T) {
	intent := coordinatorIntent(t)
	service := newScriptedCoordinatorService()
	primary := coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)
	recovery := coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)
	primary.ArtifactFingerprint = [32]byte{1}
	recovery.ArtifactFingerprint = [32]byte{2}
	coordinator, err := NewDKGCoordinator(
		service,
		sharestore.NewActivePair(),
		&recordingArtifactInspector{evidence: primary},
		&recordingArtifactInspector{evidence: recovery},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	result, err := coordinator.Run(context.Background(), intent, &blockingTransport{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Primary.ArtifactFingerprint == result.Recovery.ArtifactFingerprint {
		t.Fatal("B and C artifact fingerprints unexpectedly match")
	}
	if result.Primary.ChainCodeHash != result.Recovery.ChainCodeHash ||
		string(result.Primary.AccountPublicKey) != string(result.Recovery.AccountPublicKey) {
		t.Fatal("distinct B/C artifacts did not retain equal public evidence")
	}
}

func mustNewHandleCoordinator(
	t *testing.T,
	service *scriptedCoordinatorService,
	activePair *sharestore.ActivePair,
	intent monolith.Intent,
) *DKGCoordinator {
	t.Helper()
	coordinator, err := NewDKGCoordinator(
		service,
		activePair,
		&recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)},
		&recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return coordinator
}

func pairRegistration(intent monolith.Intent) sharestore.PairRegistration {
	return sharestore.PairRegistration{
		SessionID:       intent.SessionID,
		KeyID:           coordinatorKeyID,
		PrimaryPartyID:  coordinatorPrimaryParty,
		RecoveryPartyID: coordinatorRecoveryParty,
		DescriptorBytes: intent.Payload.DescriptorBytes,
	}
}

func assertPairAvailable(t *testing.T, activePair *sharestore.ActivePair, intent monolith.Intent) {
	t.Helper()
	lease, err := activePair.RegisterPair(pairRegistration(intent))
	if err != nil {
		t.Fatalf("active pair unavailable: %v", err)
	}
	if err := lease.Release(); err != nil {
		t.Fatal(err)
	}
}

type handleState string

const (
	handleAcquired  handleState = "acquired"
	handleConsumed  handleState = "consumed"
	handleDiscarded handleState = "discarded"
)

type scriptedHandle struct {
	mu    sync.Mutex
	id    int
	state handleState
}

func (h *scriptedHandle) Discard() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	switch h.state {
	case handleAcquired:
		h.state = handleDiscarded
		return nil
	case handleDiscarded:
		return nil
	default:
		return coretss.ErrPreParamsConsumed
	}
}

func (h *scriptedHandle) consume() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.state != handleAcquired {
		return errors.New("test handle was not acquired")
	}
	h.state = handleConsumed
	return nil
}

func (h *scriptedHandle) State() handleState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.state
}

type scriptedCoordinatorService struct {
	mu                    sync.Mutex
	handles               map[int]*scriptedHandle
	acquireErrors         map[int]error
	acquireGates          map[int]<-chan struct{}
	acquireWaitForContext map[int]bool
	acquireOrder          []int
	acquireDeadlines      map[int]time.Time
	acquireStarted        chan int
	handleRuns            int
	legacyRuns            int
	activeRuns            int
	runHandlesByParty     map[string]int
	beginCount            int
	finishCount           int
	activeRunsAtFinish    int
	run                   func(context.Context, coretss.DKGSessionRequest) error
}

func newScriptedCoordinatorService() *scriptedCoordinatorService {
	return &scriptedCoordinatorService{
		handles:               make(map[int]*scriptedHandle),
		acquireErrors:         make(map[int]error),
		acquireGates:          make(map[int]<-chan struct{}),
		acquireWaitForContext: make(map[int]bool),
		acquireDeadlines:      make(map[int]time.Time),
		acquireStarted:        make(chan int, 4),
		runHandlesByParty:     make(map[string]int),
	}
}

func (s *scriptedCoordinatorService) BeginJob(context.Context) (func(), error) {
	s.mu.Lock()
	s.beginCount++
	s.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			s.finishCount++
			s.activeRunsAtFinish = s.activeRuns
			s.mu.Unlock()
		})
	}, nil
}

func (s *scriptedCoordinatorService) AcquireDKGPreParams(ctx context.Context) (preparams.Handle, error) {
	s.mu.Lock()
	index := len(s.acquireOrder) + 1
	s.acquireOrder = append(s.acquireOrder, index)
	if deadline, ok := ctx.Deadline(); ok {
		s.acquireDeadlines[index] = deadline
	}
	gate := s.acquireGates[index]
	waitForContext := s.acquireWaitForContext[index]
	acquireErr := s.acquireErrors[index]
	s.mu.Unlock()
	s.acquireStarted <- index

	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if waitForContext {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if acquireErr != nil {
		return nil, acquireErr
	}
	handle := &scriptedHandle{id: index, state: handleAcquired}
	s.mu.Lock()
	s.handles[index] = handle
	s.mu.Unlock()
	return handle, nil
}

func (s *scriptedCoordinatorService) RunDKGSessionWithPreParams(
	ctx context.Context,
	request coretss.DKGSessionRequest,
	handle preparams.Handle,
) (coretss.DKGOutput, error) {
	owned, ok := handle.(*scriptedHandle)
	if !ok {
		return coretss.DKGOutput{}, errors.New("unexpected test handle")
	}
	if err := owned.consume(); err != nil {
		return coretss.DKGOutput{}, err
	}
	s.mu.Lock()
	s.handleRuns++
	s.activeRuns++
	s.runHandlesByParty[request.LocalPartyID] = owned.id
	run := s.run
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.activeRuns--
		s.mu.Unlock()
	}()
	if run != nil {
		if err := run(ctx, request); err != nil {
			return coretss.DKGOutput{}, err
		}
	}
	return coretss.DKGOutput{KeyID: request.Session.KeyID}, nil
}

func (s *scriptedCoordinatorService) RunDKGSession(context.Context, coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	s.mu.Lock()
	s.legacyRuns++
	s.mu.Unlock()
	return coretss.DKGOutput{}, errors.New("legacy dkg path used")
}

func (s *scriptedCoordinatorService) WaitForAcquire(t *testing.T, want int) {
	t.Helper()
	for {
		select {
		case got := <-s.acquireStarted:
			if got == want {
				return
			}
		case <-time.After(time.Second):
			t.Fatalf("acquire %d did not start", want)
		}
	}
}

func (s *scriptedCoordinatorService) Handle(index int) *scriptedHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	if handle := s.handles[index]; handle != nil {
		return handle
	}
	return &scriptedHandle{}
}

func (s *scriptedCoordinatorService) AcquireOrder() []int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]int(nil), s.acquireOrder...)
}

func (s *scriptedCoordinatorService) AcquireDeadlines() map[int]time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[int]time.Time, len(s.acquireDeadlines))
	for index, deadline := range s.acquireDeadlines {
		result[index] = deadline
	}
	return result
}

func (s *scriptedCoordinatorService) HandleRunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handleRuns
}

func (s *scriptedCoordinatorService) RunHandleForParty(partyID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runHandlesByParty[partyID]
}

func (s *scriptedCoordinatorService) BeginCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.beginCount
}

func (s *scriptedCoordinatorService) FinishCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finishCount
}

func (s *scriptedCoordinatorService) ActiveRunsAtFinish() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.activeRunsAtFinish
}
