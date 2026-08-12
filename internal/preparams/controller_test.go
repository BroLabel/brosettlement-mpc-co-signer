package preparams

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	ecdsakeygen "github.com/bnb-chain/tss-lib/ecdsa/keygen"
)

func TestProductionPreParamsProfileIsStaticAndUsesExplicitParallelism(t *testing.T) {
	profile, err := ProductionProfile(1)
	if err != nil {
		t.Fatalf("ProductionProfile() error = %v", err)
	}
	if profile.TargetSize != 2 {
		t.Fatalf("TargetSize = %d, want 2", profile.TargetSize)
	}
	if profile.MaxConcurrency != 1 {
		t.Fatalf("MaxConcurrency = %d, want one generation worker", profile.MaxConcurrency)
	}
	if profile.GenerationParallelism != 1 {
		t.Fatalf("GenerationParallelism = %d, want explicit 1", profile.GenerationParallelism)
	}
	if profile.SyncFallbackOnEmpty {
		t.Fatal("SyncFallbackOnEmpty = true, want false")
	}
	if profile.AutoRefillOnAcquire {
		t.Fatal("AutoRefillOnAcquire = true, want false")
	}

	if _, err := ProductionProfile(0); err == nil {
		t.Fatal("ProductionProfile(0) error = nil, want invalid parallelism")
	}
}

func TestPreParamsControllerExportsCoreTransitionCounters(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	service := &recordingCoreService{snapshot: coretss.Snapshot{
		PreParamsAcquiredCount:             5,
		PreParamsConsumedCount:             4,
		PreParamsDiscardedBeforeStartCount: 3,
		PreParamsAcquireFailedCount:        2,
		PreParamsConsumeConflictCount:      1,
	}}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}

	controller.AdmissionHint()

	got := metrics.Default.Snapshot()
	want := map[string]float64{
		"preparams_acquired_total":               5,
		"preparams_consumed_total":               4,
		"preparams_discarded_before_start_total": 3,
		"preparams_acquire_failed_total":         2,
		"preparams_consume_conflict_total":       1,
	}
	for name, value := range want {
		if got[name][""] != value {
			t.Fatalf("%s = %v, want %v", name, got[name][""], value)
		}
	}
}

func TestPreparamsMetricsCountConsumedBeforeRuntimeFailureAndConflictsOnce(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	owner := coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(staticPreParamsSource{}))
	coreHandle, err := owner.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	service := &recordingCoreService{acquired: coreHandle, runErr: errors.New("runtime failure after consume")}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := controller.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RunDKGSessionWithPreParams(context.Background(), validDKGRequest(), handle); err == nil {
		t.Fatal("runtime failure was lost")
	}
	if _, err := controller.RunDKGSessionWithPreParams(context.Background(), validDKGRequest(), handle); !errors.Is(err, coretss.ErrPreParamsConsumed) {
		t.Fatalf("repeat run=%v", err)
	}
	got := metrics.Default.Snapshot()
	if got["preparams_consumed_total"][""] != 1 || got["preparams_consume_conflict_total"][""] != 1 {
		t.Fatalf("transitions=%#v", got)
	}
}

func TestPreparamsMetricsCountDiscardOnlyOnceUnderConcurrentRepeats(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	owner := coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(staticPreParamsSource{}))
	controller, err := NewController(owner)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := controller.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); _ = handle.Discard() }()
	}
	wg.Wait()
	controller.AdmissionHint()
	got := metrics.Default.Snapshot()
	if got["preparams_discarded_before_start_total"][""] != 1 || got["preparams_consume_conflict_total"][""] != 0 {
		t.Fatalf("transitions=%#v", got)
	}
}

func TestInvalidPreparamsRequestLeavesHandleAcquiredForDiscard(t *testing.T) {
	owner := coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(staticPreParamsSource{}))
	controller, err := NewController(owner)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := controller.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := controller.RunDKGSessionWithPreParams(context.Background(), coretss.DKGSessionRequest{}, handle); err == nil {
		t.Fatal("invalid request accepted")
	}
	if err := handle.Discard(); err != nil {
		t.Fatalf("discard after invalid request: %v", err)
	}
	if err := handle.Discard(); err != nil {
		t.Fatalf("idempotent discard: %v", err)
	}
}

