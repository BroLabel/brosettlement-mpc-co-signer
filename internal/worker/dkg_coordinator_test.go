package worker

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/localrouter"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	ecdsakeygen "github.com/bnb-chain/tss-lib/ecdsa/keygen"
)

const (
	coordinatorPlatformParty = "mpc-signer"
	coordinatorPrimaryParty  = "co-signer-primary"
	coordinatorRecoveryParty = "co-signer-recovery"
	coordinatorKeyID         = "mpc_key_123e4567-e89b-42d3-a456-426614174000"
)

func TestDKGCoordinatorRunsTwoPartiesConcurrentlyThroughOneRunner(t *testing.T) {
	intent := coordinatorIntent(t)
	runner := newConcurrentDKGRunner()
	primary := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)}
	recovery := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)}
	activePair := sharestore.NewActivePair()

	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		activePair,
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatalf("NewDKGCoordinator() error = %v", err)
	}

	result, err := coordinator.Run(context.Background(), intent, &blockingTransport{})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if result.Primary.PartyID != coordinatorPrimaryParty || result.Recovery.PartyID != coordinatorRecoveryParty {
		t.Fatalf("unexpected party evidence: %+v", result)
	}

	requests := runner.Requests()
	if len(requests) != 2 {
		t.Fatalf("RunDKGSession() calls = %d, want 2", len(requests))
	}
	byParty := make(map[string]coretss.DKGSessionRequest, len(requests))
	for _, request := range requests {
		byParty[request.LocalPartyID] = request
		if request.Session.SessionID != intent.SessionID {
			t.Fatalf("session ID = %q, want %q", request.Session.SessionID, intent.SessionID)
		}
		if request.Session.Threshold != 2 {
			t.Fatalf("threshold = %d, want public product threshold 2", request.Session.Threshold)
		}
		if request.Transport == nil {
			t.Fatal("party transport is nil")
		}
	}
	primaryRequest, primaryOK := byParty[coordinatorPrimaryParty]
	recoveryRequest, recoveryOK := byParty[coordinatorRecoveryParty]
	if !primaryOK || !recoveryOK {
		t.Fatalf("local parties = %v, want primary and recovery", byParty)
	}
	if primaryRequest.Transport == recoveryRequest.Transport {
		t.Fatal("B and C shared one mutable transport")
	}
	if !bytes.Equal(primaryRequest.OpaqueDescriptorFingerprint, recoveryRequest.OpaqueDescriptorFingerprint) {
		t.Fatal("B and C descriptor fingerprints differ")
	}
	if primaryRequest.DerivationMaterial == nil || recoveryRequest.DerivationMaterial == nil ||
		primaryRequest.DerivationMaterial.ChainCode != intent.Payload.ChainCode ||
		recoveryRequest.DerivationMaterial.ChainCode != intent.Payload.ChainCode {
		t.Fatal("B and C did not receive the same just-in-time chain code")
	}
	if runner.Context(coordinatorPrimaryParty) == runner.Context(coordinatorRecoveryParty) {
		t.Fatal("B and C shared one cancellation context")
	}
	for _, partyID := range []string{coordinatorPrimaryParty, coordinatorRecoveryParty} {
		deadline, ok := runner.Context(partyID).Deadline()
		if !ok || !deadline.Equal(intent.ExpiresAt) {
			t.Fatalf("%s runtime deadline = %s/%t, want %s", partyID, deadline, ok, intent.ExpiresAt)
		}
	}

	if primary.Calls() != 1 || recovery.Calls() != 1 {
		t.Fatalf("InspectExisting() calls primary=%d recovery=%d, want 1 each", primary.Calls(), recovery.Calls())
	}

	lease, err := activePair.RegisterPair(sharestore.PairRegistration{
		SessionID:       intent.SessionID,
		KeyID:           coordinatorKeyID,
		PrimaryPartyID:  coordinatorPrimaryParty,
		RecoveryPartyID: coordinatorRecoveryParty,
		DescriptorBytes: intent.Payload.DescriptorBytes,
	})
	if err != nil {
		t.Fatalf("active pair remained registered after joined runtimes: %v", err)
	}
	_ = lease.Release()
}

