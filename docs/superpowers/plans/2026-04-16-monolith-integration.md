# Monolith Integration Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the current gRPC-driven co-signer entrypoints with an HTTP-polling worker that claims intents from the BroSettlement monolith, runs MPC sessions over HTTP frame exchange, and posts final results back.

**Architecture:** Keep the existing `brosettlement-mpc-core/tss.Service` and share-store wiring, but move all external coordination into a new `internal/monolith` client, `internal/transport` HTTP transport, and `internal/worker` scheduler/session-worker layer. The process becomes a single health HTTP server plus background scheduler; the legacy gRPC/session/proto packages are removed once the new flow is covered by tests.

**Tech Stack:** Go 1.24, standard library `net/http`, `crypto/ed25519`, `encoding/json`, `log/slog`, `brosettlement-mpc-core/tss`.

---

## File Map

```
cmd/co-signer/
  main.go                               # replace gRPC startup with scheduler lifecycle and worker drain

internal/config/
  config.go                             # remove gRPC/api-key config, add monolith HTTP + polling config
  config_test.go                        # load/validation coverage for new env contract

internal/health/
  server.go                             # keep shares-dir readiness only; no gRPC references
  server_test.go

internal/monolith/
  types.go                              # intent/frame/result DTOs shared by client + worker
  client.go                             # signed HTTP client, retries, typed claim errors
  client_test.go                        # signing, retry, decoding, idempotency coverage

internal/transport/
  http_transport.go                     # tss.Transport over GetMessages/PostMessage polling + frame reconstruction context
  http_transport_test.go

internal/worker/
  errors.go                             # stable wire-level error code constants
  session_worker.go                     # claim -> validate -> run MPC -> result pipeline
  session_worker_test.go
  scheduler.go                          # adaptive intent polling + concurrency semaphore
  scheduler_test.go

Delete after replacement:
  internal/grpc/
  internal/session/
  internal/transport/stream_transport.go
  internal/transport/stream_transport_test.go
  proto/
  api/proto/mpc/v1/
  buf.yaml
  buf.gen.yaml
```

## Monolith Contract Assumption

This plan assumes the required `BroSettlement` API changes are already present before work starts
in this repository:

- `POST /api/v1/co-signer/intents/:intentId/claim` derives caller identity from the authenticated
  API key and keeps retries idempotent for that same caller without a `claimedBy` request body.
- `POST /api/v1/co-signer/sessions/:sessionId/messages` accepts protocol `seq` in the request body
  and returns `deliverySeq` as the delivery cursor.
- `GET /api/v1/co-signer/sessions/:sessionId/messages?afterSeq=...` filters by delivery cursor and
  returns both `deliverySeq` and protocol `seq` for each message.
- `POST /api/v1/co-signer/intents/:intentId/result` remains a terminal ack endpoint with only
  `status`, `errorCode`, and `errorMessage`.

No `BroSettlement` code changes are in scope for executing this plan; local tasks below implement
the co-signer against that already-updated contract.

### Task 1: Replace config contract for monolith mode

**Files:**
- Modify: `internal/config/config.go:8-38`
- Modify: `internal/config/config_test.go`

- [ ] **Step 1: Write the failing config tests**

```go
func TestLoadMonolithDefaults(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.MonolithURL != "https://monolith.test" {
		t.Fatalf("MonolithURL = %q", cfg.MonolithURL)
	}
	if cfg.MaxConcurrent != 4 {
		t.Fatalf("MaxConcurrent = %d", cfg.MaxConcurrent)
	}
	if cfg.PollMinInterval != 2*time.Second {
		t.Fatalf("PollMinInterval = %v", cfg.PollMinInterval)
	}
	if cfg.FramePollInterval != 500*time.Millisecond {
		t.Fatalf("FramePollInterval = %v", cfg.FramePollInterval)
	}
}

func TestLoadRequiresSigningInputs(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_SHARE_ENCRYPTION_KEY", "share-secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for missing signing credentials")
	}
}

func TestLoadRequiresShareEncryptionKey(t *testing.T) {
	t.Setenv("CO_SIGNER_MONOLITH_URL", "https://monolith.test")
	t.Setenv("CO_SIGNER_API_KEY_ID", "key-1")
	t.Setenv("CO_SIGNER_API_PRIVATE_KEY", "cHJpdmF0ZS1rZXk=")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	if _, err := config.Load(); err == nil {
		t.Fatal("expected error for missing share encryption key")
	}
}
```

