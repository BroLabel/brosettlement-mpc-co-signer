package worker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/sharestore"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/terminal"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

type stubClient struct {
	mu          sync.Mutex
	claimResult monolith.ClaimResult
	claimErr    error
	lastResult  monolith.IntentResult
	lastSession string
	lastFrame   monolith.OutboundFrame
}

func (s *stubClient) ClaimIntent(_ context.Context, _ string, _ string) (monolith.ClaimResult, error) {
	if s.claimResult.ExpiresAt.IsZero() {
		s.claimResult.ExpiresAt = time.Now().Add(time.Minute)
	}
	return s.claimResult, s.claimErr
}

func (s *stubClient) PostResult(_ context.Context, _ string, result monolith.IntentResult) error {
	s.lastResult = result
	return nil
}

func (s *stubClient) PostMessage(_ context.Context, sessionID string, frame monolith.OutboundFrame) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastSession = sessionID
	s.lastFrame = frame
	return nil
}

func (s *stubClient) GetMessages(context.Context, string, uint64) ([]monolith.InboundMessage, error) {
	return nil, nil
}

func claimResultForIntent(intent monolith.Intent) monolith.ClaimResult {
	return monolith.ClaimResult{
		IntentID:  intent.IntentID,
		SessionID: intent.SessionID,
		Type:      intent.Type,
		Payload:   intent.Payload,
		Status:    "CLAIMED",
		ExpiresAt: time.Now().Add(time.Minute),
	}
}

type stubRunner struct{}

func (s *stubRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

type capturingRunner struct{}

func (s *capturingRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return nil
}

type countingSignRunner struct {
	calls int
}

func (r *countingSignRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	r.calls++
	return nil
}

type errorSignRunner struct {
	err error
}

func (r *errorSignRunner) RunSignSession(context.Context, coretss.SignSessionRequest) error {
	return r.err
}

type capturedLogRecord struct {
	level   slog.Level
	message string
	attrs   []slog.Attr
}

type alertCapturingHandler struct {
	mu      sync.Mutex
	records []capturedLogRecord
}

func (h *alertCapturingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *alertCapturingHandler) Handle(_ context.Context, record slog.Record) error {
	captured := capturedLogRecord{level: record.Level, message: record.Message}
	record.Attrs(func(attr slog.Attr) bool {
		captured.attrs = append(captured.attrs, attr)
		return true
	})
	h.mu.Lock()
	h.records = append(h.records, captured)
	h.mu.Unlock()
	return nil
}

func (h *alertCapturingHandler) WithAttrs([]slog.Attr) slog.Handler { return h }

func (h *alertCapturingHandler) WithGroup(string) slog.Handler { return h }

func (h *alertCapturingHandler) Records() []capturedLogRecord {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]capturedLogRecord(nil), h.records...)
}

type capturingDKGExecutor struct {
	intent monolith.Intent
	calls  int
	result DKGResult
	err    error
}

type mailboxSendingDKGExecutor struct {
	result DKGResult
	err    error
}

func (e *mailboxSendingDKGExecutor) Run(ctx context.Context, _ monolith.Intent, tr coretss.Transport) (DKGResult, error) {
	if err := tr.SendFrame(ctx, protocol.Frame{
		MessageID: "msg_0123456789abcdef",
		Seq:       1,
		Round:     1,
		FromParty: "co-signer-primary",
		ToParty:   "mpc-signer",
		Payload:   []byte{0},
	}); err != nil {
		return DKGResult{}, err
	}
	return e.result, e.err
}

type terminalPublisherFunc func(context.Context, terminal.Job) (terminal.Outcome, error)

func (f terminalPublisherFunc) Publish(ctx context.Context, job terminal.Job) (terminal.Outcome, error) {
	return f(ctx, job)
}

func acceptingTerminalPublisher(target *terminal.Job) terminalPublisherFunc {
	return func(_ context.Context, job terminal.Job) (terminal.Outcome, error) {
		if target != nil {
			*target = job
		}
		return terminal.Outcome{
			Kind:                     terminal.OutcomeAccepted,
			AuthoritativeStatus:      job.Status(),
			AuthoritativeFingerprint: job.Fingerprint(),
		}, nil
	}
}

func (e *capturingDKGExecutor) Run(_ context.Context, intent monolith.Intent, _ coretss.Transport) (DKGResult, error) {
	e.calls++
	e.intent = intent
	return e.result, e.err
}

func TestRunSessionRoutesDKGOnlyThroughCoordinator(t *testing.T) {
	intent := authoritativeDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	signRunner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: completeDKGResultForIntent(t, intent)}
	var published terminal.Job
	publisher := acceptingTerminalPublisher(&published)
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(
		context.Background(),
		intent,
		client,
		signRunner,
		dkgExecutor,
		publisher,
		"co-signer",
		time.Millisecond,
		sem,
		nil,
		slog.Default(),
	)

	if dkgExecutor.calls != 1 {
		t.Fatalf("DKG coordinator calls = %d, want 1", dkgExecutor.calls)
	}
	if published.Status() != mpc2of3.TerminalStatusCompleted {
		t.Fatalf("published terminal status = %q", published.Status())
	}
	if client.lastResult.Status != "" {
		t.Fatalf("DKG used legacy result endpoint: %+v", client.lastResult)
	}
}