func TestDKGCoordinatorAcquiresTwoPreParamsHandlesAndUsesOnlyHandleAwareRuns(t *testing.T) {
	intent := coordinatorIntent(t)
	service := newHandleAwareDKGService()
	primary := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)}
	recovery := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)}

	coordinator, err := NewDKGCoordinator(
		service,
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if got := service.AcquireCount(); got != 2 {
		t.Fatalf("AcquireDKGPreParams() calls = %d, want 2", got)
	}
	if got := service.HandleRunCount(); got != 2 {
		t.Fatalf("RunDKGSessionWithPreParams() calls = %d, want 2", got)
	}
	if got := service.LegacyRunCount(); got != 0 {
		t.Fatalf("RunDKGSession() calls = %d, want 0", got)
	}
}

func TestDKGCoordinatorCancelsSiblingAndJoinsBothParties(t *testing.T) {
	intent := coordinatorIntent(t)
	runner := &failingDKGRunner{
		failedParty: coordinatorPrimaryParty,
		failure:     errors.New("primary failed"),
		exited:      make(chan string, 2),
	}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		&recordingArtifactInspector{},
		&recordingArtifactInspector{},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, runner.failure) {
		t.Fatalf("Run() error = %v, want primary failure", err)
	}

	exited := map[string]bool{}
	for i := 0; i < 2; i++ {
		select {
		case party := <-runner.exited:
			exited[party] = true
		case <-time.After(time.Second):
			t.Fatal("Run returned before both party runtimes exited")
		}
	}
	if !exited[coordinatorPrimaryParty] || !exited[coordinatorRecoveryParty] {
		t.Fatalf("exited parties = %v", exited)
	}
}

func TestDKGCoordinatorCancelsAndJoinsBothPartiesOnNetworkIntegrityFailure(t *testing.T) {
	intent := coordinatorIntent(t)
	runner := newCancellationRecordingRunner()
	primary := &recordingArtifactInspector{}
	recovery := &recordingArtifactInspector{}
	network := newGatedNetworkTransport(
		protocol.Frame{
			SessionID:   intent.SessionID,
			Stage:       "dkg",
			MessageID:   "spoofed-network-frame",
			Seq:         1,
			Round:       1,
			Protocol:    "ECDSA",
			MessageType: "KGRound1Message",
			FromParty:   coordinatorRecoveryParty,
			ToParty:     coordinatorPrimaryParty,
			Payload:     []byte("spoofed"),
		},
		nil,
	)
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, runErr := coordinator.Run(ctx, intent, network)
		result <- runErr
	}()

	waitForSignal(t, runner.entered, "both party runners did not enter")
	close(network.release)
	waitForSignal(t, network.returned, "network transport did not return spoofed frame")

	var runErr error
	select {
	case runErr = <-result:
	case <-time.After(time.Second):
		close(runner.succeed)
		cancel()
		<-result
		t.Fatal("network integrity failure did not cancel and join both party runners")
	}
	if !errors.Is(runErr, localrouter.ErrInvalidFrame) {
		t.Fatalf("Run() error = %v, want router ErrInvalidFrame", runErr)
	}
	if got := runner.Exited(); len(got) != 2 {
		t.Fatalf("joined party runners = %v, want both parties", got)
	}
	if got := runner.Canceled(); len(got) != 2 {
		t.Fatalf("canceled party contexts = %v, want both parties", got)
	}
	if primary.Calls() != 0 || recovery.Calls() != 0 {
		t.Fatalf("InspectExisting() calls primary=%d recovery=%d, want zero after integrity failure", primary.Calls(), recovery.Calls())
	}
}

