package lifecycle

import (
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/reconcile"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
)

func TestCoordinatorStartsLockFirstAndPublishesReadinessLast(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	coordinator := mustCoordinator(t, deps)

	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })

	events.Require(t, []string{
		"validate", "lock", "open", "publisher-start", "reconcile",
		"dkg-open:true", "scheduler-start", "intake-start", "readiness",
	})
	got := deps.Readiness.Snapshot()
	if !got.ProcessReady || !got.SigningReady || !got.ProvisioningReady {
		t.Fatalf("readiness = %#v, want all capabilities ready", got)
	}
}

func TestCoordinatorRequiresStartedPublisherHandoffBeforeIntake(t *testing.T) {
	job, err := terminal.NewFailedJob("intent-1", "session-1", "mpc_key_123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatalf("NewFailedJob() error = %v", err)
	}
	wantErr := errors.New("slot start failed")
	tests := []struct {
		name string
		edit func(*Dependencies, *eventLog)
	}{
		{
			name: "publisher start failure",
			edit: func(deps *Dependencies, events *eventLog) {
				deps.StartPublisher = func(context.Context) error {
					events.Add("publisher-start")
					return wantErr
				}
			},
		},
		{
			name: "publisher handoff failure",
			edit: func(deps *Dependencies, events *eventLog) {
				deps.Handoff = func(context.Context, terminal.Job, func(terminal.PublishResult)) error {
					events.Add("handoff")
					return wantErr
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var events eventLog
			deps := successfulDependencies(&events, reconcile.Result{
				Disposition: reconcile.DispositionTerminalPublicationRequired,
				Job:         &job,
			})
			tt.edit(&deps, &events)
			coordinator := mustCoordinator(t, deps)

			if err := coordinator.Start(context.Background()); !errors.Is(err, wantErr) {
				t.Fatalf("Start() error = %v, want %v", err, wantErr)
			}
			if events.Contains("scheduler-start") || events.Contains("intake-start") {
				t.Fatalf("startup crossed failed publisher barrier: %v", events.Copy())
			}
			if got := deps.Readiness.Snapshot(); got != (health.Snapshot{}) {
				t.Fatalf("readiness = %#v, want closed", got)
			}
			if events.Last() != "lock-close" {
				t.Fatalf("failed startup release order = %v, want lock last", events.Copy())
			}
		})
	}
}

func TestCoordinatorStartupAbortJoinsStartedCapabilityLoopBeforeUnlock(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	loopStarted := make(chan struct{})
	loopCanceled := make(chan struct{})
	releaseLoop := make(chan struct{})
	loopExited := make(chan struct{})
	deps.OpenCapabilities = func(ctx context.Context) (io.Closer, error) {
		events.Add("open")
		go func() {
			close(loopStarted)
			<-ctx.Done()
			close(loopCanceled)
			<-releaseLoop
			close(loopExited)
		}()
		return closeFunc(func() error {
			<-loopExited
			events.Add("capabilities-close")
			return nil
		}), nil
	}
	wantErr := errors.New("publisher start failed")
	deps.StartPublisher = func(context.Context) error { return wantErr }
	coordinator := mustCoordinator(t, deps)

	startReturned := make(chan error, 1)
	go func() { startReturned <- coordinator.Start(context.Background()) }()
	<-loopStarted
	<-loopCanceled
	select {
	case err := <-startReturned:
		t.Fatalf("Start() returned before started capability loop exit: %v", err)
	default:
	}
	if events.Contains("lock-close") {
		t.Fatal("startup abort released lock before capability loop exit")
	}
	close(releaseLoop)
	if err := <-startReturned; !errors.Is(err, wantErr) {
		t.Fatalf("Start() error = %v, want %v", err, wantErr)
	}
	if events.Last() != "lock-close" {
		t.Fatalf("startup abort events = %v, want lock release last", events.Copy())
	}
}

func TestCoordinatorTypedConflictAllowsSigningAndOpensDKGOnlyAfterFreshPollWake(t *testing.T) {
	job, err := terminal.NewFailedJob("intent-1", "session-1", "mpc_key_123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatalf("NewFailedJob() error = %v", err)
	}
	var events eventLog
	var done func(terminal.PublishResult)
	deps := successfulDependencies(&events, reconcile.Result{
		Disposition: reconcile.DispositionTerminalPublicationRequired,
		Job:         &job,
	})
	deps.Handoff = func(_ context.Context, _ terminal.Job, callback func(terminal.PublishResult)) error {
		events.Add("handoff")
		done = callback
		return nil
	}
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })

	got := deps.Readiness.Snapshot()
	if !got.ProcessReady || !got.SigningReady || got.ProvisioningReady ||
		got.ProvisioningReason != health.ReasonDKGTerminalUnconfirmed {
		t.Fatalf("startup replay readiness = %#v", got)
	}
	if done == nil {
		t.Fatal("publisher completion callback was not installed")
	}
	done(terminal.PublishResult{Outcome: terminal.Outcome{
		Kind:                     terminal.OutcomeTerminalConflict,
		AuthoritativeStatus:      mpc2of3.TerminalStatusTimedOut,
		AuthoritativeFingerprint: job.Fingerprint(),
	}})

	events.RequireSuffix(t, []string{"dkg-open:true", "scheduler-wake"})
	got = deps.Readiness.Snapshot()
	if !got.ProvisioningReady || got.ProvisioningReason != health.ReasonNone {
		t.Fatalf("confirmed readiness = %#v", got)
	}
}