func runSessionWithExecutorsForTest(
	ctx context.Context,
	intent monolith.Intent,
	client sessionClient,
	signRunner signSessionRunner,
	dkgRunner dkgExecutor,
	terminalPublisher DKGTerminalPublisher,
	localPartyID string,
	framePollInterval time.Duration,
	sem chan struct{},
	repollCh chan struct{},
	log *slog.Logger,
) {
	lease := &jobPermitLease{wakeups: repollCh}
	if sem != nil {
		lease.general = &permitToken{owner: &permitPool{slots: sem}}
	}
	runSessionWithPermits(
		ctx,
		intent,
		client,
		signRunner,
		dkgRunner,
		terminalPublisher,
		localPartyID,
		framePollInterval,
		lease,
		log,
		nil,
	)
}

func TestRunSessionPassesClaimedMailboxContextToDKGTransport(t *testing.T) {
	intent := authoritativeDKGIntent()
	intent.SessionID = "123e4567-e89b-42d3-a456-426614174123"
	intent.Payload.OrgID = "org-123"
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	executor := &mailboxSendingDKGExecutor{result: completeDKGResultForIntent(t, intent)}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(context.Background(), intent, client, &stubRunner{}, executor, acceptingTerminalPublisher(nil), "co-signer", time.Millisecond, sem, nil, slog.Default())

	client.mu.Lock()
	defer client.mu.Unlock()
	if client.lastSession != intent.SessionID || client.lastFrame.SessionID != intent.SessionID ||
		client.lastFrame.IntentID != intent.IntentID || client.lastFrame.OrgID != intent.Payload.OrgID ||
		client.lastFrame.AuthenticatedPartyID != "co-signer-primary" {
		t.Fatalf("worker lost claimed mailbox context: route=%q frame=%+v", client.lastSession, client.lastFrame)
	}
}

func TestRunSessionRediscoveredSignClaimsReplayBeforeRuntime(t *testing.T) {
	discovery, claim := rediscoveredSignFixture(t)
	client := &stubClient{claimResult: claim}
	runner := &countingSignRunner{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(context.Background(), discovery, client, runner, &capturingDKGExecutor{}, nil, coordinatorPrimaryParty, time.Millisecond, sem, nil, slog.Default())

	if runner.calls != 1 {
		t.Fatalf("SIGN runtime calls = %d, want 1 after organization-scoped claim replay", runner.calls)
	}
	if client.lastResult.Status != "COMPLETED" {
		t.Fatalf("SIGN result = %+v, want minimal completed result", client.lastResult)
	}
}

func TestRunSessionRejectsRediscoveredSignClaimIdentityOrDeadlineMismatch(t *testing.T) {
	tests := []struct {
		name string
		edit func(*monolith.ClaimResult)
	}{
		{name: "intent", edit: func(claim *monolith.ClaimResult) { claim.IntentID = "intent-other" }},
		{name: "session", edit: func(claim *monolith.ClaimResult) { claim.SessionID = "session-other" }},
		{name: "key", edit: func(claim *monolith.ClaimResult) { claim.Payload.KeyID = "key-other" }},
		{name: "org", edit: func(claim *monolith.ClaimResult) { claim.Payload.OrgID = "org-other" }},
		{name: "deadline value", edit: func(claim *monolith.ClaimResult) { claim.Deadline = claim.Deadline.Add(time.Second) }},
		{name: "deadline representation", edit: func(claim *monolith.ClaimResult) { claim.DeadlineRaw = claim.Deadline.Format(time.RFC3339) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			discovery, claim := rediscoveredSignFixture(t)
			tt.edit(&claim)
			runner := &countingSignRunner{}
			sem := make(chan struct{}, 1)
			sem <- struct{}{}
			runSessionWithExecutorsForTest(context.Background(), discovery, &stubClient{claimResult: claim}, runner, &capturingDKGExecutor{}, nil, "co-signer", time.Millisecond, sem, nil, slog.Default())
			if runner.calls != 0 {
				t.Fatalf("SIGN runtime calls = %d, want 0", runner.calls)
			}
		})
	}
}

func rediscoveredSignFixture(t *testing.T) (monolith.Intent, monolith.ClaimResult) {
	t.Helper()
	claimed := validSignIntent(t)
	claimed.IntentID = "intent-125"
	claimed.SessionID = "sign-125"
	claimed.Payload.OrgID = "org-123"
	claimed.Payload.KeyID = "key-125"
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second)
	deadlineRaw := deadline.Format("2006-01-02T15:04:05.000Z")
	discovery := monolith.Intent{
		CreatedAt:       time.Now().UTC().Add(-time.Minute),
		DeadlineRaw:     deadlineRaw,
		DiscoveryStatus: "CLAIMED",
		IntentID:        claimed.IntentID,
		SessionID:       claimed.SessionID,
		Type:            "SIGN",
		ExpiresAt:       deadline,
		Payload:         monolith.IntentPayload{Type: "SIGN", OrgID: claimed.Payload.OrgID, KeyID: claimed.Payload.KeyID},
	}
	claim := claimResultForIntent(claimed)
	claim.Deadline = deadline
	claim.ExpiresAt = time.Time{}
	claim.DeadlineRaw = deadlineRaw
	return discovery, claim
}