func TestDKGCoordinatorPrefersNetworkReceiveErrorOverRacingPartySuccess(t *testing.T) {
	intent := coordinatorIntent(t)
	networkErr := errors.New("network receive failed")
	network := newGatedNetworkTransport(protocol.Frame{}, networkErr)
	runner := newRouterErrorSwallowingRunner()
	primary := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)}
	recovery := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, runErr := coordinator.Run(context.Background(), intent, network)
		result <- runErr
	}()
	waitForSignal(t, runner.entered, "both party runners did not enter")
	close(network.release)
	waitForSignal(t, runner.observed, "party runner did not observe router terminal error")

	select {
	case runErr := <-result:
		if !errors.Is(runErr, networkErr) {
			t.Fatalf("Run() error = %v, want authoritative network receive error", runErr)
		}
	case <-time.After(time.Second):
		t.Fatal("coordinator did not join racing successful party runners")
	}
	if primary.Calls() != 0 || recovery.Calls() != 0 {
		t.Fatalf("InspectExisting() calls primary=%d recovery=%d, want zero after network receive error", primary.Calls(), recovery.Calls())
	}
}

func TestDKGCoordinatorFinishCapturesReturnedNetworkErrorBeforeInspection(t *testing.T) {
	intent := coordinatorIntent(t)
	networkErr := newFinishBoundaryError("network error before finish boundary")
	network := &finishBoundaryErrorTransport{
		err:      networkErr,
		returned: make(chan struct{}),
	}
	runner := &finishBoundarySuccessRunner{
		processing: networkErr.processing,
		cleanup:    make(chan struct{}),
	}
	primary := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)}
	recovery := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, runErr := coordinator.Run(context.Background(), intent, network)
		result <- runErr
	}()
	waitForSignal(t, network.returned, "network RecvFrame did not return its error")

	select {
	case runErr := <-result:
		if !errors.Is(runErr, networkErr) {
			t.Fatalf("Run() error = %v, want error that returned before finish boundary", runErr)
		}
	case <-time.After(time.Second):
		close(runner.cleanup)
		<-result
		t.Fatal("coordinator did not establish and join the normal finish boundary")
	}
	if primary.Calls() != 0 || recovery.Calls() != 0 {
		t.Fatalf("InspectExisting() calls primary=%d recovery=%d, want zero after pre-finish network error", primary.Calls(), recovery.Calls())
	}
}

func TestDKGCoordinatorNormalFinishJoinsReceiverBeforeInspection(t *testing.T) {
	intent := coordinatorIntent(t)
	network := newJoiningNetworkTransport()
	runner := newConcurrentDKGRunner()
	runner.onRun = func() {
		<-network.started
	}
	receiverStillRunning := errors.New("artifact inspection started before receiver joined")
	checkJoined := func() error {
		select {
		case <-network.exited:
			return nil
		default:
			return receiverStillRunning
		}
	}
	primary := &recordingArtifactInspector{
		evidence: coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary),
		before:   checkJoined,
	}
	recovery := &recordingArtifactInspector{
		evidence: coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery),
		before:   checkJoined,
	}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, network); err != nil {
		t.Fatalf("Run() error = %v, want normal receiver finish before inspection", err)
	}
	select {
	case <-network.exited:
	default:
		t.Fatal("Run returned while network receiver was still active")
	}
}

func TestDKGCoordinatorRejectsPostRuntimeEvidenceMismatch(t *testing.T) {
	intent := coordinatorIntent(t)
	runner := newConcurrentDKGRunner()
	primaryEvidence := coordinatorEvidence(t, intent, coordinatorPrimaryParty, sharestore.StorePurposePrimary)
	recoveryEvidence := coordinatorEvidence(t, intent, coordinatorRecoveryParty, sharestore.StorePurposeRecovery)
	recoveryEvidence.AccountPublicKey = []byte{0x03, 0x01}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		&recordingArtifactInspector{evidence: primaryEvidence},
		&recordingArtifactInspector{evidence: recoveryEvidence},
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
		t.Fatalf("Run() error = %v, want ErrDKGEvidenceMismatch", err)
	}
}

