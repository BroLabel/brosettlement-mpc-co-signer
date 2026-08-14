package worker

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/metrics"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type notifyingSignRunner struct{ started chan struct{} }

func (r *notifyingSignRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	select {
	case r.started <- struct{}{}:
	default:
	}
	return nil
}

type stubPendingClient struct {
	mu          sync.Mutex
	intents     []monolith.Intent
	claimCalls  []string
	claimResult monolith.ClaimResult
	claimErr    error
	claimErrors map[string]error
	pollCalls   int
}

func TestSchedulerPublishesOldestPendingAgeFromCreatedAt(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	s := newDeterministicScheduler(t, 3, func() bool { return true })
	s.cfg.Now = func() time.Time { return now }
	s.launch = captureLaunches(new([]launchedSession))
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "sign-new", Type: "SIGN", CreatedAt: now.Add(-2 * time.Minute)},
		{IntentID: "dkg-old", Type: "DKG", CreatedAt: now.Add(-7 * time.Minute)},
		{IntentID: "sign-old", Type: "SIGN", CreatedAt: now.Add(-5 * time.Minute)},
		{IntentID: "future", Type: "DKG", CreatedAt: now.Add(time.Minute)},
	})
	got := metrics.Default.Snapshot()
	if got["oldest_pending_dkg_age_seconds"][""] != 420 || got["oldest_pending_sign_age_seconds"][""] != 300 {
		t.Fatalf("pending ages = %#v", got)
	}
}

func (s *stubPendingClient) GetPendingIntents(context.Context) ([]monolith.Intent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pollCalls++
	if s.pollCalls > 1 {
		return nil, nil
	}
	return append([]monolith.Intent(nil), s.intents...), nil
}

func (s *stubPendingClient) ClaimIntent(_ context.Context, _ string, intentID string) (monolith.ClaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claimCalls = append(s.claimCalls, intentID)
	if s.claimResult.ExpiresAt.IsZero() {
		s.claimResult.ExpiresAt = time.Now().Add(time.Minute)
	}
	if err := s.claimErrors[intentID]; err != nil {
		return monolith.ClaimResult{}, err
	}
	return s.claimResult, s.claimErr
}

func (s *stubPendingClient) PostResult(context.Context, string, monolith.IntentResult) error {
	return nil
}

func (s *stubPendingClient) PostMessage(context.Context, string, monolith.OutboundFrame) error {
	return nil
}

func (s *stubPendingClient) GetMessages(context.Context, string, uint64) ([]monolith.InboundMessage, error) {
	return nil, nil
}

func (s *stubPendingClient) claims() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.claimCalls...)
}

func TestSchedulerAcquiresDKGGuardBeforeGeneralSlot(t *testing.T) {
	permits := newSchedulerPermits(1, nil)

	heldGuard := permits.dkg.tryAcquire()
	if heldGuard == nil {
		t.Fatal("failed to occupy DKG guard")
	}
	if lease := permits.tryAcquireDKG(); lease != nil {
		t.Fatal("DKG lease acquired while guard was occupied")
	}
	if got := len(permits.general.slots); got != 0 {
		t.Fatalf("general permits in use = %d, want 0 when guard acquisition fails", got)
	}
	heldGuard.release()

	heldGeneral := permits.general.tryAcquire()
	if heldGeneral == nil {
		t.Fatal("failed to occupy general slot")
	}
	if lease := permits.tryAcquireDKG(); lease != nil {
		t.Fatal("DKG lease acquired while general slot was occupied")
	}
	if got := len(permits.dkg.slots); got != 0 {
		t.Fatalf("DKG guard in use = %d, want rollback after general acquisition fails", got)
	}
	heldGeneral.release()
}

func TestSchedulerSkipsBlockedDKGAndContinuesSIGN(t *testing.T) {
	s := newDeterministicScheduler(t, 2, func() bool { return true })
	heldGuard := s.permits.dkg.tryAcquire()
	if heldGuard == nil {
		t.Fatal("failed to occupy DKG guard")
	}
	defer heldGuard.release()

	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})
	defer releaseLaunches(launched)

	if len(launched) != 1 || launched[0].intent.IntentID != "sign-1" {
		t.Fatalf("launched = %v, want only visible SIGN", launchedIntentIDs(launched))
	}
	if launched[0].permits.dkg != nil {
		t.Fatal("SIGN received a DKG guard")
	}
}

