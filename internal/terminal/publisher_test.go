package terminal

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/contract/mpc2of3"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/monolith"
)

const (
	fixtureIntentID  = "intent-123"
	fixtureSessionID = "123e4567-e89b-42d3-a456-426614174123"
	fixtureKeyID     = "mpc_key_123e4567-e89b-42d3-a456-426614174002"
)

type recordingPolicy struct {
	mu    sync.Mutex
	kinds []RetryKind
}

func (p *recordingPolicy) NextDelay(_ uint64, kind RetryKind) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.kinds = append(p.kinds, kind)
	return 0
}

func (p *recordingPolicy) snapshot() []RetryKind {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]RetryKind(nil), p.kinds...)
}

type immediateSleeper struct{}

func (immediateSleeper) Sleep(ctx context.Context, _ time.Duration) error {
	return ctx.Err()
}

type alertRecorder struct {
	mu     sync.Mutex
	alerts []ProtocolAlert
}

func (r *alertRecorder) Alert(alert ProtocolAlert) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.alerts = append(r.alerts, alert)
}

func (r *alertRecorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.alerts)
}

func TestCanonicalJobsMatchBackendOwnedFixturesAndOmitDiagnostics(t *testing.T) {
	completed, err := NewCompletedJob(CompletedInput{
		IntentID:              fixtureIntentID,
		SessionID:             fixtureSessionID,
		KeyID:                 fixtureKeyID,
		DescriptorFingerprint: mustDescriptorFingerprint(t, "owXeRUkKctags_JkTP2Xq7uiGEFz6riO1ZhW5jsg9tQ"),
		AccountPublicKey:      mustHex(t, "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"),
		ChainCodeHash:         mustChainCodeHash(t, "Zmh6rfhivXdsj8GLjp-OIAiXFIVu4jOzkCpZHQ1fKSU"),
		Primary: ArtifactInput{
			PartyID:     "co-signer-primary",
			Purpose:     "primary",
			Fingerprint: mustArtifactFingerprint(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		},
		Recovery: ArtifactInput{
			PartyID:     "co-signer-recovery",
			Purpose:     "recovery",
			Fingerprint: mustArtifactFingerprint(t, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"),
		},
	})
	if err != nil {
		t.Fatalf("NewCompletedJob() error = %v", err)
	}
	failed, err := NewFailedJob(fixtureIntentID, fixtureSessionID, fixtureKeyID)
	if err != nil {
		t.Fatalf("NewFailedJob() error = %v", err)
	}

	assertFixtureBytes(t, completed.Body(), "terminal-completed-request.json")
	assertFixtureBytes(t, failed.Body(), "terminal-failed-request.json")
	for _, forbidden := range [][]byte{
		[]byte("diagnostic"),
		[]byte("errorCode"),
		[]byte("errorMessage"),
		[]byte("descriptorBytes"),
		[]byte("share"),
		[]byte("ciphertext"),
		[]byte("encryptionKey"),
	} {
		if bytes.Contains(completed.Body(), forbidden) || bytes.Contains(failed.Body(), forbidden) {
			t.Fatalf("wire body contains forbidden field/material %q", forbidden)
		}
	}
}

func TestPublisherRetriesCommitThenEOFByteIdenticallyUntilExactReplay(t *testing.T) {
	job := fixtureCompletedJob(t)
	var bodies [][]byte
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("ReadAll() error = %v", err)
			return
		}
		bodies = append(bodies, append([]byte(nil), body...))
		if attempts.Add(1) == 1 {
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Error("response writer cannot hijack")
				return
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Errorf("Hijack() error = %v", err)
				return
			}
			_ = conn.Close()
			return
		}
		writeFixture(t, w, http.StatusOK, "replay-response.json")
	}))
	defer server.Close()

	publisher := newHTTPPublisher(t, server.URL, &recordingPolicy{}, immediateSleeper{}, nil)
	outcome, err := publisher.Publish(context.Background(), job)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if outcome.Kind != OutcomeExactReplay || outcome.AuthoritativeStatus != mpc2of3.TerminalStatusCompleted {
		t.Fatalf("outcome = %+v", outcome)
	}
	if len(bodies) != 2 || !bytes.Equal(bodies[0], job.Body()) || !bytes.Equal(bodies[0], bodies[1]) {
		t.Fatalf("retry bodies differ: %q / %q", bodies[0], bodies[1])
	}
}