func TestDKGCoordinatorKeepsIndependentDescriptorCopyForReadback(t *testing.T) {
	intent := coordinatorIntent(t)
	originalDescriptor := append([]byte(nil), intent.Payload.DescriptorBytes...)
	runner := newConcurrentDKGRunner()
	runner.onRun = func() {
		intent.Payload.DescriptorBytes[0] ^= 0xff
	}
	primary := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intentWithDescriptor(intent, originalDescriptor), coordinatorPrimaryParty, sharestore.StorePurposePrimary)}
	recovery := &recordingArtifactInspector{evidence: coordinatorEvidence(t, intentWithDescriptor(intent, originalDescriptor), coordinatorRecoveryParty, sharestore.StorePurposeRecovery)}
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		primary,
		recovery,
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !bytes.Equal(primary.Expected().DescriptorBytes, originalDescriptor) ||
		!bytes.Equal(recovery.Expected().DescriptorBytes, originalDescriptor) {
		t.Fatal("post-runtime inspection used caller-mutated descriptor bytes")
	}
}

func TestDKGCoordinatorRejectsCallerSuppliedLibraryThresholdOne(t *testing.T) {
	intent := coordinatorIntent(t)
	intent.Payload.DescriptorBytes = bytes.Replace(
		intent.Payload.DescriptorBytes,
		[]byte(`"threshold":2`),
		[]byte(`"threshold":1`),
		1,
	)
	service := newScriptedCoordinatorService()
	coordinator, err := NewDKGCoordinator(
		service,
		sharestore.NewActivePair(),
		&recordingArtifactInspector{},
		&recordingArtifactInspector{},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), intent, &blockingTransport{}); !errors.Is(err, ErrInvalidDKGContext) {
		t.Fatalf("Run() error = %v, want ErrInvalidDKGContext", err)
	}
	if got := len(service.AcquireOrder()); got != 0 {
		t.Fatalf("preparams acquisitions after mismatched descriptor = %d, want 0", got)
	}
	if service.BeginCount() != 0 {
		t.Fatal("preparams refill paused before descriptor validation boundary")
	}
}

func TestDKGCoordinatorRejectsClaimKeyThatConflictsWithExactDescriptor(t *testing.T) {
	validIntent := coordinatorIntent(t)
	claim := monolith.ClaimResult{
		IntentID:              validIntent.IntentID,
		SessionID:             validIntent.SessionID,
		Status:                "CLAIMED",
		Deadline:              time.Now().Add(time.Minute),
		OrgID:                 validIntent.Payload.OrgID,
		KeyID:                 "mpc_key_123e4567-e89b-42d3-a456-426614174099",
		DescriptorBytes:       validIntent.Payload.DescriptorBytes,
		DescriptorFingerprint: validIntent.Payload.DescriptorFingerprint,
		ChainCode:             bytes.Repeat([]byte{0}, 32),
	}
	runner := newConcurrentDKGRunner()
	coordinator, err := NewDKGCoordinator(
		serviceForRunner(runner),
		sharestore.NewActivePair(),
		&recordingArtifactInspector{},
		&recordingArtifactInspector{},
		DKGCoordinatorConfig{
			PlatformPartyID: coordinatorPlatformParty,
			PrimaryPartyID:  coordinatorPrimaryParty,
			RecoveryPartyID: coordinatorRecoveryParty,
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := coordinator.Run(context.Background(), claim.Intent(), &blockingTransport{}); !errors.Is(err, ErrInvalidDKGContext) {
		t.Fatalf("Run() error = %v, want ErrInvalidDKGContext", err)
	}
	if got := len(runner.Requests()); got != 0 {
		t.Fatalf("RunDKGSession() calls = %d, want 0", got)
	}
}

type concurrentDKGRunner struct {
	mu        sync.Mutex
	requests  []coretss.DKGSessionRequest
	contexts  map[string]context.Context
	entered   chan struct{}
	release   chan struct{}
	once      sync.Once
	onRun     func()
	onRunOnce sync.Once
}

type testSessionRunner interface {
	RunDKGSession(context.Context, coretss.DKGSessionRequest) (coretss.DKGOutput, error)
}

type runnerBackedDKGService struct {
	runner testSessionRunner
	owner  *coretss.Service
}

func serviceForRunner(runner testSessionRunner) *runnerBackedDKGService {
	return &runnerBackedDKGService{
		runner: runner,
		owner:  coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(workerPreParamsSource{})),
	}
}

func (*runnerBackedDKGService) BeginJob(context.Context) (func(), error) {
	return func() {}, nil
}

func (s *runnerBackedDKGService) AcquireDKGPreParams(ctx context.Context) (coretss.DKGPreParamsHandle, error) {
	return s.owner.AcquireDKGPreParams(ctx)
}

func (s *runnerBackedDKGService) RunDKGSessionWithPreParams(
	ctx context.Context,
	request coretss.DKGSessionRequest,
	_ coretss.DKGPreParamsHandle,
) (coretss.DKGOutput, error) {
	return s.runner.RunDKGSession(ctx, request)
}

type handleAwareDKGService struct {
	*concurrentDKGRunner
	owner *coretss.Service

	mu               sync.Mutex
	acquireCount     int
	handleRunCount   int
	legacyRunCount   int
	acquisitionOrder []int
}

func newHandleAwareDKGService() *handleAwareDKGService {
	return &handleAwareDKGService{
		concurrentDKGRunner: newConcurrentDKGRunner(),
		owner:               coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(workerPreParamsSource{})),
	}
}