func TestPreParamsControllerAdmissionHintAndWakeupUseInventoryAndGenerationState(t *testing.T) {
	service := &recordingCoreService{
		snapshot: coretss.Snapshot{PreParamsPoolSize: 1},
	}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}

	if controller.AdmissionHint() {
		t.Fatal("AdmissionHint() = true with inventory below two")
	}
	assertNoWakeup(t, controller.Wakeups())

	service.SetSnapshot(coretss.Snapshot{
		PreParamsPoolSize:           2,
		PreParamsGenerationInFlight: 1,
	})
	if controller.AdmissionHint() {
		t.Fatal("AdmissionHint() = true while generation is in flight")
	}
	assertNoWakeup(t, controller.Wakeups())

	service.SetSnapshot(coretss.Snapshot{PreParamsPoolSize: 2})
	if !controller.AdmissionHint() {
		t.Fatal("AdmissionHint() = false with two handles and no generation")
	}
	assertWakeup(t, controller.Wakeups())

	if !controller.AdmissionHint() {
		t.Fatal("repeated AdmissionHint() changed ready state")
	}
	assertNoWakeup(t, controller.Wakeups())

	service.SetSnapshot(coretss.Snapshot{PreParamsPoolSize: 1})
	if controller.AdmissionHint() {
		t.Fatal("AdmissionHint() remained true after inventory fell")
	}
	service.SetSnapshot(coretss.Snapshot{PreParamsPoolSize: 2})
	if !controller.AdmissionHint() {
		t.Fatal("AdmissionHint() did not observe readiness restoration")
	}
	assertWakeup(t, controller.Wakeups())
}

func TestPreParamsControllerRunWakesSchedulerWhenAsyncRefillBecomesReady(t *testing.T) {
	service := &recordingCoreService{snapshot: coretss.Snapshot{PreParamsPoolSize: 1}}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go controller.Run(ctx)

	service.SetSnapshot(coretss.Snapshot{PreParamsPoolSize: 2})
	assertWakeup(t, controller.Wakeups())
}

func TestPreParamsControllerPausesOnceAndResumesAsynchronouslyAfterJob(t *testing.T) {
	service := &recordingCoreService{}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}

	finish, err := controller.BeginJob(context.Background())
	if err != nil {
		t.Fatalf("BeginJob() error = %v", err)
	}
	if service.PauseCount() != 1 || service.ResumeCount() != 0 {
		t.Fatalf("refill controls after begin = pause:%d resume:%d, want 1:0", service.PauseCount(), service.ResumeCount())
	}
	if _, err := controller.BeginJob(context.Background()); !errors.Is(err, ErrJobAlreadyActive) {
		t.Fatalf("second BeginJob() error = %v, want ErrJobAlreadyActive", err)
	}

	finish()
	finish()
	if service.PauseCount() != 1 || service.ResumeCount() != 1 {
		t.Fatalf("refill controls after finish = pause:%d resume:%d, want 1:1", service.PauseCount(), service.ResumeCount())
	}
}

func TestPreParamsControllerWaitsForInFlightGenerationBeforeJob(t *testing.T) {
	service := &recordingCoreService{
		snapshot:    coretss.Snapshot{PreParamsPoolSize: 2, PreParamsGenerationInFlight: 1},
		pauseSignal: make(chan struct{}),
	}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}

	type beginResult struct {
		finish func()
		err    error
	}
	result := make(chan beginResult, 1)
	go func() {
		finish, beginErr := controller.BeginJob(context.Background())
		result <- beginResult{finish: finish, err: beginErr}
	}()
	<-service.pauseSignal

	select {
	case got := <-result:
		t.Fatalf("BeginJob returned while generation was in flight: %v", got.err)
	default:
	}
	service.SetSnapshot(coretss.Snapshot{PreParamsPoolSize: 2})

	select {
	case got := <-result:
		if got.err != nil {
			t.Fatalf("BeginJob() error = %v", got.err)
		}
		got.finish()
	case <-time.After(time.Second):
		t.Fatal("BeginJob did not proceed after generation became idle")
	}
}