func TestSchedulerDispatchesRediscoveredOwnClaimedSignThroughClaimReplay(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	client := &stubPendingClient{claimResult: claim, claimErrors: make(map[string]error)}
	runner := &notifyingSignRunner{started: make(chan struct{}, 1)}
	scheduler := NewScheduler(client, runner, &capturingDKGExecutor{}, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, slog.Default(), 1)

	scheduler.dispatchBatch(context.Background(), []monolith.Intent{discovery})
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("rediscovered SIGN runtime did not start")
	}
	if got := client.claims(); !equalStrings(got, []string{"intent-125"}) {
		t.Fatalf("claim calls = %v, want existing claim endpoint", got)
	}
}

func TestSchedulerDoesNotLaunchDKGWithoutGeneralSlot(t *testing.T) {
	s := newDeterministicScheduler(t, 1, func() bool { return true })
	heldGeneral := s.permits.general.tryAcquire()
	if heldGeneral == nil {
		t.Fatal("failed to occupy general slot")
	}
	defer heldGeneral.release()

	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{{IntentID: "dkg-1", Type: "DKG"}})

	if len(launched) != 0 {
		t.Fatalf("launched = %v, want none", launchedIntentIDs(launched))
	}
	if got := len(s.permits.dkg.slots); got != 0 {
		t.Fatalf("DKG guard in use = %d, want rollback", got)
	}
}

func TestSchedulerClaimConflictReleasesTypedDKGLease(t *testing.T) {
	wakeups := make(chan struct{}, 1)
	permits := newSchedulerPermits(2, wakeups)
	lease := permits.tryAcquireDKG()
	if lease == nil {
		t.Fatal("failed to acquire DKG lease")
	}
	client := &stubPendingClient{claimErr: monolith.ErrAlreadyClaimed}

	runSessionWithPermits(
		context.Background(),
		monolith.Intent{IntentID: "dkg-1", Type: "DKG"},
		client,
		&stubRunner{},
		&capturingDKGExecutor{},
		nil,
		"party-1",
		time.Millisecond,
		lease,
		slog.Default(),
		nil,
	)
	lease.Release()

	if got := len(permits.general.slots); got != 0 {
		t.Fatalf("general permits in use = %d, want 0", got)
	}
	if got := len(permits.dkg.slots); got != 0 {
		t.Fatalf("DKG permits in use = %d, want 0", got)
	}
	select {
	case <-wakeups:
	default:
		t.Fatal("claim conflict release did not wake scheduler")
	}
}

func TestSchedulerClaimConflictContinuesSIGNFromSameBatchAtCapacityOne(t *testing.T) {
	client := &stubPendingClient{
		claimErrors: map[string]error{"dkg-1": monolith.ErrAlreadyClaimed},
	}
	s := NewScheduler(
		client,
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			ProvisioningHint:  func() bool { return true },
			TerminalPublisher: acceptingTerminalPublisher(nil),
		},
		slog.Default(),
		1,
	)
	s.SetDKGAdmissionOpen(true)

	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})

	deadline := time.Now().Add(time.Second)
	for len(client.claims()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got, want := client.claims(), []string{"dkg-1", "sign-1"}; !equalStrings(got, want) {
		t.Fatalf("claim calls = %v, want same-batch continuation %v", got, want)
	}
}

func TestSchedulerAttemptsAtMostOneDKGPerBatchAndContinuesSIGN(t *testing.T) {
	s := newDeterministicScheduler(t, 3, func() bool { return true })
	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "dkg-2", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})
	defer releaseLaunches(launched)

	if got, want := launchedIntentIDs(launched), []string{"dkg-1", "sign-1"}; !equalStrings(got, want) {
		t.Fatalf("launched = %v, want %v", got, want)
	}
	if launched[0].permits.dkg == nil {
		t.Fatal("DKG job did not own the DKG guard")
	}
	if launched[1].permits.dkg != nil {
		t.Fatal("SIGN job unexpectedly owned the DKG guard")
	}
}