func (*handleAwareDKGService) BeginJob(context.Context) (func(), error) {
	return func() {}, nil
}

func (s *handleAwareDKGService) AcquireDKGPreParams(ctx context.Context) (coretss.DKGPreParamsHandle, error) {
	s.mu.Lock()
	s.acquireCount++
	s.acquisitionOrder = append(s.acquisitionOrder, s.acquireCount)
	s.mu.Unlock()
	return s.owner.AcquireDKGPreParams(ctx)
}

func (s *handleAwareDKGService) RunDKGSessionWithPreParams(
	ctx context.Context,
	request coretss.DKGSessionRequest,
	_ coretss.DKGPreParamsHandle,
) (coretss.DKGOutput, error) {
	s.mu.Lock()
	s.handleRunCount++
	s.mu.Unlock()
	return s.concurrentDKGRunner.RunDKGSession(ctx, request)
}

func (s *handleAwareDKGService) RunDKGSession(
	ctx context.Context,
	request coretss.DKGSessionRequest,
) (coretss.DKGOutput, error) {
	s.mu.Lock()
	s.legacyRunCount++
	s.mu.Unlock()
	return s.concurrentDKGRunner.RunDKGSession(ctx, request)
}

func (s *handleAwareDKGService) AcquireCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.acquireCount
}

func (s *handleAwareDKGService) HandleRunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.handleRunCount
}

func (s *handleAwareDKGService) LegacyRunCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.legacyRunCount
}

type workerPreParamsSource struct{}

func (workerPreParamsSource) Acquire(context.Context) (*ecdsakeygen.LocalPreParams, error) {
	return &ecdsakeygen.LocalPreParams{}, nil
}

func newConcurrentDKGRunner() *concurrentDKGRunner {
	return &concurrentDKGRunner{
		contexts: make(map[string]context.Context),
		entered:  make(chan struct{}, 2),
		release:  make(chan struct{}),
	}
}

func (r *concurrentDKGRunner) RunDKGSession(ctx context.Context, request coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	r.onRunOnce.Do(func() {
		if r.onRun != nil {
			r.onRun()
		}
	})
	r.mu.Lock()
	r.requests = append(r.requests, request)
	r.contexts[request.LocalPartyID] = ctx
	r.mu.Unlock()
	r.entered <- struct{}{}
	if len(r.entered) == cap(r.entered) {
		r.once.Do(func() { close(r.release) })
	}
	select {
	case <-r.release:
		return coretss.DKGOutput{KeyID: request.Session.KeyID}, nil
	case <-ctx.Done():
		return coretss.DKGOutput{}, ctx.Err()
	}
}

func (r *concurrentDKGRunner) Requests() []coretss.DKGSessionRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]coretss.DKGSessionRequest(nil), r.requests...)
}

func (r *concurrentDKGRunner) Context(partyID string) context.Context {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.contexts[partyID]
}

type failingDKGRunner struct {
	failedParty string
	failure     error
	exited      chan string
}

func (r *failingDKGRunner) RunDKGSession(ctx context.Context, request coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	defer func() { r.exited <- request.LocalPartyID }()
	if request.LocalPartyID == r.failedParty {
		return coretss.DKGOutput{}, r.failure
	}
	<-ctx.Done()
	return coretss.DKGOutput{}, ctx.Err()
}