func TestNormalDKGHoldsPermitLeaseUntilAuthoritativeTerminalOutcome(t *testing.T) {
	intent := authoritativeDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	dkgExecutor := &capturingDKGExecutor{result: completeDKGResultForIntent(t, intent)}
	started := make(chan terminal.Job, 1)
	confirm := make(chan struct{})
	publisher := terminalPublisherFunc(func(_ context.Context, job terminal.Job) (terminal.Outcome, error) {
		started <- job
		<-confirm
		return terminal.Outcome{
			Kind:                     terminal.OutcomeAccepted,
			AuthoritativeStatus:      job.Status(),
			AuthoritativeFingerprint: job.Fingerprint(),
		}, nil
	})
	permits := newSchedulerPermits(2, nil)
	lease := permits.tryAcquireDKG()
	if lease == nil {
		t.Fatal("failed to acquire DKG permit lease")
	}

	done := make(chan struct{})
	go func() {
		runSessionWithPermits(
			context.Background(),
			intent,
			client,
			&stubRunner{},
			dkgExecutor,
			publisher,
			"co-signer",
			time.Millisecond,
			lease,
			slog.Default(),
			nil,
		)
		close(done)
	}()

	var job terminal.Job
	select {
	case job = <-started:
	case <-time.After(time.Second):
		t.Fatal("terminal publication did not start")
	}
	if job.Status() != mpc2of3.TerminalStatusCompleted {
		t.Fatalf("terminal status = %q", job.Status())
	}
	if len(permits.general.slots) != 1 || len(permits.dkg.slots) != 1 {
		t.Fatal("DKG permit lease released before authoritative outcome")
	}
	close(confirm)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("normal DKG did not finish after confirmation")
	}
	if len(permits.general.slots) != 0 || len(permits.dkg.slots) != 0 {
		t.Fatal("DKG permit lease not released after authoritative outcome")
	}
}

func TestUnconfirmedDKGPublicationDoesNotBlockConcurrentSIGN(t *testing.T) {
	dkgIntent := authoritativeDKGIntent()
	dkgClient := &stubClient{claimResult: claimResultForIntent(dkgIntent)}
	dkgStarted := make(chan struct{})
	cancelPublication := make(chan struct{})
	publisher := terminalPublisherFunc(func(_ context.Context, _ terminal.Job) (terminal.Outcome, error) {
		close(dkgStarted)
		<-cancelPublication
		return terminal.Outcome{}, context.Canceled
	})
	permits := newSchedulerPermits(2, nil)
	dkgLease := permits.tryAcquireDKG()
	if dkgLease == nil {
		t.Fatal("failed to acquire DKG lease")
	}
	dkgDone := make(chan struct{})
	lifecycleCtx, cancelLifecycle := context.WithCancel(context.Background())
	go func() {
		runSessionWithPermits(
			lifecycleCtx, dkgIntent, dkgClient, &stubRunner{},
			&capturingDKGExecutor{result: completeDKGResultForIntent(t, dkgIntent)},
			publisher, "co-signer", time.Millisecond, dkgLease, slog.Default(), nil,
		)
		close(dkgDone)
	}()
	select {
	case <-dkgStarted:
	case <-time.After(time.Second):
		t.Fatal("DKG publication did not start")
	}

	signLease := permits.tryAcquireSIGN()
	if signLease == nil {
		t.Fatal("remaining general capacity did not admit SIGN")
	}
	signLease.Release()
	if len(permits.dkg.slots) != 1 {
		t.Fatal("concurrent SIGN released DKG guard")
	}
	close(cancelPublication)
	cancelLifecycle()
	<-dkgDone
}

func TestAdmittedSIGNRejectsClaimedDKGWithoutRuntimeOrPublicationAndRetainsLease(t *testing.T) {
	pending := validSignIntent(t)
	pending.IntentID = "intent-mismatch"
	claimed := authoritativeDKGIntent()
	claimed.IntentID = pending.IntentID
	client := &stubClient{
		claimResult: claimResultForIntent(claimed),
	}
	signRunner := &countingSignRunner{}
	dkgRunner := &capturingDKGExecutor{}
	var publishCalls int
	publisher := terminalPublisherFunc(func(context.Context, terminal.Job) (terminal.Outcome, error) {
		publishCalls++
		return terminal.Outcome{}, nil
	})
	permits := newSchedulerPermits(1, nil)
	lease := permits.tryAcquireSIGN()
	if lease == nil {
		t.Fatal("failed to acquire SIGN lease")
	}
	logs := make(chan string, 4)
	log := slog.New(&messageHandler{messages: logs})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSessionWithPermits(
			ctx, pending, client, signRunner, dkgRunner, publisher,
			"co-signer", time.Millisecond, lease, log, nil,
		)
		close(done)
	}()

	waitForLogMessage(t, logs, "claimed intent kind mismatch")
	if signRunner.calls != 0 || dkgRunner.calls != 0 || publishCalls != 0 || client.lastResult.Status != "" {
		t.Fatalf(
			"mismatch side effects: sign=%d dkg=%d terminal=%d legacy=%+v",
			signRunner.calls, dkgRunner.calls, publishCalls, client.lastResult,
		)
	}
	if len(permits.general.slots) != 1 {
		t.Fatal("SIGN lease released while mismatched claim remained unconfirmed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mismatched SIGN worker did not stop at lifecycle shutdown")
	}
	if len(permits.general.slots) != 0 {
		t.Fatal("SIGN lease not released after lifecycle shutdown")
	}
}