func TestCoordinatorDoesNotPublishStaleUnconfirmedReadinessWhenHandoffConfirmsImmediately(t *testing.T) {
	job, err := terminal.NewFailedJob("intent-1", "session-1", "mpc_key_123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatalf("NewFailedJob() error = %v", err)
	}
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{
		Disposition: reconcile.DispositionTerminalPublicationRequired,
		Job:         &job,
	})
	deps.Handoff = func(_ context.Context, _ terminal.Job, done func(terminal.PublishResult)) error {
		events.Add("handoff")
		done(terminal.PublishResult{Outcome: terminal.Outcome{
			Kind:                     terminal.OutcomeExactReplay,
			AuthoritativeStatus:      mpc2of3.TerminalStatusFailed,
			AuthoritativeFingerprint: job.Fingerprint(),
		}})
		return nil
	}
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })

	got := deps.Readiness.Snapshot()
	if !got.ProvisioningReady || got.ProvisioningReason != health.ReasonNone {
		t.Fatalf("immediately confirmed readiness = %#v", got)
	}
}

func TestCoordinatorCapabilityDeferredGateIsLatchedUntilRestart(t *testing.T) {
	var events eventLog
	deferred := &reconcile.CapabilityDeferredError{Cause: errors.New("recovery unavailable")}
	deps := successfulDependencies(&events, reconcile.Result{
		Disposition: reconcile.DispositionCapabilityDeferred,
		Cause:       deferred,
	})
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })

	coordinator.CapabilitiesRestored()
	if coordinator.DKGAdmissionOpen() {
		t.Fatal("capability restoration dynamically reopened DKG admission")
	}
	got := deps.Readiness.Snapshot()
	if !got.ProcessReady || !got.SigningReady || got.ProvisioningReady ||
		got.ProvisioningReason != health.ReasonCapabilityDeferred {
		t.Fatalf("capability-deferred readiness = %#v", got)
	}
}

func TestCoordinatorSeparatesEligibleReconciliationFromDynamicProvisioningReadiness(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	deps.ProvisioningReady = func() bool { return false }
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })

	got := deps.Readiness.Snapshot()
	if !got.ProcessReady || !got.SigningReady || got.ProvisioningReady ||
		got.ProvisioningReason != health.ReasonProvisioningUnavailable {
		t.Fatalf("dynamic provisioning readiness = %#v", got)
	}
	if !coordinator.DKGAdmissionOpen() {
		t.Fatal("eligible reconciliation gate closed instead of deferring to scheduler capability hint")
	}
}