type cancellationRecordingRunner struct {
	mu       sync.Mutex
	count    int
	entered  chan struct{}
	succeed  chan struct{}
	exited   []string
	canceled []string
}

func newCancellationRecordingRunner() *cancellationRecordingRunner {
	return &cancellationRecordingRunner{
		entered: make(chan struct{}),
		succeed: make(chan struct{}),
	}
}

func (r *cancellationRecordingRunner) RunDKGSession(ctx context.Context, request coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	r.mu.Lock()
	r.count++
	if r.count == 2 {
		close(r.entered)
	}
	r.mu.Unlock()

	var runErr error
	select {
	case <-ctx.Done():
		runErr = ctx.Err()
		r.mu.Lock()
		r.canceled = append(r.canceled, request.LocalPartyID)
		r.mu.Unlock()
	case <-r.succeed:
	}
	r.mu.Lock()
	r.exited = append(r.exited, request.LocalPartyID)
	r.mu.Unlock()
	return coretss.DKGOutput{KeyID: request.Session.KeyID}, runErr
}

func (r *cancellationRecordingRunner) Exited() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.exited...)
}

func (r *cancellationRecordingRunner) Canceled() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.canceled...)
}

type routerErrorSwallowingRunner struct {
	mu       sync.Mutex
	count    int
	entered  chan struct{}
	observed chan struct{}
}

func newRouterErrorSwallowingRunner() *routerErrorSwallowingRunner {
	return &routerErrorSwallowingRunner{
		entered:  make(chan struct{}),
		observed: make(chan struct{}),
	}
}

func (r *routerErrorSwallowingRunner) RunDKGSession(ctx context.Context, request coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	r.mu.Lock()
	r.count++
	if r.count == 2 {
		close(r.entered)
	}
	r.mu.Unlock()

	if request.LocalPartyID == coordinatorPrimaryParty {
		_, _ = request.Transport.RecvFrame(ctx)
		close(r.observed)
	} else {
		select {
		case <-r.observed:
		case <-ctx.Done():
		}
	}
	return coretss.DKGOutput{KeyID: request.Session.KeyID}, nil
}

type finishBoundarySuccessRunner struct {
	processing <-chan struct{}
	cleanup    chan struct{}
}

func (r *finishBoundarySuccessRunner) RunDKGSession(_ context.Context, request coretss.DKGSessionRequest) (coretss.DKGOutput, error) {
	select {
	case <-r.processing:
	case <-r.cleanup:
	}
	return coretss.DKGOutput{KeyID: request.Session.KeyID}, nil
}

type recordingArtifactInspector struct {
	mu       sync.Mutex
	evidence sharestore.ArtifactEvidence
	err      error
	before   func() error
	calls    int
	expected sharestore.ExpectedArtifactContext
}

func (i *recordingArtifactInspector) InspectExisting(_ context.Context, expected sharestore.ExpectedArtifactContext) (sharestore.ArtifactEvidence, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.calls++
	i.expected = expected
	i.expected.DescriptorBytes = append([]byte(nil), expected.DescriptorBytes...)
	if i.before != nil {
		if err := i.before(); err != nil {
			return sharestore.ArtifactEvidence{}, err
		}
	}
	return i.evidence, i.err
}

func (i *recordingArtifactInspector) Calls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.calls
}

func (i *recordingArtifactInspector) Expected() sharestore.ExpectedArtifactContext {
	i.mu.Lock()
	defer i.mu.Unlock()
	expected := i.expected
	expected.DescriptorBytes = append([]byte(nil), i.expected.DescriptorBytes...)
	return expected
}

type blockingTransport struct{}