func TestAdmittedDKGClaimedAsSIGNPublishesFailedBeforeReleasingGuard(t *testing.T) {
	pending := authoritativeDKGIntent()
	claimed := validSignIntent(t)
	claimed.IntentID = pending.IntentID
	claimed.SessionID = pending.SessionID
	claimed.Payload.KeyID = pending.Payload.KeyID
	client := &stubClient{claimResult: claimResultForIntent(claimed)}
	signRunner := &countingSignRunner{}
	dkgRunner := &capturingDKGExecutor{}
	started := make(chan terminal.Job, 1)
	confirm := make(chan struct{})
	publisher := terminalPublisherFunc(func(_ context.Context, job terminal.Job) (terminal.Outcome, error) {
		started <- job
		<-confirm
		return terminal.Outcome{
			Kind:                     terminal.OutcomeAccepted,
			AuthoritativeStatus:      job.Status(),
			AuthoritativeFingerprint: job.Fingerprint(),
		}, nil
	})
	permits := newSchedulerPermits(2, nil)
	lease := permits.tryAcquireDKG()
	if lease == nil {
		t.Fatal("failed to acquire DKG lease")
	}
	done := make(chan struct{})
	go func() {
		runSessionWithPermits(
			context.Background(), pending, client, signRunner, dkgRunner, publisher,
			"co-signer", time.Millisecond, lease, slog.Default(), nil,
		)
		close(done)
	}()

	var job terminal.Job
	select {
	case job = <-started:
	case <-time.After(time.Second):
		t.Fatal("mismatched admitted DKG did not publish FAILED")
	}
	if job.Status() != mpc2of3.TerminalStatusFailed {
		t.Fatalf("terminal status = %q, want FAILED", job.Status())
	}
	if signRunner.calls != 0 || dkgRunner.calls != 0 || client.lastResult.Status != "" {
		t.Fatalf("wrong-protocol side effects: sign=%d dkg=%d legacy=%+v", signRunner.calls, dkgRunner.calls, client.lastResult)
	}
	if len(permits.general.slots) != 1 || len(permits.dkg.slots) != 1 {
		t.Fatal("DKG lease released before authoritative FAILED outcome")
	}
	close(confirm)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("mismatched DKG worker did not finish after confirmation")
	}
	if len(permits.general.slots) != 0 || len(permits.dkg.slots) != 0 {
		t.Fatal("DKG lease not released after authoritative FAILED outcome")
	}
}

func TestAdmittedDKGMismatchWithoutStableIdentityRetainsLeaseUntilShutdown(t *testing.T) {
	pending := authoritativeDKGIntent()
	claimed := validSignIntent(t)
	claimed.IntentID = pending.IntentID
	claimed.SessionID = pending.SessionID
	claimed.Payload.KeyID = ""
	client := &stubClient{claimResult: claimResultForIntent(claimed)}
	signRunner := &countingSignRunner{}
	dkgRunner := &capturingDKGExecutor{}
	var publishCalls int
	publisher := terminalPublisherFunc(func(context.Context, terminal.Job) (terminal.Outcome, error) {
		publishCalls++
		return terminal.Outcome{}, nil
	})
	permits := newSchedulerPermits(2, nil)
	lease := permits.tryAcquireDKG()
	if lease == nil {
		t.Fatal("failed to acquire DKG lease")
	}
	logs := make(chan string, 4)
	log := slog.New(&messageHandler{messages: logs})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		runSessionWithPermits(
			ctx, pending, client, signRunner, dkgRunner, publisher,
			"co-signer", time.Millisecond, lease, log, nil,
		)
		close(done)
	}()

	waitForLogMessage(t, logs, "construct canonical failed dkg terminal result failed")
	if publishCalls != 0 || signRunner.calls != 0 || dkgRunner.calls != 0 {
		t.Fatalf("unstable mismatch side effects: publish=%d sign=%d dkg=%d", publishCalls, signRunner.calls, dkgRunner.calls)
	}
	if len(permits.general.slots) != 1 || len(permits.dkg.slots) != 1 {
		t.Fatal("DKG lease released without stable terminal identity")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unstable mismatched DKG did not stop at lifecycle shutdown")
	}
	if len(permits.general.slots) != 0 || len(permits.dkg.slots) != 0 {
		t.Fatal("DKG lease not released after lifecycle shutdown")
	}
}

type messageHandler struct {
	messages chan<- string
}

func (h *messageHandler) Enabled(context.Context, slog.Level) bool { return true }

func (h *messageHandler) Handle(_ context.Context, record slog.Record) error {
	h.messages <- record.Message
	return nil
}

func (h *messageHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *messageHandler) WithGroup(string) slog.Handler      { return h }

func waitForLogMessage(t *testing.T, messages <-chan string, want string) {
	t.Helper()
	for {
		select {
		case got := <-messages:
			if got == want {
				return
			}
		case <-time.After(time.Second):
			t.Fatalf("did not observe log message %q", want)
		}
	}
}

func TestRunSessionRejectsInvalidIntent(t *testing.T) {
	intent := monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Parties:   []string{"party-1", "co-signer"},
			Threshold: 2,
		},
	}
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &stubRunner{}

	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(
		context.Background(),
		intent,
		client,
		runner,
		&capturingDKGExecutor{},
		nil,
		"party-1",
		time.Millisecond,
		sem,
		make(chan struct{}, 1),
		slog.Default(),
	)

	if client.lastResult.ErrorCode != ErrorCodeInvalidIntent {
		t.Fatalf("error code = %q, want %q", client.lastResult.ErrorCode, ErrorCodeInvalidIntent)
	}
}

func TestBuildResultMapsShareNotFound(t *testing.T) {
	result := BuildResult(coretss.ErrShareNotFound, context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeShareNotFound {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeShareNotFound)
	}
}

