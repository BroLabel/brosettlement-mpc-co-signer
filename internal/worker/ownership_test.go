package worker

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type controlledSignRunner struct {
	run func(context.Context, coretss.SignSessionRequest) error
}

func TestRejectedReadinessCannotReplaceTrustedResultIdentity(t *testing.T) {
	for _, name := range []string{"changed start", "changed expiry", "empty malformed lifecycle", "terminal readiness", "expired readiness"} {
		t.Run(name, func(t *testing.T) {
			discovery, claim := rediscoveredSignFixture(t)
			discovery.DiscoveryStatus = "PENDING"
			start := time.Now()
			expiry := start.Add(300 * time.Second)
			claim.Session.Status = "RUNNING"
			claim.Session.StartedAt = &start
			claim.Session.ExecutionExpiresAt = &expiry
			changedStart := start.Add(time.Second)
			if name == "expired readiness" {
				changedStart = start.Add(-400 * time.Second)
			}
			changedExpiry := changedStart.Add(300 * time.Second)
			rejected := claim.Session
			rejected.StartedAt = &changedStart
			rejected.ExecutionExpiresAt = &changedExpiry
			terminal := rejected
			terminal.Status = "COMPLETED"
			var readinessErr error
			switch name {
			case "changed expiry":
				rejected.StartedAt = &start
			case "empty malformed lifecycle":
				rejected = monolith.SessionLifecycle{}
				readinessErr = monolith.ErrInvalidLifecycle
			case "terminal readiness":
				rejected.Status = "COMPLETED"
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			var polls, posts, coreCalls atomic.Int32
			retried, release := make(chan struct{}), make(chan struct{})
			client := &stubPendingClient{claimResult: claim}
			client.pollFunc = func(context.Context, string, uint64) (monolith.MessagesResult, error) {
				if polls.Add(1) == 1 {
					return monolith.MessagesResult{Session: rejected}, readinessErr
				}
				return monolith.MessagesResult{Session: terminal}, nil
			}
			client.postFunc = func(c context.Context, _ string, result monolith.IntentResult) error {
				if result.Status != "FAILED" {
					t.Errorf("readiness failure result = %+v", result)
				}
				if posts.Add(1) == 1 {
					return errors.New("FAILED response lost")
				}
				close(retried)
				select {
				case <-release:
					return nil
				case <-c.Done():
					return c.Err()
				}
			}
			runner := controlledSignRunner{run: func(context.Context, coretss.SignSessionRequest) error { coreCalls.Add(1); return nil }}
			s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
			s.dispatchBatch(ctx, []monolith.Intent{discovery})
			select {
			case <-retried:
				if len(s.Semaphore()) != 1 {
					t.Error("rejected readiness released permit")
				}
				close(release)
				waitForIntentRelease(t, s, discovery.IntentID)
			case <-s.repollCh:
				waitForIntentRelease(t, s, discovery.IntentID)
				t.Fatalf("rejected readiness replaced trusted identity: permit released after %d POST, Core calls %d", posts.Load(), coreCalls.Load())
			case <-ctx.Done():
				t.Fatal("owned result was not retried")
			}
			if coreCalls.Load() != 0 {
				t.Fatalf("invalid readiness started Core %d times", coreCalls.Load())
			}
		})
	}
}

func TestMatchingTerminalReadinessResolvesLegitimateCleanup(t *testing.T) {
	for _, baseline := range []string{"pending", "running", "expired execution"} {
		t.Run(baseline, func(t *testing.T) {
			discovery, claim := rediscoveredSignFixture(t)
			discovery.DiscoveryStatus = "PENDING"
			if baseline != "pending" {
				start := time.Now()
				if baseline == "expired execution" {
					start = start.Add(-400 * time.Second)
				}
				expiry := start.Add(300 * time.Second)
				claim.Session.Status = "RUNNING"
				claim.Session.StartedAt = &start
				claim.Session.ExecutionExpiresAt = &expiry
			}
			terminal := claim.Session
			terminal.Status = "FAILED"
			var posts, coreCalls atomic.Int32
			client := &stubPendingClient{claimResult: claim}
			client.pollFunc = func(context.Context, string, uint64) (monolith.MessagesResult, error) {
				if baseline == "expired execution" && posts.Load() == 0 {
					return monolith.MessagesResult{Session: claim.Session}, nil
				}
				return monolith.MessagesResult{Session: terminal}, nil
			}
			client.postFunc = func(context.Context, string, monolith.IntentResult) error {
				posts.Add(1)
				return errors.New("cleanup response lost")
			}
			runner := controlledSignRunner{run: func(context.Context, coretss.SignSessionRequest) error { coreCalls.Add(1); return nil }}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
			s.dispatchBatch(ctx, []monolith.Intent{discovery})
			waitForIntentRelease(t, s, discovery.IntentID)
			if ctx.Err() != nil || posts.Load() != 1 || coreCalls.Load() != 0 {
				t.Fatalf("legitimate terminal cleanup: context=%v posts=%d Core=%d", ctx.Err(), posts.Load(), coreCalls.Load())
			}
		})
	}
}

func (r controlledSignRunner) RunSignSession(ctx context.Context, req coretss.SignSessionRequest) error {
	return r.run(ctx, req)
}

func TestClaimAmbiguityDoesNotBlockOtherPermits(t *testing.T) {
	first, claim := rediscoveredSignFixture(t)
	first.DiscoveryStatus = "PENDING"
	second := first
	second.IntentID = "second"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	otherClaim := make(chan struct{})
	client := &stubPendingClient{}
	client.claimFunc = func(ctx context.Context, _ string, id string) (monolith.ClaimResult, error) {
		if id == first.IntentID {
			return monolith.ClaimResult{}, monolith.ErrClaimOutcomeUnknown
		}
		close(otherClaim)
		return claim, monolith.ErrAlreadyClaimed
	}
	s := NewScheduler(client, &stubRunner{}, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 2)
	s.dispatchBatch(ctx, []monolith.Intent{first, second})
	select {
	case <-otherClaim:
	case <-ctx.Done():
		t.Fatal("claim ambiguity blocked another free permit")
	}
	cancel()
	s.sessions.Wait()
}

func TestMalformedRunningLifecycleRetainsPermitThroughRunnerAndResult(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	start := time.Now()
	expiry := start.Add(300 * time.Second)
	claim.Session.Status = "RUNNING"
	claim.Session.StartedAt = &start
	claim.Session.ExecutionExpiresAt = &expiry
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	entered, canceled, releaseRunner := make(chan struct{}), make(chan struct{}), make(chan struct{})
	retry, releasePost := make(chan struct{}), make(chan struct{})
	var polls, posts atomic.Int32
	client := &stubPendingClient{claimResult: claim}
	client.pollFunc = func(c context.Context, _ string, _ uint64) (monolith.MessagesResult, error) {
		if polls.Add(1) == 1 {
			return monolith.MessagesResult{Session: claim.Session}, nil
		}
		select {
		case <-entered:
		case <-c.Done():
			return monolith.MessagesResult{}, c.Err()
		}
		return monolith.MessagesResult{}, monolith.ErrInvalidLifecycle
	}
	client.postFunc = func(c context.Context, _ string, result monolith.IntentResult) error {
		if result.Status != "FAILED" {
			t.Errorf("malformed lifecycle result=%+v", result)
		}
		if posts.Add(1) == 1 {
			return errors.New("response lost")
		}
		close(retry)
		select {
		case <-releasePost:
			return nil
		case <-c.Done():
			return c.Err()
		}
	}
	runner := controlledSignRunner{run: func(c context.Context, _ coretss.SignSessionRequest) error {
		close(entered)
		<-c.Done()
		close(canceled)
		<-releaseRunner
		return c.Err()
	}}
	s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
	s.dispatchBatch(ctx, []monolith.Intent{discovery})
	select {
	case <-canceled:
	case <-ctx.Done():
		t.Fatal("malformed running lifecycle did not stop runner")
	}
	if len(s.Semaphore()) != 1 || posts.Load() != 0 {
		t.Fatal("cancellation bypassed actual runner completion")
	}
	close(releaseRunner)
	select {
	case <-retry:
	case <-ctx.Done():
		t.Fatal("result was not retried")
	}
	if len(s.Semaphore()) != 1 {
		t.Fatal("ambiguous failure publication released permit")
	}
	close(releasePost)
	waitForIntentRelease(t, s, discovery.IntentID)
}

func TestTerminalWaitingAndRunnerErrorResolveBeforePermitRelease(t *testing.T) {
	for _, terminalWaiting := range []bool{true, false} {
		name := "runner error"
		if terminalWaiting {
			name = "terminal while waiting"
		}
		t.Run(name, func(t *testing.T) {
			discovery, claim := rediscoveredSignFixture(t)
			discovery.DiscoveryStatus = "PENDING"
			if terminalWaiting {
				claim.Session.Status = "FAILED"
			}
			client := &stubPendingClient{claimResult: claim}
			posted, release := make(chan struct{}), make(chan struct{})
			client.postFunc = func(c context.Context, _ string, result monolith.IntentResult) error {
				if result.Status != "FAILED" {
					t.Errorf("result=%+v", result)
				}
				close(posted)
				select {
				case <-release:
					return &monolith.ResultConflictError{AuthoritativeStatus: "FAILED"}
				case <-c.Done():
					return c.Err()
				}
			}
			var calls atomic.Int32
			runner := controlledSignRunner{run: func(context.Context, coretss.SignSessionRequest) error {
				calls.Add(1)
				return errors.New("runner failed")
			}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
			s.dispatchBatch(ctx, []monolith.Intent{discovery})
			select {
			case <-posted:
			case <-ctx.Done():
				t.Fatal("failure did not enter publication")
			}
			if len(s.Semaphore()) != 1 {
				t.Fatal("failure released permit before result resolution")
			}
			want := int32(1)
			if terminalWaiting {
				want = 0
			}
			if calls.Load() != want {
				t.Fatalf("Core calls=%d want=%d", calls.Load(), want)
			}
			close(release)
			waitForIntentRelease(t, s, discovery.IntentID)
		})
	}
}

func TestLostClaimRetainsOwnerAndRecoversAfterRunning(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	start := time.Now()
	expiry := start.Add(300 * time.Second)
	claim.Session.Status = "RUNNING"
	claim.Session.StartedAt = &start
	claim.Session.ExecutionExpiresAt = &expiry
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	replay := make(chan struct{})
	allow := make(chan struct{})
	var calls atomic.Int32
	client := &stubPendingClient{claimResult: claim}
	client.claimFunc = func(c context.Context, kind, id string) (monolith.ClaimResult, error) {
		if kind != "SIGN" || id != discovery.IntentID {
			t.Errorf("claim identity changed: %s %s", kind, id)
		}
		parentDeadline, _ := ctx.Deadline()
		if deadline, ok := c.Deadline(); !ok || deadline.After(parentDeadline) || deadline.After(discovery.ExpiresAt) {
			t.Error("claim request exceeds parent or intent deadline")
		}
		if calls.Add(1) == 1 {
			return monolith.ClaimResult{}, monolith.ErrClaimOutcomeUnknown
		}
		close(replay)
		select {
		case <-allow:
		case <-c.Done():
			return monolith.ClaimResult{}, c.Err()
		}
		return claim, nil
	}
	runner := &notifyingSignRunner{started: make(chan struct{}, 2)}
	s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, slog.Default(), 2)
	dispatched := make(chan struct{})
	go func() { s.dispatchBatch(ctx, []monolith.Intent{discovery}); close(dispatched) }()
	select {
	case <-dispatched:
	case <-ctx.Done():
		t.Fatal("dispatch waited for recovery")
	}
	select {
	case <-replay:
	case <-ctx.Done():
		t.Fatal("lost claim was not retried by live owner")
	}
	if len(s.Semaphore()) != 1 {
		t.Fatal("claim ambiguity released permit")
	}
	s.dispatchBatch(ctx, []monolith.Intent{discovery, discovery})
	if calls.Load() != 2 {
		t.Fatal("rediscovery created another owner")
	}
	close(allow)
	select {
	case <-runner.started:
	case <-ctx.Done():
		t.Fatal("recovered claim did not run within handoff budget")
	}
	waitForIntentRelease(t, s, discovery.IntentID)
	if calls.Load() != 2 {
		t.Fatal("claim identity executed more than once")
	}
}

func TestNoPermitPreventsFirstClaim(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	client := &stubPendingClient{claimResult: claim}
	s := NewScheduler(client, &stubRunner{}, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
	lease := s.permits.tryAcquireSIGN()
	defer lease.Release()
	s.dispatchBatch(context.Background(), []monolith.Intent{discovery})
	if len(client.claims()) != 0 {
		t.Fatal("claim sent without reserved capacity")
	}
	releaseIntent, reserved := s.reserveIntentID(discovery.IntentID)
	if !reserved {
		t.Fatal("unclaimed intent retained local ownership")
	}
	releaseIntent()
}

func TestClaimLostThenExpiredConflictCleansBeforePermitRelease(t *testing.T) {
	discovery, _ := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var calls atomic.Int32
	posted := make(chan struct{}, 1)
	client := &stubPendingClient{}
	client.claimFunc = func(context.Context, string, string) (monolith.ClaimResult, error) {
		if calls.Add(1) == 1 {
			return monolith.ClaimResult{}, monolith.ErrClaimOutcomeUnknown
		}
		return monolith.ClaimResult{}, monolith.ErrAlreadyClaimed
	}
	client.postFunc = func(context.Context, string, monolith.IntentResult) error { posted <- struct{}{}; return nil }
	s := NewScheduler(client, &stubRunner{}, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
	s.dispatchBatch(ctx, []monolith.Intent{discovery})
	select {
	case <-posted:
	case <-ctx.Done():
		t.Fatal("expired replay conflict abandoned the owned claim")
	}
	waitForIntentRelease(t, s, discovery.IntentID)
}

func TestLostResultRetainsPermitWithoutRecomputation(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	retry := make(chan struct{})
	allow := make(chan struct{})
	var posts atomic.Int32
	client := &stubPendingClient{claimResult: claim}
	client.postFunc = func(c context.Context, id string, result monolith.IntentResult) error {
		if result.Status != "COMPLETED" || id != discovery.IntentID {
			t.Errorf("result changed: %+v", result)
		}
		if posts.Add(1) == 1 {
			return errors.New("response lost after commit")
		}
		close(retry)
		select {
		case <-allow:
			return nil
		case <-c.Done():
			return c.Err()
		}
	}
	runner := &notifyingSignRunner{started: make(chan struct{}, 2)}
	s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, slog.Default(), 1)
	s.dispatchBatch(ctx, []monolith.Intent{discovery})
	select {
	case <-retry:
	case <-s.repollCh:
		waitForIntentRelease(t, s, discovery.IntentID)
		s.dispatchBatch(ctx, []monolith.Intent{discovery})
		select {
		case <-retry:
		case <-ctx.Done():
			t.Fatal("premature permit release prevented result resolution")
		}
		calls := len(runner.started)
		close(allow)
		waitForIntentRelease(t, s, discovery.IntentID)
		t.Fatalf("lost result released permit and recomputed Core: calls=%d", calls)
	case <-ctx.Done():
		t.Fatal("lost result abandoned instead of retried")
	}
	if len(s.Semaphore()) != 1 {
		t.Fatal("result ambiguity released permit")
	}
	s.dispatchBatch(ctx, []monolith.Intent{discovery, discovery})
	if len(runner.started) != 1 {
		t.Fatal("result loss reran Core")
	}
	close(allow)
	waitForIntentRelease(t, s, discovery.IntentID)
}

func TestSchedulerRunJoinsBlockedSignAfterCancellation(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	release := make(chan struct{})
	runner := &blockingSignRunner{started: make(chan string, 1), release: release}
	client := &stubPendingClient{intents: []monolith.Intent{discovery}, claimResult: claim}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := NewScheduler(client, runner, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{MinInterval: time.Millisecond}, slog.Default(), 1)
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-runner.started:
	case <-time.After(time.Second):
		t.Fatal("runner not started")
	}
	cancel()
	select {
	case <-done:
		close(release)
		t.Fatal("scheduler returned before runner stopped")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("scheduler failed to join completed runner")
	}
}

func TestClaimAmbiguityDeadlineRequiresCleanup(t *testing.T) {
	discovery, _ := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	discovery.ExpiresAt = time.Now().Add(30 * time.Millisecond)
	posted := make(chan monolith.IntentResult, 1)
	client := &stubPendingClient{}
	client.claimFunc = func(ctx context.Context, _, _ string) (monolith.ClaimResult, error) {
		<-ctx.Done()
		return monolith.ClaimResult{}, monolith.ErrClaimOutcomeUnknown
	}
	client.postFunc = func(_ context.Context, _ string, result monolith.IntentResult) error { posted <- result; return nil }
	s := NewScheduler(client, &stubRunner{}, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
	s.dispatchBatch(context.Background(), []monolith.Intent{discovery})
	select {
	case result := <-posted:
		if result.Status != "FAILED" {
			t.Fatalf("cleanup = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("claim deadline released ownership without cleanup")
	}
	waitForIntentRelease(t, s, discovery.IntentID)
}

func TestLostResultReconcilesAuthoritativeTerminalWithoutCore(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var resultSent atomic.Bool
	client := &stubPendingClient{claimResult: claim}
	client.postFunc = func(context.Context, string, monolith.IntentResult) error {
		resultSent.Store(true)
		return errors.New("lost result response")
	}
	client.pollFunc = func(context.Context, string, uint64) (monolith.MessagesResult, error) {
		state := claim.Session
		start := state.Deadline.Add(-time.Minute)
		expiry := state.Deadline
		state.StartedAt = &start
		state.ExecutionExpiresAt = &expiry
		if resultSent.Load() {
			state.Status = "COMPLETED"
		} else {
			start := state.Deadline.Add(-time.Minute)
			expiry := state.Deadline
			state.Status = "RUNNING"
			state.StartedAt = &start
			state.ExecutionExpiresAt = &expiry
		}
		return monolith.MessagesResult{Session: state}, nil
	}
	runner := &countingSignRunner{}
	permits := newSchedulerPermits(1, nil)
	done := make(chan struct{})
	go func() {
		runSessionWithPermits(ctx, discovery, client, runner, nil, nil, coordinatorPrimaryParty, time.Millisecond, permits.tryAcquireSIGN(), slog.Default(), nil)
		close(done)
	}()
	select {
	case <-done:
		if ctx.Err() != nil {
			t.Fatal("result ownership ended only at shutdown, not terminal reconciliation")
		}
	case <-ctx.Done():
		<-done
		t.Fatal("authoritative terminal failed to resolve delivery")
	}
	if runner.calls != 1 {
		t.Fatalf("Core calls=%d", runner.calls)
	}
}

func TestResultReadbackRejectsChangedExecutionIdentity(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	discovery.DiscoveryStatus = "PENDING"
	start := time.Now()
	expiry := start.Add(300 * time.Second)
	claim.Session.Status = "RUNNING"
	claim.Session.StartedAt = &start
	claim.Session.ExecutionExpiresAt = &expiry
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var posts atomic.Int32
	retried, release := make(chan struct{}), make(chan struct{})
	client := &stubPendingClient{claimResult: claim}
	client.pollFunc = func(context.Context, string, uint64) (monolith.MessagesResult, error) {
		state := claim.Session
		if posts.Load() > 0 {
			changed := start.Add(time.Second)
			changedExpiry := expiry.Add(time.Second)
			state.Status = "COMPLETED"
			state.StartedAt = &changed
			state.ExecutionExpiresAt = &changedExpiry
		}
		return monolith.MessagesResult{Session: state}, nil
	}
	client.postFunc = func(c context.Context, _ string, _ monolith.IntentResult) error {
		if posts.Add(1) == 1 {
			return errors.New("lost result")
		}
		close(retried)
		select {
		case <-release:
			return nil
		case <-c.Done():
			return c.Err()
		}
	}
	s := NewScheduler(client, &stubRunner{}, nil, coordinatorPrimaryParty, time.Millisecond, SchedulerConfig{}, nil, 1)
	s.dispatchBatch(ctx, []monolith.Intent{discovery})
	select {
	case <-retried:
		close(release)
		waitForIntentRelease(t, s, discovery.IntentID)
	case <-s.repollCh:
		t.Fatal("changed terminal execution identity released ownership")
	case <-ctx.Done():
		t.Fatal("result did not retry")
	}
}