func (*blockingTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (*blockingTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	<-ctx.Done()
	return protocol.Frame{}, ctx.Err()
}

type gatedNetworkTransport struct {
	frame    protocol.Frame
	err      error
	release  chan struct{}
	returned chan struct{}
}

type finishBoundaryError struct {
	message    string
	processing chan struct{}
	ctx        context.Context
	once       sync.Once
}

func newFinishBoundaryError(message string) *finishBoundaryError {
	return &finishBoundaryError{
		message:    message,
		processing: make(chan struct{}),
	}
}

func (e *finishBoundaryError) Error() string {
	return e.message
}

func (e *finishBoundaryError) Is(error) bool {
	e.once.Do(func() { close(e.processing) })
	<-e.ctx.Done()
	return false
}

type finishBoundaryErrorTransport struct {
	err      *finishBoundaryError
	returned chan struct{}
}

func (*finishBoundaryErrorTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (t *finishBoundaryErrorTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	t.err.ctx = ctx
	close(t.returned)
	return protocol.Frame{}, t.err
}

type joiningNetworkTransport struct {
	started chan struct{}
	exited  chan struct{}
}

func newJoiningNetworkTransport() *joiningNetworkTransport {
	return &joiningNetworkTransport{
		started: make(chan struct{}),
		exited:  make(chan struct{}),
	}
}

func (*joiningNetworkTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (t *joiningNetworkTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	close(t.started)
	<-ctx.Done()
	close(t.exited)
	return protocol.Frame{}, ctx.Err()
}

func newGatedNetworkTransport(frame protocol.Frame, err error) *gatedNetworkTransport {
	return &gatedNetworkTransport{
		frame:    frame,
		err:      err,
		release:  make(chan struct{}),
		returned: make(chan struct{}),
	}
}

func (*gatedNetworkTransport) SendFrame(context.Context, protocol.Frame) error { return nil }

func (t *gatedNetworkTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	select {
	case <-t.release:
		close(t.returned)
		return t.frame, t.err
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(time.Second):
		t.Fatal(message)
	}
}

func coordinatorIntent(t *testing.T) monolith.Intent {
	t.Helper()
	chainCode := bytes.Repeat([]byte{0}, 32)
	chainCodeHash := mpc2of3.ChainCodeHashFor(chainCode).String()
	descriptorBytes := []byte(`{"algorithm":"ECDSA","chainCodeHash":"` + chainCodeHash + `","curve":"secp256k1","derivationScheme":"bip32_secp256k1","descriptorKind":"mpc-key-descriptor","descriptorVersion":1,"keyId":"` + coordinatorKeyID + `","parties":[{"partyId":"mpc-signer","purpose":"platform"},{"partyId":"co-signer-primary","purpose":"primary"},{"partyId":"co-signer-recovery","purpose":"recovery"}],"protocolVersion":1,"publicKeyFormat":"compressed_sec1","threshold":2}`)
	if _, _, err := mpc2of3.ParseCanonicalDescriptor(descriptorBytes); err != nil {
		t.Fatal(err)
	}
	fingerprint := mpc2of3.DescriptorFingerprintFor(descriptorBytes)
	return monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "DKG",
		ExpiresAt: time.Now().Add(time.Minute),
		Payload: monolith.IntentPayload{
			OrgID:                 "org-1",
			KeyID:                 coordinatorKeyID,
			DescriptorBytes:       descriptorBytes,
			DescriptorFingerprint: fingerprint.String(),
			ChainCode:             strings.Repeat("00", 32),
			ChainCodeHash:         chainCodeHash,
		},
	}
}

func coordinatorEvidence(t *testing.T, intent monolith.Intent, partyID string, purpose sharestore.StorePurpose) sharestore.ArtifactEvidence {
	t.Helper()
	_, fingerprint, err := mpc2of3.ParseCanonicalDescriptor(intent.Payload.DescriptorBytes)
	if err != nil {
		t.Fatal(err)
	}
	return sharestore.ArtifactEvidence{
		SessionID:             intent.SessionID,
		KeyID:                 coordinatorKeyID,
		PartyID:               partyID,
		Purpose:               purpose,
		DescriptorFingerprint: fingerprint,
		AccountPublicKey:      []byte{0x02, 0x01},
		ChainCodeHash:         mpc2of3.ChainCodeHashFor(bytes.Repeat([]byte{0}, 32)),
		CodecVersion:          2,
	}
}

func intentWithDescriptor(intent monolith.Intent, descriptor []byte) monolith.Intent {
	intent.Payload.DescriptorBytes = append([]byte(nil), descriptor...)
	return intent
}