func TestCoordinatorProtocolIntegrityAbortsBeforeSchedulerAndSigning(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{
		Disposition: reconcile.DispositionProtocolIntegrity,
		Cause:       &reconcile.ProtocolIntegrityError{Reason: "foreign claim"},
	})
	coordinator := mustCoordinator(t, deps)

	if err := coordinator.Start(context.Background()); err == nil {
		t.Fatal("Start() error = nil")
	}
	if events.Contains("scheduler-start") || events.Contains("intake-start") {
		t.Fatalf("protocol abort crossed intake barrier: %v", events.Copy())
	}
	if got := deps.Readiness.Snapshot(); got != (health.Snapshot{}) {
		t.Fatalf("readiness = %#v, want closed", got)
	}
}

func TestCoordinatorShutdownCancelsAndDrainsBeforeReleasingLock(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events.Reset()

	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	events.Require(t, []string{
		"dkg-open:false", "readiness-closed", "intake-stop", "cancel", "drain",
		"publisher-wait", "capabilities-close", "lock-close",
	})
}

func TestCoordinatorShutdownCancelsPublisherLifecycleBeforeWaitingAndUnlocking(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	var publisherCtx context.Context
	deps.StartPublisher = func(ctx context.Context) error {
		events.Add("publisher-start")
		publisherCtx = ctx
		return nil
	}
	deps.WaitPublisher = func() {
		if publisherCtx == nil || !errors.Is(publisherCtx.Err(), context.Canceled) {
			t.Fatalf("publisher context error = %v, want context.Canceled", publisherCtx.Err())
		}
		events.Add("publisher-wait")
	}
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	events.Reset()

	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if events.Last() != "lock-close" {
		t.Fatalf("shutdown events = %v, want lock release last", events.Copy())
	}
}

func TestCoordinatorConcurrentConfirmationCannotReopenReadinessDuringShutdown(t *testing.T) {
	job, err := terminal.NewFailedJob("intent-1", "session-1", "mpc_key_123e4567-e89b-42d3-a456-426614174000")
	if err != nil {
		t.Fatalf("NewFailedJob() error = %v", err)
	}
	var events eventLog
	var done func(terminal.PublishResult)
	deps := successfulDependencies(&events, reconcile.Result{
		Disposition: reconcile.DispositionTerminalPublicationRequired,
		Job:         &job,
	})
	deps.Handoff = func(_ context.Context, _ terminal.Job, callback func(terminal.PublishResult)) error {
		done = callback
		return nil
	}
	confirmationEntered := make(chan struct{})
	releaseConfirmation := make(chan struct{})
	blockConfirmation := false
	deps.SetDKGAdmissionOpen = func(open bool) {
		if open && blockConfirmation {
			close(confirmationEntered)
			<-releaseConfirmation
		}
	}
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	blockConfirmation = true
	confirmed := make(chan struct{})
	go func() {
		defer close(confirmed)
		done(terminal.PublishResult{Outcome: terminal.Outcome{
			Kind:                     terminal.OutcomeAccepted,
			AuthoritativeStatus:      mpc2of3.TerminalStatusFailed,
			AuthoritativeFingerprint: job.Fingerprint(),
		}})
	}()
	<-confirmationEntered

	if err := coordinator.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	close(releaseConfirmation)
	<-confirmed

	if got := deps.Readiness.Snapshot(); got != (health.Snapshot{}) {
		t.Fatalf("post-shutdown readiness reopened: %#v", got)
	}
	if coordinator.DKGAdmissionOpen() {
		t.Fatal("post-shutdown DKG admission reopened")
	}
}