func TestPublisherHasNoAttemptLimitAcross5xxSequence(t *testing.T) {
	job := fixtureCompletedJob(t)
	const failures = 32
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) <= failures {
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
			return
		}
		writeFixture(t, w, http.StatusOK, "replay-response.json")
	}))
	defer server.Close()
	policy := &recordingPolicy{}

	outcome, err := newHTTPPublisher(t, server.URL, policy, immediateSleeper{}, nil).
		Publish(context.Background(), job)
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if outcome.Kind != OutcomeExactReplay || attempts.Load() != failures+1 {
		t.Fatalf("outcome = %+v, attempts = %d", outcome, attempts.Load())
	}
	for _, kind := range policy.snapshot() {
		if kind != RetryTransient {
			t.Fatalf("retry kind = %v, want transient", kind)
		}
	}
}

func TestPublisherRetriesTimeoutAndEOF(t *testing.T) {
	responses := []error{context.DeadlineExceeded, io.EOF, nil}
	var attempts int
	sender := senderFunc(func(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error) {
		err := responses[attempts]
		attempts++
		if err != nil {
			return monolith.TerminalHTTPResponse{}, err
		}
		return fixtureHTTPResponse(t, http.StatusOK, "accepted-response.json"), nil
	})
	policy := &recordingPolicy{}

	outcome, err := NewPublisher(sender, policy, immediateSleeper{}, nil).
		Publish(context.Background(), fixtureCompletedJob(t))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if outcome.Kind != OutcomeAccepted || attempts != 3 {
		t.Fatalf("outcome = %+v, attempts = %d", outcome, attempts)
	}
	if got := policy.snapshot(); len(got) != 2 || got[0] != RetryTransient || got[1] != RetryTransient {
		t.Fatalf("retry kinds = %v", got)
	}
}

func TestPublisherAcceptsOnlyBackendOwnedAuthoritative200Outcomes(t *testing.T) {
	tests := []struct {
		fixture string
		want    OutcomeKind
	}{
		{fixture: "accepted-response.json", want: OutcomeAccepted},
		{fixture: "replay-response.json", want: OutcomeExactReplay},
	}
	for _, tt := range tests {
		t.Run(tt.fixture, func(t *testing.T) {
			sender := senderFunc(func(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error) {
				return fixtureHTTPResponse(t, http.StatusOK, tt.fixture), nil
			})
			outcome, err := NewPublisher(sender, &recordingPolicy{}, immediateSleeper{}, nil).
				Publish(context.Background(), fixtureCompletedJob(t))
			if err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			if outcome.Kind != tt.want {
				t.Fatalf("outcome = %+v, want kind %q", outcome, tt.want)
			}
		})
	}
}