func TestSchedulerSkipsDKGWhenAdmissionOrProvisioningIsClosed(t *testing.T) {
	tests := []struct {
		name      string
		closeGate bool
		hint      func() bool
	}{
		{name: "dkg admission closed", closeGate: true, hint: func() bool { return true }},
		{name: "preparams or disk unavailable", hint: func() bool { return false }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newDeterministicScheduler(t, 2, tt.hint)
			if tt.closeGate {
				s.SetDKGAdmissionOpen(false)
			}
			var launched []launchedSession
			s.launch = captureLaunches(&launched)
			s.dispatchBatch(context.Background(), []monolith.Intent{
				{IntentID: "dkg-1", Type: "DKG"},
				{IntentID: "sign-1", Type: "SIGN"},
			})
			defer releaseLaunches(launched)

			if got, want := launchedIntentIDs(launched), []string{"sign-1"}; !equalStrings(got, want) {
				t.Fatalf("launched = %v, want %v", got, want)
			}
		})
	}
}

func TestSchedulerCountsSkippedPreparamsOnlyForPreparamsUnavailability(t *testing.T) {
	metrics.Default = metrics.NewRegistry()
	s := newDeterministicScheduler(t, 1, func() bool { return false })
	s.cfg.PreparamsHint = func() bool { return true }
	s.dispatchBatch(context.Background(), []monolith.Intent{{IntentID: "dkg-disk", Type: "DKG"}})
	if got := metrics.Default.Snapshot()["dkg_skipped_preparams_total"][""]; got != 0 {
		t.Fatalf("disk gate incremented preparams skip: %v", got)
	}
	s.cfg.PreparamsHint = func() bool { return false }
	s.dispatchBatch(context.Background(), []monolith.Intent{{IntentID: "dkg-preparams", Type: "DKG"}})
	if got := metrics.Default.Snapshot()["dkg_skipped_preparams_total"][""]; got != 1 {
		t.Fatalf("preparams skips=%v", got)
	}
}

func TestSchedulerStartsWithDKGAdmissionClosedUntilLifecycleReconciliation(t *testing.T) {
	s := NewScheduler(
		&stubPendingClient{},
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			ProvisioningHint:  func() bool { return true },
			TerminalPublisher: acceptingTerminalPublisher(nil),
		},
		slog.Default(),
		2,
	)
	if s.DKGAdmissionOpen() {
		t.Fatal("new scheduler opened DKG before lifecycle reconciliation")
	}

	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})
	defer releaseLaunches(launched)
	if got, want := launchedIntentIDs(launched), []string{"sign-1"}; !equalStrings(got, want) {
		t.Fatalf("launched = %v, want %v", got, want)
	}
}

func TestSchedulerAllowsDKGAndSIGNWhenGeneralCapacityExceedsOne(t *testing.T) {
	s := newDeterministicScheduler(t, 2, func() bool { return true })
	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})
	defer releaseLaunches(launched)

	if got, want := launchedIntentIDs(launched), []string{"dkg-1", "sign-1"}; !equalStrings(got, want) {
		t.Fatalf("launched = %v, want %v", got, want)
	}
}

func TestSchedulerAcceptsBackendOrderedHeadOfLineAtCapacityOne(t *testing.T) {
	s := newDeterministicScheduler(t, 1, func() bool { return true })
	var launched []launchedSession
	s.launch = captureLaunches(&launched)
	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "dkg-1", Type: "DKG"},
		{IntentID: "sign-1", Type: "SIGN"},
	})
	defer releaseLaunches(launched)

	if got, want := launchedIntentIDs(launched), []string{"dkg-1"}; !equalStrings(got, want) {
		t.Fatalf("launched = %v, want backend-ordered head item %v", got, want)
	}
}

func TestSchedulerWakeIsBoundedAndSafeForConcurrentSources(t *testing.T) {
	s := newDeterministicScheduler(t, 1, func() bool { return true })
	const callers = 64
	var wg sync.WaitGroup
	wg.Add(callers)
	for i := 0; i < callers; i++ {
		go func() {
			defer wg.Done()
			s.Wake()
		}()
	}
	wg.Wait()

	if got := len(s.repollCh); got != 1 {
		t.Fatalf("bounded wakeups = %d, want 1", got)
	}
}