func TestPrimarySigningArtifactFailuresEmitRedactedCriticalAlertAndDoNotDegradeReadiness(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "missing", err: coretss.ErrShareNotFound, wantCode: ErrorCodeShareNotFound},
		{name: "invalid payload", err: coretss.ErrInvalidSharePayload, wantCode: ErrorCodeInvalidSharePayload},
		{name: "binding mismatch", err: sharestore.ErrArtifactBinding, wantCode: ErrorCodeShareMetadata},
		{name: "metadata mismatch", err: coretss.ErrMetadataMismatch, wantCode: ErrorCodeShareMetadata},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := validSignIntent(t)
			intent.IntentID = "intent-secret-canary"
			intent.SessionID = "session-secret-canary"
			intent.Payload.KeyID = "key-secret-canary"
			intent.Payload.Digest = []byte("ciphertext-secret-canary")
			client := &stubClient{claimResult: claimResultForIntent(intent)}
			handler := &alertCapturingHandler{}
			sem := make(chan struct{}, 1)
			sem <- struct{}{}
			readiness := health.NewReadiness()
			initial := health.Snapshot{ProcessReady: true, SigningReady: true, ProvisioningReady: true}
			readiness.Set(initial)

			runSessionWithExecutorsForTest(
				context.Background(), intent, client, &errorSignRunner{err: tt.err},
				&capturingDKGExecutor{}, nil, coordinatorPrimaryParty, time.Millisecond, sem, nil,
				slog.New(handler),
			)

			if client.lastResult.Status != intentStatusFailed || client.lastResult.ErrorCode != tt.wantCode {
				t.Fatalf("result = %+v, want key-specific failed code %q", client.lastResult, tt.wantCode)
			}
			if got := readiness.Snapshot(); got != initial {
				t.Fatalf("primary artifact failure changed readiness: got %#v, want %#v", got, initial)
			}

			var alert *capturedLogRecord
			records := handler.Records()
			for i := range records {
				record := records[i]
				if record.level == slog.LevelError && record.message == "critical primary signing material failure" {
					alert = &record
					break
				}
			}
			if alert == nil {
				t.Fatal("missing critical primary signing artifact alert")
			}
			alertText := alert.message
			classified := false
			for _, attr := range alert.attrs {
				alertText += " " + attr.Key + "=" + attr.Value.String()
				if attr.Key == "alert_class" && attr.Value.String() == "primary_material_unavailable" {
					classified = true
				}
			}
			if !classified {
				t.Fatalf("critical alert has no primary-material classification: %+v", alert.attrs)
			}
			alertText = strings.ToLower(alertText)
			for _, forbidden := range []string{
				"key", "session", "path", "share", "ciphertext", "descriptor", "secret",
				"key-secret-canary", "session-secret-canary", "intent-secret-canary", "path-secret-canary",
				"ciphertext-secret-canary", "descriptor-secret-canary", "share-secret-canary", "secret-canary",
			} {
				if strings.Contains(alertText, forbidden) {
					t.Fatalf("critical alert leaked %q: %q", forbidden, alertText)
				}
			}

			next := validSignIntent(t)
			next.IntentID = "unrelated-sign-intent"
			next.SessionID = "unrelated-sign-session"
			nextClient := &stubClient{claimResult: claimResultForIntent(next)}
			nextSem := make(chan struct{}, 1)
			nextSem <- struct{}{}
			runSessionWithExecutorsForTest(
				context.Background(), next, nextClient, &stubRunner{}, &capturingDKGExecutor{}, nil,
				coordinatorPrimaryParty, time.Millisecond, nextSem, nil, slog.New(handler),
			)
			if nextClient.lastResult.Status != intentStatusCompleted {
				t.Fatalf("unrelated SIGN result = %+v, want completed", nextClient.lastResult)
			}
		})
	}
}

func TestBuildResultMapsKnownProtocolErrors(t *testing.T) {
	result := BuildResult(errors.New("duplicate frame"), context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeProtocol {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeProtocol)
	}
}

func TestBuildResultMapsDerivationErrorsToInvalidIntent(t *testing.T) {
	tests := []struct {
		name string
		err  error
	}{
		{name: "chain code missing", err: coretss.ErrChainCodeMissing},
		{name: "chain code invalid", err: coretss.ErrChainCodeInvalid},
		{name: "derivation context required", err: coretss.ErrDerivationContextRequired},
		{name: "invalid derivation context", err: coretss.ErrInvalidDerivationContext},
		{name: "unsupported derivation scheme", err: coretss.ErrUnsupportedDerivationScheme},
		{name: "derived signing unsupported", err: coretss.ErrDerivedSigningUnsupported},
		{name: "derivation path invalid", err: coretss.ErrDerivationPathInvalid},
		{name: "derivation context mismatch", err: coretss.ErrDerivationContextMismatch},
		{name: "unsupported algorithm curve", err: coretss.ErrUnsupportedAlgorithmCurve},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wrappedErr := fmt.Errorf("wrapped core derivation error: %w", tt.err)
			result := BuildResult(wrappedErr, context.Background(), monolith.Intent{Type: "SIGN"})
			if result.ErrorCode != ErrorCodeInvalidIntent {
				t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeInvalidIntent)
			}
		})
	}
}

func TestBuildResultUsesCanceledSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithCancel(context.Background())
	cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeWorkerShutdown {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeWorkerShutdown)
	}
}