func TestPublisherProtocolAlertsAndSlowRetriesMalformedOrUnexpectedResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{name: "malformed 200", status: http.StatusOK, body: `{"outcome":"EXACT_REPLAY"}`},
		{name: "unknown field 200", status: http.StatusOK, body: `{"authoritativeResultFingerprint":"xx0XKjmRzBHaiRDVPNRz6qA07rRLru9u0PPoqd9GMSo","authoritativeStatus":"FAILED","httpStatus":200,"outcome":"EXACT_REPLAY","unknown":true}`},
		{name: "malformed 409", status: http.StatusConflict, body: `{"outcome":"TERMINAL_CONFLICT"}`},
		{name: "unexpected 4xx", status: http.StatusForbidden, body: `{"error":"forbidden"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.WriteHeader(tt.status)
					_, _ = io.WriteString(w, tt.body)
					return
				}
				writeFixture(t, w, http.StatusOK, "replay-response.json")
			}))
			defer server.Close()
			policy := &recordingPolicy{}
			alerts := &alertRecorder{}

			outcome, err := newHTTPPublisher(t, server.URL, policy, immediateSleeper{}, alerts).
				Publish(context.Background(), fixtureCompletedJob(t))
			if err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			if outcome.Kind != OutcomeExactReplay {
				t.Fatalf("outcome = %+v", outcome)
			}
			if got := policy.snapshot(); len(got) != 1 || got[0] != RetryProtocol {
				t.Fatalf("retry kinds = %v, want one protocol retry", got)
			}
			if alerts.count() != 1 {
				t.Fatalf("protocol alerts = %d, want 1", alerts.count())
			}
		})
	}
}

func TestPublisherRejectsDuplicateAuthoritativeResponseFieldsWithSlowRetry(t *testing.T) {
	fields := []string{
		`"outcome":"EXACT_REPLAY"`,
		`"authoritativeStatus":"COMPLETED"`,
		`"authoritativeResultFingerprint":"qQ-8-uXoRxlOUaJKqKRVlycQvqMwP6tNQ1bb6FDl7E0"`,
		`"httpStatus":200`,
	}
	for _, field := range fields {
		t.Run(field, func(t *testing.T) {
			fixture := string(readFixture(t, "replay-response.json"))
			duplicate := strings.Replace(fixture, field, field+","+field, 1)
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if attempts.Add(1) == 1 {
					w.WriteHeader(http.StatusOK)
					_, _ = io.WriteString(w, duplicate)
					return
				}
				writeFixture(t, w, http.StatusOK, "replay-response.json")
			}))
			defer server.Close()
			policy := &recordingPolicy{}
			alerts := &alertRecorder{}

			outcome, err := newHTTPPublisher(t, server.URL, policy, immediateSleeper{}, alerts).
				Publish(context.Background(), fixtureCompletedJob(t))
			if err != nil {
				t.Fatalf("Publish() error = %v", err)
			}
			if outcome.Kind != OutcomeExactReplay || attempts.Load() != 2 {
				t.Fatalf("outcome = %+v, attempts = %d", outcome, attempts.Load())
			}
			if got := policy.snapshot(); len(got) != 1 || got[0] != RetryProtocol {
				t.Fatalf("retry kinds = %v, want protocol", got)
			}
			if alerts.count() != 1 {
				t.Fatalf("protocol alerts = %d, want 1", alerts.count())
			}
		})
	}
}

func TestPublisherTreatsOversizedResponseAsSlowProtocolRetry(t *testing.T) {
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if attempts.Add(1) == 1 {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(bytes.Repeat([]byte("x"), 16<<10))
			return
		}
		writeFixture(t, w, http.StatusOK, "replay-response.json")
	}))
	defer server.Close()
	policy := &recordingPolicy{}
	alerts := &alertRecorder{}

	outcome, err := newHTTPPublisher(t, server.URL, policy, immediateSleeper{}, alerts).
		Publish(context.Background(), fixtureCompletedJob(t))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if outcome.Kind != OutcomeExactReplay {
		t.Fatalf("outcome = %+v", outcome)
	}
	if got := policy.snapshot(); len(got) != 1 || got[0] != RetryProtocol {
		t.Fatalf("retry kinds = %v, want protocol", got)
	}
	if alerts.count() != 1 {
		t.Fatalf("protocol alerts = %d, want 1", alerts.count())
	}
}

func TestPublisherReturnsTypedConflictWithAuthoritativeWinner(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeFixture(t, w, http.StatusConflict, "conflict-response.json")
	}))
	defer server.Close()

	alerts := &alertRecorder{}
	outcome, err := newHTTPPublisher(t, server.URL, &recordingPolicy{}, immediateSleeper{}, alerts).
		Publish(context.Background(), fixtureCompletedJob(t))
	if err != nil {
		t.Fatalf("Publish() error = %v", err)
	}
	if outcome.Kind != OutcomeTerminalConflict ||
		outcome.AuthoritativeStatus != mpc2of3.TerminalStatusFailed ||
		outcome.AuthoritativeFingerprint.String() != "iFdDYnsaTdf44zp0M55qfYVIu_R2J7JhNVNfFWa0RbI" {
		t.Fatalf("outcome = %+v", outcome)
	}
	if alerts.count() != 1 {
		t.Fatalf("integrity alerts = %d, want 1", alerts.count())
	}
}

func TestPublisherCancellationPreventsAnotherAttempt(t *testing.T) {
	job := fixtureFailedJob(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var attempts atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		attempts.Add(1)
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	sleeper := sleeperFunc(func(context.Context, time.Duration) error {
		cancel()
		return context.Canceled
	})

	_, err := newHTTPPublisher(t, server.URL, &recordingPolicy{}, sleeper, nil).Publish(ctx, job)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish() error = %v, want context.Canceled", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("attempts = %d, want 1", attempts.Load())
	}
}

func TestSingleSlotRequiresStartAndRejectsConcurrentHandoff(t *testing.T) {
	block := make(chan struct{})
	sender := senderFunc(func(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error) {
		<-block
		return monolith.TerminalHTTPResponse{}, context.Canceled
	})
	slot, err := NewSingleSlot(NewPublisher(sender, &recordingPolicy{}, immediateSleeper{}, nil))
	if err != nil {
		t.Fatalf("NewSingleSlot() error = %v", err)
	}
	if err := slot.Handoff(context.Background(), fixtureFailedJob(t), nil); !errors.Is(err, ErrPublisherNotStarted) {
		t.Fatalf("Handoff() before Start error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := slot.Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}
	if err := slot.Start(ctx); !errors.Is(err, ErrPublisherAlreadyStarted) {
		t.Fatalf("second Start() error = %v", err)
	}
	if err := slot.Handoff(context.Background(), fixtureFailedJob(t), nil); err != nil {
		t.Fatalf("Handoff() error = %v", err)
	}
	if err := slot.Handoff(context.Background(), fixtureFailedJob(t), nil); !errors.Is(err, ErrPublisherSlotOccupied) {
		t.Fatalf("concurrent Handoff() error = %v", err)
	}
	cancel()
	close(block)
	slot.Wait()
}

func TestSingleSlotStartupJobPublishesWithoutSchedulerPermit(t *testing.T) {
	called := make(chan struct{})
	sender := senderFunc(func(_ context.Context, _ string, _ []byte) (monolith.TerminalHTTPResponse, error) {
		close(called)
		return fixtureHTTPResponse(t, http.StatusOK, "replay-response.json"), nil
	})
	slot, err := NewSingleSlot(NewPublisher(sender, &recordingPolicy{}, immediateSleeper{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := slot.Start(ctx); err != nil {
		t.Fatal(err)
	}
	done := make(chan PublishResult, 1)
	if err := slot.Handoff(context.Background(), fixtureCompletedJob(t), func(result PublishResult) {
		done <- result
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("startup job did not start")
	}
	select {
	case result := <-done:
		if result.Err != nil || result.Outcome.Kind != OutcomeExactReplay {
			t.Fatalf("result = %+v", result)
		}
	case <-time.After(time.Second):
		t.Fatal("startup job did not complete")
	}
}

func TestSingleSlotWithoutHandoffDoesNotPublishAlreadyTerminalIntent(t *testing.T) {
	var calls atomic.Int32
	sender := senderFunc(func(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error) {
		calls.Add(1)
		return monolith.TerminalHTTPResponse{}, nil
	})
	slot, err := NewSingleSlot(NewPublisher(sender, &recordingPolicy{}, immediateSleeper{}, nil))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := slot.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	slot.Wait()
	if calls.Load() != 0 {
		t.Fatalf("publish calls = %d, want 0", calls.Load())
	}
}

func TestParseJobRejectsUnknownRequestFields(t *testing.T) {
	raw := readFixture(t, "terminal-failed-request.json")
	raw = append(raw[:len(raw)-1], []byte(`,"diagnostic":"must-not-be-on-wire"}`)...)
	if _, err := ParseJob(raw); err == nil {
		t.Fatal("ParseJob() accepted unknown diagnostic field")
	}
}

func TestParseJobRejectsDuplicateWrapperFields(t *testing.T) {
	fixture := string(readFixture(t, "terminal-failed-request.json"))
	for _, field := range []string{
		`"terminalResultFingerprint":"iFdDYnsaTdf44zp0M55qfYVIu_R2J7JhNVNfFWa0RbI"`,
		`"terminalResult":{"intentId":"intent-123","keyId":"mpc_key_123e4567-e89b-42d3-a456-426614174002","resultKind":"mpc-dkg-terminal-result","resultVersion":1,"sessionId":"123e4567-e89b-42d3-a456-426614174123","status":"FAILED"}`,
	} {
		duplicate := strings.Replace(fixture, field, field+","+field, 1)
		if _, err := ParseJob([]byte(duplicate)); err == nil {
			t.Fatalf("ParseJob() accepted duplicate wrapper field %s", field)
		}
	}
}

func TestDecodeStrictRejectsDuplicateObjectKeys(t *testing.T) {
	var target struct {
		Outer map[string]int `json:"outer"`
	}
	for _, raw := range []string{
		`{"outer":{"value":1},"outer":{"value":1}}`,
		`{"outer":{"value":1,"value":1}}`,
	} {
		if err := decodeStrict([]byte(raw), &target); err == nil {
			t.Fatalf("decodeStrict(%s) accepted duplicate key", raw)
		}
	}
}

type sleeperFunc func(context.Context, time.Duration) error

func (f sleeperFunc) Sleep(ctx context.Context, delay time.Duration) error {
	return f(ctx, delay)
}

type senderFunc func(context.Context, string, []byte) (monolith.TerminalHTTPResponse, error)

func (f senderFunc) PostTerminalResult(ctx context.Context, intentID string, body []byte) (monolith.TerminalHTTPResponse, error) {
	return f(ctx, intentID, body)
}

func newHTTPPublisher(t *testing.T, baseURL string, policy RetryPolicy, sleeper Sleeper, alerts ProtocolAlerter) *Publisher {
	t.Helper()
	seed := make([]byte, ed25519.SeedSize)
	client := monolith.New(baseURL, "11111111-2222-3333-4444-555555555555", ed25519.NewKeyFromSeed(seed), time.Second)
	return NewPublisher(client, policy, sleeper, alerts)
}

func fixtureFailedJob(t *testing.T) Job {
	t.Helper()
	job, err := NewFailedJob(fixtureIntentID, fixtureSessionID, fixtureKeyID)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func fixtureCompletedJob(t *testing.T) Job {
	t.Helper()
	body := readFixture(t, "terminal-completed-request.json")
	job, err := ParseJob(body)
	if err != nil {
		t.Fatal(err)
	}
	return job
}

func fixtureHTTPResponse(t *testing.T, status int, name string) monolith.TerminalHTTPResponse {
	t.Helper()
	return monolith.TerminalHTTPResponse{StatusCode: status, Body: readFixture(t, name)}
}

func assertFixtureBytes(t *testing.T, got []byte, name string) {
	t.Helper()
	want := readFixture(t, name)
	if !bytes.Equal(got, want) {
		t.Fatalf("bytes = %s\nwant = %s", got, want)
	}
}

func writeFixture(t *testing.T, w http.ResponseWriter, status int, name string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(readFixture(t, name))
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "mpc-co-signer-http", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustDescriptorFingerprint(t *testing.T, raw string) mpc2of3.DescriptorFingerprint {
	t.Helper()
	value, err := mpc2of3.ParseDescriptorFingerprint(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustArtifactFingerprint(t *testing.T, raw string) mpc2of3.ArtifactFingerprint {
	t.Helper()
	value, err := mpc2of3.ParseArtifactFingerprint(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustChainCodeHash(t *testing.T, raw string) mpc2of3.ChainCodeHash {
	t.Helper()
	value, err := mpc2of3.ParseChainCodeHash(raw)
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func mustHex(t *testing.T, raw string) []byte {
	t.Helper()
	decoded, err := hex.DecodeString(raw)
	if err != nil {
		t.Fatal(err)
	}
	return decoded
}