func TestSchedulerDoesNotClaimUnknownPendingIntentType(t *testing.T) {
	client := &stubPendingClient{}
	s := NewScheduler(
		client,
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			ProvisioningHint:  func() bool { return true },
			TerminalPublisher: acceptingTerminalPublisher(nil),
		},
		slog.Default(),
		1,
	)

	s.dispatchBatch(context.Background(), []monolith.Intent{
		{IntentID: "unknown-1", Type: "RECOVER"},
	})

	if got := client.claims(); len(got) != 0 {
		t.Fatalf("claim calls = %v, want none for unknown pending type", got)
	}
	if got := len(s.permits.general.slots); got != 0 {
		t.Fatalf("general permits in use = %d, want 0", got)
	}
}

func TestSchedulerForwardsProvisioningWakeupsIntoExistingPollLoop(t *testing.T) {
	provisioningWakeups := make(chan struct{}, 1)
	s := NewScheduler(
		&stubPendingClient{},
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			MinInterval:        time.Hour,
			MaxInterval:        time.Hour,
			BackoffFactor:      1,
			ProvisioningHint:   func() bool { return true },
			ProvisioningWakeup: provisioningWakeups,
			TerminalPublisher:  acceptingTerminalPublisher(nil),
		},
		slog.Default(),
		1,
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.forwardProvisioningWakeups(ctx)

	provisioningWakeups <- struct{}{}
	select {
	case <-s.repollCh:
	case <-time.After(time.Second):
		t.Fatal("provisioning wakeup was not forwarded")
	}
}

func TestSchedulerRunJoinsProvisioningForwarderAfterCancellation(t *testing.T) {
	s := newDeterministicScheduler(t, 1, func() bool { return true })
	forwarderStarted := make(chan struct{})
	forwarderCanceled := make(chan struct{})
	releaseForwarder := make(chan struct{})
	s.forwardWakeups = func(ctx context.Context) {
		close(forwarderStarted)
		<-ctx.Done()
		close(forwarderCanceled)
		<-releaseForwarder
	}

	ctx, cancel := context.WithCancel(context.Background())
	runReturned := make(chan struct{})
	go func() {
		defer close(runReturned)
		s.Run(ctx)
	}()
	<-forwarderStarted
	cancel()
	<-forwarderCanceled

	select {
	case <-runReturned:
		t.Fatal("Scheduler.Run returned before its provisioning forwarder exited")
	default:
	}
	close(releaseForwarder)
	<-runReturned
}

type launchedSession struct {
	intent  monolith.Intent
	permits *jobPermitLease
}

func newDeterministicScheduler(t *testing.T, maxConcurrent int, hint func() bool) *Scheduler {
	t.Helper()
	scheduler := NewScheduler(
		&stubPendingClient{},
		&stubRunner{},
		&capturingDKGExecutor{},
		"party-1",
		time.Millisecond,
		SchedulerConfig{
			MinInterval:       time.Millisecond,
			MaxInterval:       5 * time.Millisecond,
			BackoffFactor:     2,
			ProvisioningHint:  hint,
			TerminalPublisher: acceptingTerminalPublisher(nil),
		},
		slog.Default(),
		maxConcurrent,
	)
	scheduler.SetDKGAdmissionOpen(true)
	return scheduler
}

func captureLaunches(target *[]launchedSession) sessionLauncher {
	return func(_ context.Context, intent monolith.Intent, permits *jobPermitLease) <-chan struct{} {
		*target = append(*target, launchedSession{intent: intent, permits: permits})
		dispatched := make(chan struct{})
		close(dispatched)
		return dispatched
	}
}

func releaseLaunches(launched []launchedSession) {
	for _, session := range launched {
		session.permits.Release()
	}
}

func launchedIntentIDs(launched []launchedSession) []string {
	ids := make([]string, 0, len(launched))
	for _, session := range launched {
		ids = append(ids, session.intent.IntentID)
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