func TestPreParamsControllerKeepsCoreHandleOpaqueUntilRunOrDiscard(t *testing.T) {
	source := staticPreParamsSource{}
	owner := coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(source))
	coreHandle, err := owner.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatalf("AcquireDKGPreParams() setup error = %v", err)
	}
	service := &recordingCoreService{acquired: coreHandle}
	controller, err := NewController(service)
	if err != nil {
		t.Fatal(err)
	}

	handle, err := controller.AcquireDKGPreParams(context.Background())
	if err != nil {
		t.Fatalf("controller AcquireDKGPreParams() error = %v", err)
	}
	if handle == nil {
		t.Fatal("controller returned nil opaque handle")
	}
	request := validDKGRequest()
	if _, err := controller.RunDKGSessionWithPreParams(context.Background(), request, handle); err != nil {
		t.Fatalf("RunDKGSessionWithPreParams() error = %v", err)
	}
	if service.RunHandle() != coreHandle {
		t.Fatal("controller did not return the same service-bound core handle at consume")
	}

	otherOwner := coretss.NewBnbService(slog.Default(), coretss.WithPreParamsSource(staticPreParamsSource{}))
	otherController, err := NewController(otherOwner)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := otherController.RunDKGSessionWithPreParams(context.Background(), request, handle); !errors.Is(err, coretss.ErrForeignPreParamsHandle) {
		t.Fatalf("foreign service run error = %v, want ErrForeignPreParamsHandle", err)
	}
}

type staticPreParamsSource struct{}

type validatingTransport struct{}

func (validatingTransport) SendFrame(context.Context, protocol.Frame) error { return nil }
func (validatingTransport) RecvFrame(context.Context) (protocol.Frame, error) {
	return protocol.Frame{}, context.Canceled
}

func validDKGRequest() coretss.DKGSessionRequest {
	return coretss.DKGSessionRequest{Session: coretss.DKGSessionDescriptor{SessionID: "dkg-1", OrgID: "org-1", Parties: []string{"a", "b"}, Threshold: 2, Algorithm: "TEST"}, LocalPartyID: "b", Transport: validatingTransport{}}
}

func (staticPreParamsSource) Acquire(context.Context) (*ecdsakeygen.LocalPreParams, error) {
	return &ecdsakeygen.LocalPreParams{}, nil
}

type recordingCoreService struct {
	mu          sync.Mutex
	snapshot    coretss.Snapshot
	acquired    coretss.DKGPreParamsHandle
	run         coretss.DKGPreParamsHandle
	runErr      error
	pauses      int
	resumes     int
	pauseSignal chan struct{}
	pauseOnce   sync.Once
	consumed    map[coretss.DKGPreParamsHandle]struct{}
}

func (s *recordingCoreService) AcquireDKGPreParams(context.Context) (coretss.DKGPreParamsHandle, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.acquired != nil {
		s.snapshot.PreParamsAcquiredCount++
	}
	return s.acquired, nil
}

func (s *recordingCoreService) RunDKGSessionWithPreParams(
	_ context.Context,
	request coretss.DKGSessionRequest,
	handle coretss.DKGPreParamsHandle,
) (coretss.DKGOutput, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.run = handle
	if s.consumed == nil {
		s.consumed = make(map[coretss.DKGPreParamsHandle]struct{})
	}
	if _, exists := s.consumed[handle]; exists {
		s.snapshot.PreParamsConsumeConflictCount++
		return coretss.DKGOutput{}, coretss.ErrPreParamsConsumed
	}
	s.consumed[handle] = struct{}{}
	s.snapshot.PreParamsConsumedCount++
	if s.runErr != nil {
		return coretss.DKGOutput{}, s.runErr
	}
	return coretss.DKGOutput{KeyID: request.Session.KeyID}, nil
}

func (s *recordingCoreService) PausePreParamsRefill() {
	s.mu.Lock()
	s.pauses++
	signal := s.pauseSignal
	s.mu.Unlock()
	if signal != nil {
		s.pauseOnce.Do(func() { close(signal) })
	}
}

func (s *recordingCoreService) ResumePreParamsRefill() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumes++
}

func (s *recordingCoreService) Snapshot() coretss.Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot
}

func (s *recordingCoreService) SetSnapshot(snapshot coretss.Snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshot = snapshot
}

func (s *recordingCoreService) PauseCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pauses
}

func (s *recordingCoreService) ResumeCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.resumes
}

func (s *recordingCoreService) RunHandle() coretss.DKGPreParamsHandle {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run
}

func assertWakeup(t *testing.T, wakeups <-chan struct{}) {
	t.Helper()
	select {
	case <-wakeups:
	case <-time.After(time.Second):
		t.Fatal("controller did not emit readiness wakeup")
	}
}

func assertNoWakeup(t *testing.T, wakeups <-chan struct{}) {
	t.Helper()
	select {
	case <-wakeups:
		t.Fatal("controller emitted unexpected readiness wakeup")
	default:
	}
}