func TestBuildResultUsesExpiredSessionContext(t *testing.T) {
	sessionCtx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := BuildResult(errors.New("transport closed"), sessionCtx, monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != ErrorCodeSessionTimeout {
		t.Fatalf("error code = %q, want %q", result.ErrorCode, ErrorCodeSessionTimeout)
	}
}

func TestValidateIntentDKGContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(*monolith.Intent)
	}{
		{name: "missing org id", edit: func(intent *monolith.Intent) { intent.Payload.OrgID = "" }},
		{name: "missing key id", edit: func(intent *monolith.Intent) { intent.Payload.KeyID = "" }},
		{name: "missing curve", edit: func(intent *monolith.Intent) { intent.Payload.Curve = "" }},
		{name: "non-empty chain", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "ethereum" }},
		{name: "missing chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = "" }},
		{name: "malformed chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = "0x11" }},
		{name: "uppercase chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("A", 64) }},
		{name: "non-hex chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("g", 64) }},
		{name: "short chain code", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("1", 63) }},
		{name: "missing chain code hash", edit: func(intent *monolith.Intent) { intent.Payload.ChainCodeHash = "" }},
		{name: "mismatched chain code hash", edit: func(intent *monolith.Intent) { intent.Payload.ChainCodeHash = "wrong" }},
		{name: "missing derivation scheme", edit: func(intent *monolith.Intent) { intent.Payload.DerivationScheme = "" }},
		{name: "unsupported derivation scheme", edit: func(intent *monolith.Intent) { intent.Payload.DerivationScheme = "bip32_public" }},
		{name: "derivation context present", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext = &monolith.DerivationContext{} }},
		{name: "digest present", edit: func(intent *monolith.Intent) { intent.Payload.Digest = []byte{1} }},
		{name: "conflicting payload type", edit: func(intent *monolith.Intent) { intent.Payload.Type = "SIGN" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := validDKGIntent()
			tt.edit(&intent)
			if err := validateIntent(intent, "co-signer"); err == nil {
				t.Fatal("validateIntent() error = nil, want error")
			}
		})
	}
}

func TestValidateIntentDKGAcceptsNormalizedPayloadTypeAndExpectedHash(t *testing.T) {
	intent := validDKGIntent()
	intent.Payload.Type = " dkg "
	intent.Payload.ChainCode = strings.Repeat("11", 32)
	intent.Payload.ChainCodeHash = "AtRJox-7JnyPNS6ZaKeePl_JXBu-qlAv1kVOveWkvtw"

	if err := validateIntent(intent, "co-signer"); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
}

func TestValidateIntentSignContract(t *testing.T) {
	tests := []struct {
		name string
		edit func(*monolith.Intent)
	}{
		{name: "missing org id", edit: func(intent *monolith.Intent) { intent.Payload.OrgID = "" }},
		{name: "missing key id", edit: func(intent *monolith.Intent) { intent.Payload.KeyID = "" }},
		{name: "missing wallet id", edit: func(intent *monolith.Intent) { intent.Payload.WalletID = "" }},
		{name: "missing profile id", edit: func(intent *monolith.Intent) { intent.Payload.ProfileID = "" }},
		{name: "zero profile version", edit: func(intent *monolith.Intent) { intent.Payload.ProfileVersion = 0 }},
		{name: "missing profile template id", edit: func(intent *monolith.Intent) { intent.Payload.ProfileTemplateID = "" }},
		{name: "missing digest type", edit: func(intent *monolith.Intent) { intent.Payload.DigestType = "" }},
		{name: "missing hash algorithm", edit: func(intent *monolith.Intent) { intent.Payload.HashAlgorithm = "" }},
		{name: "missing signing payload type", edit: func(intent *monolith.Intent) { intent.Payload.SigningPayloadType = "" }},
		{name: "missing derivation context hash", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContextHash = "" }},
		{name: "missing party id", edit: func(intent *monolith.Intent) { intent.Payload.PartyID = "" }},
		{name: "missing chain", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "" }},
		{name: "missing digest", edit: func(intent *monolith.Intent) { intent.Payload.Digest = nil }},
		{name: "missing derivation context", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext = nil }},
		{name: "invalid derivation context", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.ProfileID = "" }},
		{name: "zero nested profile version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.ProfileVersion = 0 }},
		{name: "zero descriptor version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.DescriptorVersion = 0 }},
		{name: "zero key version", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContext.KeyVersion = 0 }},
		{name: "chain conflict", edit: func(intent *monolith.Intent) { intent.Payload.Chain = "bitcoin" }},
		{name: "algorithm conflict", edit: func(intent *monolith.Intent) { intent.Payload.Algorithm = "EdDSA" }},
		{name: "curve conflict", edit: func(intent *monolith.Intent) { intent.Payload.Curve = "ed25519" }},
		{name: "profile id conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileID = "profile-2" }},
		{name: "profile template conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileTemplateID = "bitcoin-default" }},
		{name: "profile version conflict", edit: func(intent *monolith.Intent) { intent.Payload.ProfileVersion = 4 }},
		{name: "hash mismatch", edit: func(intent *monolith.Intent) { intent.Payload.DerivationContextHash = "wrong" }},
		{name: "chain code present", edit: func(intent *monolith.Intent) { intent.Payload.ChainCode = strings.Repeat("11", 32) }},
		{name: "derivation scheme present", edit: func(intent *monolith.Intent) {
			intent.Payload.DerivationScheme = coretss.DerivationSchemeBIP32Secp256k1
		}},
		{name: "conflicting payload type", edit: func(intent *monolith.Intent) { intent.Payload.Type = "DKG" }},
		{name: "recovery subset", edit: func(intent *monolith.Intent) {
			intent.Payload.Parties = []string{coordinatorPrimaryParty, coordinatorRecoveryParty}
		}},
		{name: "three party roster", edit: func(intent *monolith.Intent) {
			intent.Payload.Parties = []string{coordinatorPlatformParty, coordinatorPrimaryParty, coordinatorRecoveryParty}
		}},
		{name: "reordered roster", edit: func(intent *monolith.Intent) {
			intent.Payload.Parties = []string{coordinatorPrimaryParty, coordinatorPlatformParty}
		}},
		{name: "duplicate primary party", edit: func(intent *monolith.Intent) {
			intent.Payload.Parties = []string{coordinatorPlatformParty, coordinatorPrimaryParty, coordinatorPrimaryParty}
		}},
		{name: "foreign local party", edit: func(intent *monolith.Intent) {
			intent.Payload.Parties = []string{coordinatorPlatformParty, "foreign-primary"}
		}},
		{name: "threshold three", edit: func(intent *monolith.Intent) { intent.Payload.Threshold = 3 }},
		{name: "payload party is recovery", edit: func(intent *monolith.Intent) {
			intent.Payload.PartyID = coordinatorRecoveryParty
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intent := validSignIntent(t)
			tt.edit(&intent)
			if err := validateIntent(intent, coordinatorPrimaryParty); err == nil {
				t.Fatal("validateIntent() error = nil, want error")
			}
		})
	}
}

func TestValidateIntentSignAcceptsEmptyDKGFieldsAndNormalizedPayloadType(t *testing.T) {
	intent := validSignIntent(t)
	intent.Payload.Type = " sign "
	intent.Payload.ChainCode = ""
	intent.Payload.DerivationScheme = ""

	if err := validateIntent(intent, coordinatorPrimaryParty); err != nil {
		t.Fatalf("validateIntent() error = %v", err)
	}
}

func TestBuildSignRequestMapsHDPayload(t *testing.T) {
	intent := validSignIntent(t)
	req := buildSignRequest(intent, coordinatorPrimaryParty, nil)

	if req.Session.OrgID != "org-1" ||
		req.Session.KeyID != "key-1" ||
		req.Session.Chain != "ethereum" ||
		!sameBytes(req.Digest, []byte{1, 2, 3}) {
		t.Fatalf("unexpected sign request = %+v", req)
	}
	if req.DerivationContext == nil {
		t.Fatal("DerivationContext is nil")
	}
	ctx := req.DerivationContext
	if ctx.ProfileID != "profile-1" ||
		ctx.ProfileTemplateID != "ethereum-default" ||
		ctx.Chain != "ethereum" ||
		ctx.Algorithm != "ecdsa" ||
		ctx.Curve != "secp256k1" ||
		ctx.Scheme != coretss.DerivationSchemeBIP32Secp256k1 ||
		ctx.PublicKeyFormat != coretss.PublicKeyFormatUncompressedHex ||
		ctx.FullPath != "m/44'/60'/0'/0/15" ||
		ctx.DerivedPublicKey != intent.Payload.DerivationContext.ExpectedPublicKey ||
		ctx.DescriptorVersion != 7 ||
		ctx.ProfileVersion != 3 ||
		ctx.KeyVersion != 1 {
		t.Fatalf("unexpected derivation context = %+v", ctx)
	}
}

func TestRunSessionPublishesCanonicalCompletedDKG(t *testing.T) {
	intent := authoritativeDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: completeDKGResultForIntent(t, intent)}
	var published terminal.Job
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(context.Background(), intent, client, runner, dkgExecutor, acceptingTerminalPublisher(&published), "co-signer", time.Millisecond, sem, nil, slog.Default())

	if published.Status() != mpc2of3.TerminalStatusCompleted {
		t.Fatalf("terminal status = %q", published.Status())
	}
	if client.lastResult.Status != "" {
		t.Fatalf("legacy DKG result was posted: %+v", client.lastResult)
	}
}