func TestCoordinatorFreshPollDispositionRepeatsReconciliationBeforeIntake(t *testing.T) {
	var events eventLog
	deps := successfulDependencies(&events, reconcile.Result{Disposition: reconcile.DispositionEligible})
	calls := 0
	deps.Reconcile = func(context.Context) (reconcile.Result, error) {
		events.Add("reconcile")
		calls++
		if calls == 1 {
			return reconcile.Result{Disposition: reconcile.DispositionFreshPollRequired}, nil
		}
		return reconcile.Result{Disposition: reconcile.DispositionEligible}, nil
	}
	coordinator := mustCoordinator(t, deps)
	if err := coordinator.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	t.Cleanup(func() { _ = coordinator.Shutdown(context.Background()) })
	if calls != 2 {
		t.Fatalf("reconcile calls = %d, want fresh authoritative poll", calls)
	}
}

func mustCoordinator(t *testing.T, deps Dependencies) *Coordinator {
	t.Helper()
	coordinator, err := NewCoordinator(deps)
	if err != nil {
		t.Fatalf("NewCoordinator() error = %v", err)
	}
	return coordinator
}

func successfulDependencies(events *eventLog, result reconcile.Result) Dependencies {
	readiness := health.NewReadiness()
	deps := Dependencies{Readiness: readiness}
	deps.ProvisioningReady = func() bool { return true }
	deps.Validate = func(context.Context) error { events.Add("validate"); return nil }
	deps.AcquireLock = func() (io.Closer, error) {
		events.Add("lock")
		return closeFunc(func() error { events.Add("lock-close"); return nil }), nil
	}
	deps.OpenCapabilities = func(context.Context) (io.Closer, error) {
		events.Add("open")
		return closeFunc(func() error { events.Add("capabilities-close"); return nil }), nil
	}
	deps.StartPublisher = func(context.Context) error { events.Add("publisher-start"); return nil }
	deps.Reconcile = func(context.Context) (reconcile.Result, error) {
		events.Add("reconcile")
		return result, nil
	}
	deps.Handoff = func(context.Context, terminal.Job, func(terminal.PublishResult)) error {
		events.Add("handoff")
		return nil
	}
	deps.SetDKGAdmissionOpen = func(open bool) { events.Add("dkg-open:" + boolText(open)) }
	deps.StartScheduler = func(context.Context) { events.Add("scheduler-start") }
	deps.WakeScheduler = func() { events.Add("scheduler-wake") }
	deps.StartIntake = func(context.Context) error { events.Add("intake-start"); return nil }
	deps.StopIntake = func(context.Context) error { events.Add("intake-stop"); return nil }
	deps.Drain = func(context.Context) error { events.Add("drain"); return nil }
	deps.WaitPublisher = func() { events.Add("publisher-wait") }
	deps.OnCancel = func() { events.Add("cancel") }
	deps.OnReadiness = func(ready bool) {
		if ready {
			events.Add("readiness")
		} else {
			events.Add("readiness-closed")
		}
	}
	return deps
}

type closeFunc func() error

func (f closeFunc) Close() error { return f() }

type eventLog struct {
	mu     sync.Mutex
	events []string
}

func (l *eventLog) Add(event string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = append(l.events, event)
}

func (l *eventLog) Copy() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...)
}

func (l *eventLog) Contains(event string) bool {
	for _, got := range l.Copy() {
		if got == event {
			return true
		}
	}
	return false
}

func (l *eventLog) Last() string {
	events := l.Copy()
	if len(events) == 0 {
		return ""
	}
	return events[len(events)-1]
}

func (l *eventLog) Reset() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.events = nil
}

func (l *eventLog) Require(t *testing.T, want []string) {
	t.Helper()
	got := l.Copy()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events = %v, want %v", got, want)
		}
	}
}

func (l *eventLog) RequireSuffix(t *testing.T, want []string) {
	t.Helper()
	got := l.Copy()
	if len(got) < len(want) {
		t.Fatalf("events = %v, want suffix %v", got, want)
	}
	got = got[len(got)-len(want):]
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("events suffix = %v, want %v", got, want)
		}
	}
}

func boolText(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