- [ ] **Step 2: Run the config test target and verify it fails**

Run: `go test ./internal/config -run 'TestLoadMonolithDefaults|TestLoadRequiresSigningInputs|TestLoadRequiresShareEncryptionKey' -count=1`

Expected: FAIL because `Config` still exposes `APIKey` / `GRPCAddr` instead of the monolith settings.

- [ ] **Step 3: Replace `Config` with the monolith-specific fields**

`internal/config/config.go`:
```go
type Config struct {
	MonolithURL       string
	APIKeyID          string
	APIPrivateKey     string
	ShareEncryptionKey string
	PartyID           string
	HTTPAddr          string
	SharesDir         string
	MaxConcurrent     int
	PollMinInterval   time.Duration
	PollMaxInterval   time.Duration
	PollBackoffFactor float64
	FramePollInterval time.Duration
	HTTPTimeout       time.Duration
}

func Load() (Config, error) {
	cfg := Config{
		MonolithURL:       os.Getenv("CO_SIGNER_MONOLITH_URL"),
		APIKeyID:          os.Getenv("CO_SIGNER_API_KEY_ID"),
		APIPrivateKey:     os.Getenv("CO_SIGNER_API_PRIVATE_KEY"),
		ShareEncryptionKey: os.Getenv("CO_SIGNER_SHARE_ENCRYPTION_KEY"),
		PartyID:           os.Getenv("CO_SIGNER_PARTY_ID"),
		HTTPAddr:          envString("CO_SIGNER_HTTP_ADDR", "0.0.0.0:8081"),
		SharesDir:         envString("CO_SIGNER_SHARES_DIR", "./data/shares"),
		MaxConcurrent:     envInt("CO_SIGNER_MAX_CONCURRENT", 4),
		PollMinInterval:   envDuration("CO_SIGNER_POLL_MIN_INTERVAL", 2*time.Second),
		PollMaxInterval:   envDuration("CO_SIGNER_POLL_MAX_INTERVAL", 60*time.Second),
		PollBackoffFactor: envFloat("CO_SIGNER_POLL_BACKOFF_FACTOR", 1.5),
		FramePollInterval: envDuration("CO_SIGNER_FRAME_POLL_INTERVAL", 500*time.Millisecond),
		HTTPTimeout:       envDuration("CO_SIGNER_HTTP_TIMEOUT", 30*time.Second),
	}
	if cfg.MonolithURL == "" || cfg.APIKeyID == "" || cfg.APIPrivateKey == "" || cfg.ShareEncryptionKey == "" || cfg.PartyID == "" {
		return Config{}, errors.New("monolith URL, signing credentials, share encryption key, and party id are required")
	}
	if cfg.MaxConcurrent < 1 {
		return Config{}, errors.New("CO_SIGNER_MAX_CONCURRENT must be >= 1")
	}
	if cfg.PollMinInterval <= 0 || cfg.PollMaxInterval < cfg.PollMinInterval {
		return Config{}, errors.New("poll intervals are invalid")
	}
	return cfg, nil
}
```

- [ ] **Step 4: Re-run config tests**

Run: `go test ./internal/config -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/config/config.go internal/config/config_test.go
git commit -m "refactor: replace grpc config with monolith settings"
```

### Task 2: Add monolith DTOs and signed HTTP client

**Files:**
- Create: `internal/monolith/types.go`
- Create: `internal/monolith/client.go`
- Create: `internal/monolith/client_test.go`

- [ ] **Step 1: Write client tests for signing, retries, and the current claim/messages contract**