func TestRunSessionUsesClaimedPayloadForDkgExecution(t *testing.T) {
	claimedIntent := authoritativeDKGIntent()
	pendingIntent := claimedIntent
	pendingIntent.Payload.ChainCode = ""
	pendingIntent.Payload.ChainCodeHash = claimedIntent.Payload.ChainCodeHash
	client := &stubClient{
		claimResult: monolith.ClaimResult{
			IntentID:  claimedIntent.IntentID,
			SessionID: claimedIntent.SessionID,
			Type:      claimedIntent.Type,
			Payload:   claimedIntent.Payload,
			Status:    "CLAIMED",
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{result: completeDKGResultForIntent(t, claimedIntent)}
	var published terminal.Job
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(context.Background(), pendingIntent, client, runner, dkgExecutor, acceptingTerminalPublisher(&published), "co-signer", time.Millisecond, sem, nil, slog.Default())

	if published.Status() != mpc2of3.TerminalStatusCompleted {
		t.Fatalf("terminal status = %q", published.Status())
	}
	if dkgExecutor.intent.Payload.ChainCode != claimedIntent.Payload.ChainCode {
		t.Fatalf("unexpected DKG derivation material = %q", dkgExecutor.intent.Payload.ChainCode)
	}
}

func TestRunSessionRejectsIncompleteClaimResponse(t *testing.T) {
	pendingIntent := validDKGIntent()
	client := &stubClient{
		claimResult: monolith.ClaimResult{
			IntentID:  pendingIntent.IntentID,
			SessionID: pendingIntent.SessionID,
			Type:      pendingIntent.Type,
			ExpiresAt: time.Now().Add(time.Minute),
		},
	}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{}
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	runSessionWithExecutorsForTest(ctx, pendingIntent, client, runner, dkgExecutor, acceptingTerminalPublisher(nil), "co-signer", time.Millisecond, sem, nil, slog.Default())

	if client.lastResult.Status != "" {
		t.Fatalf("malformed DKG claim used legacy result endpoint: %+v", client.lastResult)
	}
	if dkgExecutor.calls != 0 {
		t.Fatalf("DKG should not run for incomplete claim response, calls = %d", dkgExecutor.calls)
	}
}

func TestRunSessionPublishesMinimalFailedDKG(t *testing.T) {
	intent := authoritativeDKGIntent()
	client := &stubClient{claimResult: claimResultForIntent(intent)}
	runner := &capturingRunner{}
	dkgExecutor := &capturingDKGExecutor{err: coretss.ErrChainCodeMissing}
	var published terminal.Job
	sem := make(chan struct{}, 1)
	sem <- struct{}{}

	runSessionWithExecutorsForTest(context.Background(), intent, client, runner, dkgExecutor, acceptingTerminalPublisher(&published), "co-signer", time.Millisecond, sem, nil, slog.Default())

	if published.Status() != mpc2of3.TerminalStatusFailed {
		t.Fatalf("terminal status = %q", published.Status())
	}
	if bytes.Contains(published.Body(), []byte("error")) || bytes.Contains(published.Body(), []byte("material")) {
		t.Fatalf("FAILED terminal body contains diagnostics/material: %s", published.Body())
	}
}

func completeDKGResultForIntent(t *testing.T, intent monolith.Intent) DKGResult {
	t.Helper()
	descriptorFingerprint, err := mpc2of3.ParseDescriptorFingerprint("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	artifactFingerprint, err := mpc2of3.ParseArtifactFingerprint("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	if err != nil {
		t.Fatal(err)
	}
	publicKey := tMustDecodeHex("0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798")
	common := sharestore.ArtifactEvidence{
		SessionID:             intent.SessionID,
		KeyID:                 intent.Payload.KeyID,
		DescriptorFingerprint: descriptorFingerprint,
		AccountPublicKey:      publicKey,
		ChainCodeHash:         mpc2of3.ChainCodeHashFor(tMustDecodeHex(intent.Payload.ChainCode)),
		ArtifactFingerprint:   artifactFingerprint,
	}
	primary := common
	primary.PartyID = coordinatorPrimaryParty
	primary.Purpose = sharestore.StorePurposePrimary
	recovery := common
	recovery.PartyID = coordinatorRecoveryParty
	recovery.Purpose = sharestore.StorePurposeRecovery
	return DKGResult{Primary: primary, Recovery: recovery}
}

func authoritativeDKGIntent() monolith.Intent {
	intent := validDKGIntent()
	intent.IntentID = "intent-123"
	intent.SessionID = "dkg-123"
	intent.Payload.KeyID = "mpc_key_123e4567-e89b-42d3-a456-426614174002"
	return intent
}

func validDKGIntent() monolith.Intent {
	chainCode := strings.Repeat("11", 32)
	return monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "DKG",
		Payload: monolith.IntentPayload{
			Type:             "DKG",
			OrgID:            "org-1",
			KeyID:            "key-1",
			Parties:          []string{"party-1", "co-signer"},
			Threshold:        2,
			Algorithm:        "ECDSA",
			Curve:            "secp256k1",
			ChainCode:        chainCode,
			ChainCodeHash:    chainCodeHash(tMustDecodeHex(chainCode)),
			DerivationScheme: coretss.DerivationSchemeBIP32Secp256k1,
		},
	}
}

func validSignIntent(t *testing.T) monolith.Intent {
	t.Helper()

	ctx := monolith.DerivationContext{
		ProfileID:         "profile-1",
		ProfileTemplateID: "ethereum-default",
		Chain:             "ethereum",
		Algorithm:         "ecdsa",
		Curve:             "secp256k1",
		Scheme:            coretss.DerivationSchemeBIP32Secp256k1,
		AccountPath:       "m/44'/60'/0'",
		ChildPath:         "/0/15",
		FullPath:          "m/44'/60'/0'/0/15",
		ExpectedPublicKey: "042f8bde4d1a07209355b4a7250a5c5128e88b84bddc619ab7cba8d569b240efe4d8ac222636e5e3d6d4dba9dda6c9c426f788271bab0d6840dca87d3aa6ac62d6",
		PublicKeyFormat:   coretss.PublicKeyFormatUncompressedHex,
		DescriptorVersion: 7,
		ProfileVersion:    3,
		KeyVersion:        1,
	}
	hash, err := coretss.DerivationContextHashV1(toCoreDerivationContext(ctx))
	if err != nil {
		t.Fatalf("DerivationContextHashV1() error = %v", err)
	}

	return monolith.Intent{
		IntentID:  "intent-1",
		SessionID: "session-1",
		Type:      "SIGN",
		Payload: monolith.IntentPayload{
			Type:                  "SIGN",
			OrgID:                 "org-1",
			WalletID:              "wallet-1",
			KeyID:                 "key-1",
			ProfileID:             "profile-1",
			ProfileVersion:        3,
			ProfileTemplateID:     "ethereum-default",
			Parties:               []string{coordinatorPlatformParty, coordinatorPrimaryParty},
			Threshold:             2,
			Algorithm:             "ECDSA",
			Curve:                 "secp256k1",
			Chain:                 "ethereum",
			Digest:                []byte{1, 2, 3},
			DigestType:            "transaction_hash",
			HashAlgorithm:         "sha256",
			SigningPayloadType:    "ethereum_transaction",
			DerivationContextHash: hash,
			PartyID:               coordinatorPrimaryParty,
			DerivationContext:     &ctx,
		},
	}
}

func chainCodeHash(chainCode []byte) string {
	sum := sha256.Sum256(chainCode)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func tMustDecodeHex(input string) []byte {
	out, err := hex.DecodeString(input)
	if err != nil {
		panic(err)
	}
	return out
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sameBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