`internal/monolith/client_test.go`:
```go
func TestClaimIntentReturnsAlreadyClaimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	_, err := client.ClaimIntent(context.Background(), "intent-1")
	if !errors.Is(err, ErrAlreadyClaimed) {
		t.Fatalf("expected ErrAlreadyClaimed, got %v", err)
	}
}

func TestClaimIntentSendsNoBody(t *testing.T) {
	var gotContentLength int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotContentLength = r.ContentLength
		_, _ = w.Write([]byte(`{"expiresAt":"2026-04-16T12:00:00Z"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	if _, err := client.ClaimIntent(context.Background(), "intent-1"); err != nil {
		t.Fatalf("ClaimIntent() error = %v", err)
	}
	if gotContentLength > 0 {
		t.Fatalf("ClaimIntent() sent unexpected body, ContentLength = %d", gotContentLength)
	}
}

func TestPostMessageAddsSigningAndIdempotencyHeaders(t *testing.T) {
	var gotAuth, gotBodyHash, gotIdempotency string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-Api-Signature")
		gotBodyHash = r.Header.Get("X-Api-Body-Hash")
		gotIdempotency = r.Header.Get("X-Idempotency-Key")
		_, _ = w.Write([]byte(`{"deliverySeq":17}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	err := client.PostMessage(context.Background(), "session-1", OutboundFrame{
		MessageID: "msg-1",
		Seq:       9,
		Round:     2,
		ToPartyID: "co-signer",
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if gotAuth == "" || gotBodyHash == "" || gotIdempotency != "msg-1" {
		t.Fatalf("missing signing headers")
	}
}

func TestGetMessagesDecodesDeliverySeqSeparatelyFromProtocolSeq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"messages":[{"deliverySeq":11,"seq":7,"messageId":"msg-1","round":2,"fromPartyId":"co-signer","toPartyId":"party-1","payload":"YWJj"}]}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv.URL)
	msgs, err := client.GetMessages(context.Background(), "session-1", 10)
	if err != nil {
		t.Fatalf("GetMessages() error = %v", err)
	}
	if len(msgs) != 1 || msgs[0].DeliverySeq != 11 || msgs[0].Seq != 7 {
		t.Fatalf("unexpected messages = %+v", msgs)
	}
}
```

- [ ] **Step 2: Run the client test target and verify it fails**

Run: `go test ./internal/monolith -run 'TestClaimIntentReturnsAlreadyClaimed|TestClaimIntentSendsNoBody|TestPostMessageAddsSigningAndIdempotencyHeaders|TestGetMessagesDecodesDeliverySeqSeparatelyFromProtocolSeq' -count=1`

Expected: FAIL because `internal/monolith` does not exist yet.

- [ ] **Step 3: Create the transport DTOs**

`internal/monolith/types.go`:
```go
type Intent struct {
	IntentID  string        `json:"intentId"`
	SessionID string        `json:"sessionId"`
	Type      string        `json:"type"`
	ExpiresAt time.Time     `json:"expiresAt"`
	Payload   IntentPayload `json:"payload"`
}

type IntentPayload struct {
	KeyID     string   `json:"keyId"`
	Parties   []string `json:"parties"`
	Threshold uint32   `json:"threshold"`
	Algorithm string   `json:"algorithm"`
	Curve     string   `json:"curve"`
	Chain     string   `json:"chain"`
	Digest    []byte   `json:"digest"`
}

type OutboundFrame struct {
	MessageID string `json:"messageId"`
	Seq       uint64 `json:"seq"`
	Round     uint32 `json:"round"`
	ToPartyID string `json:"toPartyId,omitempty"`
	Payload   []byte `json:"payload"`
}

type InboundMessage struct {
	DeliverySeq uint64 `json:"deliverySeq"`
	Seq         uint64 `json:"seq"`
	MessageID   string `json:"messageId"`
	Round       uint32 `json:"round"`
	FromPartyID string `json:"fromPartyId"`
	ToPartyID   string `json:"toPartyId"`
	Payload     []byte `json:"payload"`
}

type ClaimResult struct {
	ExpiresAt time.Time `json:"expiresAt"`
}

type IntentResult struct {
	Status       string        `json:"status"`
	ErrorCode    string        `json:"errorCode,omitempty"`
	ErrorMessage string        `json:"errorMessage,omitempty"`
}
```

- [ ] **Step 4: Implement the client with request signing and retries**

`internal/monolith/client.go`:
```go
type Client struct {
	baseURL    string
	keyID      string
	privateKey ed25519.PrivateKey
	httpClient *http.Client
}

var (
	ErrAlreadyClaimed      = errors.New("intent already claimed")
	ErrNotFound            = errors.New("intent not found")
	ErrClaimOutcomeUnknown = errors.New("claim outcome unknown")
)

func New(baseURL, keyID string, privateKey ed25519.PrivateKey, timeout time.Duration) *Client {
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		keyID:      keyID,
		privateKey: privateKey,
		httpClient: &http.Client{Timeout: timeout},
	}
}

func (c *Client) ClaimIntent(ctx context.Context, intentID string) (ClaimResult, error) {
	req, err := c.newRequest(ctx, http.MethodPost, "/api/v1/co-signer/intents/"+intentID+"/claim", nil, intentID)
	if err != nil {
		return ClaimResult{}, err
	}
	var out ClaimResult
	if err := c.doJSON(req, &out); err != nil {
		switch {
		case statusCode(err) == http.StatusConflict:
			return ClaimResult{}, ErrAlreadyClaimed
		case statusCode(err) == http.StatusNotFound:
			return ClaimResult{}, ErrNotFound
		case isAmbiguous(err):
			return ClaimResult{}, ErrClaimOutcomeUnknown
		default:
			return ClaimResult{}, err
		}
	}
	return out, nil
}
```

- [ ] **Step 5: Re-run the monolith tests**

Run: `go test ./internal/monolith -count=1`

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/monolith
git commit -m "feat: add signed monolith client"
```

### Task 3: Replace stream transport with HTTPTransport

**Files:**
- Create: `internal/transport/http_transport.go`
- Create: `internal/transport/http_transport_test.go`
- Delete: `internal/transport/stream_transport.go`
- Delete: `internal/transport/stream_transport_test.go`

- [ ] **Step 1: Write the failing HTTP transport tests**

`internal/transport/http_transport_test.go`:
```go
func TestRecvFramePollsAndPreservesProtocolFields(t *testing.T) {
	client := &stubClient{
		messages: [][]monolith.InboundMessage{{
			{
				DeliverySeq: 11,
				Seq:         7,
				MessageID:   "msg-1",
				Round:       2,
				FromPartyID: "co-signer",
				ToPartyID:   "party-1",
				Payload:     []byte("frame"),
			},
		}},
	}
	tr := transport.NewHTTPTransport(client, transport.FrameContext{
		SessionID: "session-1",
		Stage:     "dkg",
		Protocol:  "ECDSA",
	}, time.Millisecond, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tr.Start(ctx)
	frame, err := tr.RecvFrame(context.Background())
	if err != nil {
		t.Fatalf("RecvFrame() error = %v", err)
	}
	if frame.SessionID != "session-1" || frame.Stage != "dkg" || frame.Seq != 7 || frame.FromParty != "co-signer" {
		t.Fatalf("frame was not preserved: %+v", frame)
	}
}

func TestSendFrameMapsOutboundPayload(t *testing.T) {
	client := &stubClient{}
	tr := transport.NewHTTPTransport(client, transport.FrameContext{
		SessionID: "session-1",
		Stage:     "sign",
	}, time.Millisecond, slog.Default())
	err := tr.SendFrame(context.Background(), protocol.Frame{
		MessageID: "msg-1",
		Seq:       9,
		Round:     2,
		ToParty:   "co-signer",
		Payload:   []byte("abc"),
	})
	if err != nil {
		t.Fatalf("SendFrame() error = %v", err)
	}
	if client.lastOutbound.MessageID != "msg-1" || client.lastOutbound.Seq != 9 || client.lastOutbound.ToPartyID != "co-signer" {
		t.Fatalf("unexpected outbound frame: %+v", client.lastOutbound)
	}
}
```

- [ ] **Step 2: Run transport tests and verify they fail**

Run: `go test ./internal/transport -run 'TestRecvFramePollsAndPreservesProtocolFields|TestSendFrameMapsOutboundPayload' -count=1`

Expected: FAIL because `HTTPTransport` is not implemented yet.

- [ ] **Step 3: Implement the HTTP transport**

`internal/transport/http_transport.go`:
```go
var ErrTransportClosed = errors.New("transport closed")

type FrameContext struct {
	SessionID string
	Stage     string
	Protocol  string
}

type messageClient interface {
	PostMessage(ctx context.Context, sessionID string, frame monolith.OutboundFrame) error
	GetMessages(ctx context.Context, sessionID string, afterSeq uint64) ([]monolith.InboundMessage, error)
}

type HTTPTransport struct {
	client       messageClient
	frameCtx     FrameContext
	pollInterval time.Duration
	inbound      chan protocol.Frame
	startOnce    sync.Once
	closeOnce    sync.Once
	done         chan struct{}
	log          *slog.Logger
}

func NewHTTPTransport(client messageClient, frameCtx FrameContext, pollInterval time.Duration, log *slog.Logger) *HTTPTransport {
	return &HTTPTransport{
		client:       client,
		frameCtx:     frameCtx,
		pollInterval: pollInterval,
		inbound:      make(chan protocol.Frame, 256),
		done:         make(chan struct{}),
		log:          log,
	}
}

func (t *HTTPTransport) Start(ctx context.Context) {
	t.startOnce.Do(func() {
		go t.poll(ctx)
	})
}

func (t *HTTPTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	return t.client.PostMessage(ctx, t.frameCtx.SessionID, monolith.OutboundFrame{
		MessageID: frame.MessageID,
		Seq:       frame.Seq,
		Round:     frame.Round,
		ToPartyID: frame.ToParty,
		Payload:   frame.Payload,
	})
}

func (t *HTTPTransport) toFrame(msg monolith.InboundMessage) protocol.Frame {
	return protocol.Frame{
		SessionID: t.frameCtx.SessionID,
		Stage:     t.frameCtx.Stage,
		Protocol:  t.frameCtx.Protocol,
		MessageID: msg.MessageID,
		Seq:       msg.Seq,
		Round:     msg.Round,
		FromParty: msg.FromPartyID,
		ToParty:   msg.ToPartyID,
		Payload:   msg.Payload,
	}
}
```

- [ ] **Step 4: Re-run transport tests**

Run: `go test ./internal/transport -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/transport/http_transport.go internal/transport/http_transport_test.go
git rm internal/transport/stream_transport.go internal/transport/stream_transport_test.go
git commit -m "feat: replace stream transport with http polling transport"
```

### Task 4: Add worker result mapping and session execution flow

**Files:**
- Create: `internal/worker/errors.go`
- Create: `internal/worker/session_worker.go`
- Create: `internal/worker/session_worker_test.go`

- [ ] **Step 1: Write failing session-worker tests**

`internal/worker/session_worker_test.go`:
```go
func TestRunSessionRejectsInvalidIntent(t *testing.T) {
	client := &stubClient{}
	runner := &stubRunner{}
	intent := monolith.Intent{
		IntentID: "intent-1",
		SessionID: "session-1",
		Type: "SIGN",
		Payload: monolith.IntentPayload{
			Parties: []string{"party-1", "co-signer"},
			Threshold: 2,
		},
	}

	worker.RunSession(context.Background(), intent, client, runner, "party-1", time.Millisecond, make(chan struct{}, 1), make(chan struct{}, 1), slog.Default())
	if client.lastResult.ErrorCode != worker.ErrorCodeInvalidIntent {
		t.Fatalf("ErrorCode = %q", client.lastResult.ErrorCode)
	}
}

func TestBuildResultMapsShareNotFound(t *testing.T) {
	result := worker.BuildResult(tss.ErrShareNotFound, context.Background(), monolith.Intent{Type: "SIGN"})
	if result.ErrorCode != worker.ErrorCodeShareNotFound {
		t.Fatalf("ErrorCode = %q", result.ErrorCode)
	}
}
```

- [ ] **Step 2: Run worker tests and verify they fail**

Run: `go test ./internal/worker -run 'TestRunSessionRejectsInvalidIntent|TestBuildResultMapsShareNotFound' -count=1`

Expected: FAIL because the worker package does not exist yet.

- [ ] **Step 3: Implement stable error constants and result classification**

`internal/worker/errors.go`:
```go
const (
	ErrorCodeInvalidIntent       = "INVALID_INTENT"
	ErrorCodeAlreadyExpired      = "ALREADY_EXPIRED"
	ErrorCodeSessionTimeout      = "SESSION_TIMEOUT"
	ErrorCodeWorkerShutdown      = "WORKER_SHUTDOWN"
	ErrorCodeShareNotFound       = "SHARE_NOT_FOUND"
	ErrorCodeShareDisabled       = "SHARE_DISABLED"
	ErrorCodeInvalidSharePayload = "INVALID_SHARE_PAYLOAD"
	ErrorCodeShareMetadata       = "SHARE_METADATA_MISMATCH"
	ErrorCodeMissingPublicKey    = "DKG_MISSING_PUBLIC_KEY"
	ErrorCodeMissingAddress      = "DKG_MISSING_ADDRESS"
	ErrorCodeProtocol            = "MPC_PROTOCOL_ERROR"
	ErrorCodeInternal            = "INTERNAL_ERROR"
)
```

- [ ] **Step 4: Implement `runSession`, validation helpers, and request builders**

`internal/worker/session_worker.go`:
```go
func RunSession(ctx context.Context, intent monolith.Intent, client sessionClient, runner tssRunner, localPartyID string, framePollInterval time.Duration, sem chan struct{}, repollCh chan struct{}, log *slog.Logger) {
	defer releaseSem(sem, repollCh)

	claim, err := client.ClaimIntent(ctx, intent.IntentID)
	if err != nil {
		return
	}
	if err := validateIntent(intent, localPartyID); err != nil {
		postResult(ctx, client, intent.IntentID, monolith.IntentResult{
			Status: "FAILED",
			ErrorCode: ErrorCodeInvalidIntent,
			ErrorMessage: err.Error(),
		}, log)
		return
	}

	sessionCtx, cancel := context.WithDeadline(ctx, claim.ExpiresAt)
	defer cancel()

	frameCtx := transport.FrameContext{
		SessionID: intent.SessionID,
		Stage:     strings.ToLower(intent.Type),
		Protocol:  intent.Payload.Algorithm,
	}

	tr := transport.NewHTTPTransport(client, frameCtx, framePollInterval, log)
	defer tr.Close()
	tr.Start(sessionCtx)

	var runErr error
	switch intent.Type {
	case "DKG":
		_, runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, localPartyID, tr))
	case "SIGN":
		runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
	}

	postResult(ctx, client, intent.IntentID, BuildResult(runErr, sessionCtx, intent), log)
}
```

- [ ] **Step 5: Re-run worker tests**

Run: `go test ./internal/worker -count=1`

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/worker
git commit -m "feat: add session worker lifecycle"
```

### Task 5: Add scheduler/backoff and concurrency control

**Files:**
- Create: `internal/worker/scheduler.go`
- Create: `internal/worker/scheduler_test.go`

- [ ] **Step 1: Write the failing scheduler tests**

`internal/worker/scheduler_test.go`:
```go
func TestSchedulerDispatchesOnlyAvailableSlots(t *testing.T) {
	client := &stubPendingClient{
		intents: []monolith.Intent{
			{IntentID: "1", SessionID: "s1", Type: "DKG"},
			{IntentID: "2", SessionID: "s2", Type: "DKG"},
		},
	}
	s := worker.NewScheduler(client, &stubRunner{}, "party-1", time.Millisecond, worker.SchedulerConfig{
		MinInterval: time.Millisecond,
		MaxInterval: 5 * time.Millisecond,
		BackoffFactor: 2,
	}, slog.Default(), 1)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)

	require.Eventually(t, func() bool { return client.claimCalls >= 1 }, time.Second, time.Millisecond)
	if client.claimCalls > 1 {
		t.Fatalf("claimCalls = %d, want at most 1 active worker", client.claimCalls)
	}
}
```

- [ ] **Step 2: Run scheduler tests and verify they fail**

Run: `go test ./internal/worker -run 'TestSchedulerDispatchesOnlyAvailableSlots' -count=1`

Expected: FAIL because `Scheduler` is not implemented yet.

- [ ] **Step 3: Implement `Scheduler` and `nextBackoff`**

`internal/worker/scheduler.go`:
```go
type SchedulerConfig struct {
	MinInterval   time.Duration
	MaxInterval   time.Duration
	BackoffFactor float64
}

type Scheduler struct {
	client            pendingClient
	runner            tssRunner
	localPartyID      string
	framePollInterval time.Duration
	sem               chan struct{}
	repollCh          chan struct{}
	cfg               SchedulerConfig
	log               *slog.Logger
}

func (s *Scheduler) Run(ctx context.Context) {
	backoff := s.cfg.MinInterval
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		case <-s.repollCh:
		}

		intents, err := s.client.GetPendingIntents(ctx)
		if err != nil {
			backoff = nextBackoff(backoff, s.cfg)
			continue
		}
		if len(intents) == 0 {
			backoff = nextBackoff(backoff, s.cfg)
			continue
		}
		backoff = s.cfg.MinInterval
	dispatch:
		for _, intent := range intents {
			select {
			case s.sem <- struct{}{}:
				go RunSession(ctx, intent, s.client, s.runner, s.localPartyID, s.framePollInterval, s.sem, s.repollCh, s.log)
			default:
				break dispatch
			}
		}
	}
}
```

- [ ] **Step 4: Re-run scheduler tests**

Run: `go test ./internal/worker -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/worker/scheduler.go internal/worker/scheduler_test.go
git commit -m "feat: add monolith intent scheduler"
```

### Task 6: Rewrite process wiring and health startup for background worker mode

**Files:**
- Modify: `cmd/co-signer/main.go:1-132`
- Modify: `internal/health/server.go:16-76`
- Modify: `internal/health/server_test.go`

- [ ] **Step 1: Write failing main/health tests**

`internal/health/server_test.go`:
```go
func TestHealthStillChecksSharesDirAfterGrpcRemoval(t *testing.T) {
	dir := t.TempDir()
	h := health.NewHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
}
```

`cmd/co-signer/main_test.go`:
```go
func TestDrainWorkersConsumesAllSemaphoreSlots(t *testing.T) {
	sem := make(chan struct{}, 2)
	sem <- struct{}{}
	go func() {
		time.Sleep(time.Millisecond)
		<-sem
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := drainWorkers(ctx, sem); err != nil {
		t.Fatalf("drainWorkers() error = %v", err)
	}
}
```

- [ ] **Step 2: Run the affected tests and verify they fail**

Run: `go test ./cmd/co-signer ./internal/health -count=1`

Expected: FAIL because `main.go` still depends on `internal/grpc` and `internal/session`.

- [ ] **Step 3: Rewrite `main.go` around client + scheduler startup**

`cmd/co-signer/main.go`:
```go
func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	privateKey, err := decodePrivateKey(cfg.APIPrivateKey)
	if err != nil {
		log.Error("invalid private key", "err", err)
		os.Exit(1)
	}
	shareStore, err := sharestore.NewFileStore(cfg.SharesDir, shareEncryptionKey(cfg.ShareEncryptionKey))
	if err != nil {
		log.Error("failed to initialize share store", "err", err)
		os.Exit(1)
	}

	tssSvc := coretss.NewBnbService(log, coretss.WithShareStore(shareStore))
	client := monolith.New(cfg.MonolithURL, cfg.APIKeyID, privateKey, cfg.HTTPTimeout)
	scheduler := worker.NewScheduler(client, tssSvc, cfg.PartyID, cfg.FramePollInterval, worker.SchedulerConfig{
		MinInterval: cfg.PollMinInterval,
		MaxInterval: cfg.PollMaxInterval,
		BackoffFactor: cfg.PollBackoffFactor,
	}, log, cfg.MaxConcurrent)

	go scheduler.Run(ctx)
	go serveHealth(log, &http.Server{Addr: cfg.HTTPAddr, Handler: health.NewHandler(version, cfg.SharesDir)})
	<-ctx.Done()
	_ = drainWorkers(shutdownCtx, scheduler.Semaphore())
}
```

- [ ] **Step 4: Re-run main and health tests**

Run: `go test ./cmd/co-signer ./internal/health -count=1`

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add cmd/co-signer/main.go cmd/co-signer/main_test.go internal/health/server.go internal/health/server_test.go
git commit -m "refactor: run cosigner as monolith polling worker"
```

### Task 7: Remove legacy gRPC/proto/session surface and verify repo-wide behavior

**Files:**
- Delete: `internal/grpc/auth.go`
- Delete: `internal/grpc/auth_test.go`
- Delete: `internal/grpc/control.go`
- Delete: `internal/grpc/control_test.go`
- Delete: `internal/grpc/relay.go`
- Delete: `internal/grpc/relay_test.go`
- Delete: `internal/grpc/server.go`
- Delete: `internal/session/store.go`
- Delete: `internal/session/store_test.go`
- Delete: `proto/mpc.v1.proto`
- Delete: `api/proto/mpc/v1/mpc.v1.pb.go`
- Delete: `api/proto/mpc/v1/mpc.v1_grpc.pb.go`
- Delete: `buf.yaml`
- Delete: `buf.gen.yaml`
- Modify: `go.mod`
- Modify: `go.sum`
- Modify: `README.md`

- [ ] **Step 1: Remove legacy files and gRPC dependencies**

```bash
git rm -r internal/grpc internal/session proto api/proto/mpc/v1 buf.yaml buf.gen.yaml
go mod edit -droprequire=google.golang.org/grpc
go mod edit -droprequire=google.golang.org/protobuf
go mod tidy
```

Expected: deleted files staged; `go.mod` and `go.sum` no longer require gRPC/protobuf directly.

- [ ] **Step 2: Update README usage to the new HTTP polling model**

`README.md` snippet:
```md
## Runtime model

`brosettlement-mpc-co-signer` no longer exposes a gRPC API. It polls the BroSettlement monolith
for pending intents, claims work over signed HTTP requests, exchanges MPC frames through the
monolith message endpoints, and posts final results back.
```

- [ ] **Step 3: Run focused and repo-wide verification**

Run: `go test ./internal/config ./internal/monolith ./internal/transport ./internal/worker ./internal/health ./cmd/co-signer -count=1`

Expected: PASS

Run: `go test ./... -count=1`

Expected: PASS

Run: `go build ./...`

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add README.md go.mod go.sum
git commit -m "refactor: remove grpc interface for monolith integration"
```

## Self-Review

- Spec coverage:
  Task 1 covers the new environment contract and removes `CO_SIGNER_API_KEY` / `CO_SIGNER_GRPC_ADDR`.
  The Monolith Contract Assumption section makes the required `BroSettlement` wire contract explicit before local implementation starts.
  Task 2 covers all five monolith endpoints, signing headers, idempotency keys, retry semantics, typed claim errors, and the split between protocol `seq` and delivery `deliverySeq`.
  Task 3 covers the two-sequence transport model, immutable frame reconstruction context, and replacement of gRPC frame streaming with HTTP polling.
  Task 4 covers claim lifecycle, intent validation, party checks, session deadline handling, frame-context construction from intent metadata, and terminal result mapping without DKG output on the co-signer result endpoint.
  Task 5 covers adaptive pending-intent polling, repoll signaling, and bounded concurrency.
  Task 6 covers process startup, graceful shutdown, and retained health behavior.
  Task 7 covers removal of obsolete gRPC/proto/session assets and full verification.
- Placeholder scan: no `TODO`/`TBD` placeholders remain; each task includes concrete files, commands, and code.
- Type consistency: the plan uses `monolith.Intent`, `monolith.IntentResult`, `transport.NewHTTPTransport`, `worker.RunSession`, `worker.BuildResult`, and `worker.SchedulerConfig` consistently across later tasks.
  `Task 2` defines the full `monolith` DTO contract used later (`ClaimResult`, `OutboundFrame`,
  `InboundMessage`, `IntentResult`) so `Task 3` and `Task 4` do not rely on undeclared types.
