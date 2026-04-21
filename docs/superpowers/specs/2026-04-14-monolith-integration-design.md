# MPC Co-Signer: Monolith Integration Design

## Goal

Describe the current HTTP polling model where `mpc-co-signer` actively fetches work from the
BroSettlement monolith, participates in MPC sessions over HTTP-based frame exchange, and reports
results back.

The HTTP API of the monolith is the **sole external contract** of this service.

---

## Context

### Previous model

BroSettlement previously initiated work through a server-driven control API and a bidirectional
relay stream. That passive server model has been removed from the repository.

### New model

`mpc-co-signer` becomes the **initiator**:
1. Polls `GET /api/v1/co-signer/intents/pending` for work from the shared pending queue of this co-signer deployment
2. Claims intents atomically via `POST /intents/:id/claim`
3. Drives MPC protocol rounds by polling inbound frames and pushing outbound frames over HTTP
4. Reports final status via `POST /intents/:id/result`

### Deployment model

Single process, single instance for MVP. No distributed coordination required.

The service represents a single logical co-signer party. All instances (if horizontally scaled)
operate under the same `CO_SIGNER_PARTY_ID` and are considered replicas of the same logical party.
The monolith exposes a shared pending queue for this deployment, and `POST /claim` compare-and-set
is used only for coordination between replicas of this same party. Supporting multiple distinct
co-signer parties is out of scope and not supported by this design.

### Crash recovery

On restart, the service polls only `PENDING` intents. Intents left `CLAIMED` by a crashed instance
are handled by the monolith's timeout sweep. The service takes no action to rehydrate or explicitly
fail orphaned intents.

---

## What Changes

### Removed legacy surface

The old server-side control layer, relay transport, generated RPC contract, and in-process session
state have all been removed. Runtime state still exists where needed, but now lives only in the
current transport and worker layers.

### Added

```
internal/monolith/
  client.go               — HTTP client: all 5 endpoints + Ed25519 request signing
  client_test.go
  types.go                — Intent, OutboundFrame, InboundMessage, IntentResult structs

internal/transport/
  http_transport.go       — HTTPTransport: implements tss.Transport over HTTP polling
  http_transport_test.go

internal/worker/
  scheduler.go            — Scheduler: adaptive backoff loop, semaphore, dispatch
  scheduler_test.go
  session_worker.go       — runSession: claim → MPC → result lifecycle
  session_worker_test.go
```

### Modified

```
internal/config/config.go   — extended with new variables, gRPC variables removed
internal/health/server.go   — updated checks (gRPC removed; shares_dir retained)
cmd/co-signer/main.go       — rewritten: remove gRPC wiring, wire Scheduler
```

### Unchanged

```
internal/sharestore/file_store.go
```

---

## Data Flow

```
main.go
  ├── creates MonolithClient (URL, Ed25519 key, HTTP timeout)
  ├── creates Scheduler (client, tssRunner, localPartyID, framePollInterval, semaphore, backoff config)
  └── go scheduler.Run(ctx)

Scheduler
  ├── adaptive-backoff ticker OR repollCh signal
  ├── client.GetPendingIntents(ctx)     → []Intent   [side-effect free]
  └── for each intent while slots available:
        acquire semaphore
        go runSession(ctx, intent, ...)

runSession goroutine
  ├── defer releaseSem(sem, repollCh)   [always: release slot + best-effort repoll signal]
  ├── client.ClaimIntent(ctx, intentID) [single point of work capture]
  │     ErrAlreadyClaimed               → log + return  (another worker owns the claim)
  │     ErrNotFound                     → log + return  (intent was removed or expired)
  │     ErrClaimOutcomeUnknown          → log + return  (worker must not assume it owns the claim)
  ├── [intent is now CLAIMED — all subsequent errors lead to PostResult or monolith timeout]
  ├── sessionCtx = derived from claim.expiresAt
  ├── frameCtx = derived from claimed intent metadata
  ├── tr := NewHTTPTransport(client, frameCtx, framePollInterval, log)
  ├── defer tr.Close()
  ├── tr.Start(sessionCtx)              [idempotent, always succeeds, no error returned]
  ├── tssRunner.Run{DKG,Sign}Session(sessionCtx, ..., localPartyID, tr)
  ├── client.PostResult(ctx, intentID, result)
  │     on failure: best-effort; intent stays CLAIMED until monolith timeout sweep
  └── [releaseSem fires via defer]
```

---

## Component Designs

### MonolithClient (`internal/monolith/client.go`)

The only layer with knowledge of the monolith HTTP contract.

```go
type Client struct {
    baseURL    string
    keyID      string
    privateKey ed25519.PrivateKey
    httpClient *http.Client   // configured timeout
}
```

**Methods:**

```
GetPendingIntents(ctx) ([]Intent, error)
ClaimIntent(ctx, intentID string) (ClaimResult, error)
    → ErrAlreadyClaimed on 409, ErrNotFound on 404
    → ErrClaimOutcomeUnknown if retries exhaust after an ambiguous timeout / connection loss
    → ClaimResult carries the authoritative post-claim deadline (see below)
PostMessage(ctx, sessionID string, frame OutboundFrame) error
    → X-Idempotency-Key = frame.MessageID
    → OutboundFrame = { MessageID string, Seq uint64, Round uint32, ToPartyID string, Payload []byte }
    → frame.Seq is the sender-local protocol sequence from `protocol.Frame.Seq`, not the delivery cursor
    → successful API response returns `deliverySeq`, but the client does not use it for protocol logic
GetMessages(ctx, sessionID string, afterSeq uint64) ([]InboundMessage, error)
    → returns only messages where deliverySeq > afterSeq
    → `InboundMessage.DeliverySeq` is a transport-level polling cursor, distinct from `InboundMessage.Seq`
PostResult(ctx, intentID string, result IntentResult) error
    → IntentResult = { Status "COMPLETED"|"FAILED", ErrorCode string, ErrorMessage string }
    → `ErrorCode` is a stable machine-readable contract owned by this service
    → this endpoint is terminal ack only; DKG key metadata is not returned here
```

`ClaimIntent` does not send a request body. The monolith derives the logical caller from the
authenticated API key and must keep claim idempotent for repeated retries by that same caller:
after a lost response, a retry returns `200` with the same `ClaimResult` instead of `409`.

**ClaimResult:**

```go
type ClaimResult struct {
    ExpiresAt time.Time  // post-claim deadline (original + 10 min extension)
}
```

`expiresAt` is a required part of the successful claim response. The worker must not infer the
deadline locally from `time.Now()` because the claim endpoint is the source of truth for the
post-claim extension window.

**Intent payload contract:** each claimed intent must contain enough data to build the exact TSS
request expected by `brosettlement-mpc-core`.

```go
type Intent struct {
    IntentID   string
    SessionID  string
    Type       string // "DKG" | "SIGN"
    ExpiresAt  time.Time
    Payload    IntentPayload
}

type IntentPayload struct {
    KeyID      string   // required for SIGN; for ECDSA DKG derived from SessionID
    Parties    []string
    Threshold  uint32
    Algorithm  string
    Curve      string
    Chain      string
    Digest     []byte   // required for SIGN, empty for DKG
}
```

Validation happens **after claim succeeds**. If required fields are missing or inconsistent, the
worker posts `FAILED / INVALID_INTENT` and does not start the MPC session.

`CO_SIGNER_PARTY_ID` remains the fixed local identity passed to `brosettlement-mpc-core` as
`LocalPartyID`, while `Payload.Parties` defines the MPC participant set for the intent. The worker
always executes under that single configured identity and never switches party identity per intent.

```go
type InboundMessage struct {
    DeliverySeq uint64
    Seq         uint64
    MessageID   string
    Round       uint32
    FromPartyID string
    ToPartyID   string
    Payload     []byte
}

type IntentResult struct {
    Status       string
    ErrorCode    string
    ErrorMessage string
}
```

**MVP algorithm scope:** this monolith integration is currently specified only for ECDSA-based DKG
and SIGN flows. Although `brosettlement-mpc-core` may evolve broader algorithm support over time,
non-ECDSA intents are out of scope for this document and must be rejected as `INVALID_INTENT`
until the monolith integration contract is explicitly extended beyond ECDSA.

**DKG metadata contract:** `POST /intents/:intentId/result` is not used to return
`keyId/publicKey/address`. If the monolith needs DKG key metadata, it must obtain and persist it
through the signer-side status / key-metadata path rather than the co-signer terminal ack.

**Validation rules:** `validateIntent(intent)` should reject at least:

- empty `SessionID`
- `Type` outside `DKG|SIGN`
- fewer than 2 unique parties
- `Threshold < 2` or `Threshold > len(Parties)`
- local party not present in `Payload.Parties`
- any non-ECDSA `Algorithm` for MVP monolith integration
- unsupported ECDSA `Curve` combination for the current core
- `SIGN` intents with empty `KeyID` or empty `Digest`

**Sequence model:** the design uses **two different sequence spaces** and they must not be
collapsed into one field:

- `protocol.Frame.Seq` is generated by the sender-side TSS engine and is preserved end-to-end
  because the core deduper keys on `(sessionID, stage, fromParty, frame.Seq, payloadHash)`.
- `InboundMessage.DeliverySeq` is generated by the monolith as a polling cursor so the worker can
  fetch `deliverySeq > afterSeq` without replaying already-delivered rows.

The monolith may store both values, but it must not overwrite the protocol frame sequence with the
delivery cursor.

**Frame mapping contract:** outbound HTTP payloads may omit fields that the monolith can derive
from the claimed intent (`Stage`, `Protocol`, `FromParty`, `PayloadHash`, `SentAt`).
The current outbound request body carries at minimum:

- `MessageID`
- `Seq`
- `Round`
- `ToPartyID`
- `Payload`

Inbound messages returned to the worker must include at minimum:

- `DeliverySeq`
- `Seq`
- `MessageID`
- `Round`
- `FromPartyID`
- `ToPartyID`
- `Payload`

The worker reconstructs the remaining `protocol.Frame` fields locally from the claimed intent,
route parameters, and payload bytes. To make that reconstruction explicit in code,
`HTTPTransport` receives immutable per-session `FrameContext` derived from the claimed intent,
at minimum `SessionID` and `Stage`, and may also carry static fields such as `Protocol` that do
not change during the session.

Without these fields, core frame validation / dedupe semantics are no longer reliable.

**Ed25519 signing:** encapsulated in a private `signRequest(req)` middleware.

- Required auth headers for every request: `X-Api-Key-Id`, `X-Api-Timestamp`,
  `X-Api-Signature`
- For requests with body, the client also sends `X-Api-Body-Hash = hex(sha256(raw_body_bytes))`
- For body-less requests, the canonical body-hash component is the empty string; the client may
  omit `X-Api-Body-Hash`
- For `POST` / `PUT` / `PATCH` requests under API-key auth, the client must also send
  `X-Idempotency-Key`

Canonical string signed by Ed25519:

```text
UPPERCASE(method) + "\n" +
path_without_query + "\n" +
(x_api_body_hash || "") + "\n" +
x_api_timestamp
```

Rules:

- `method` is normalized with `strings.ToUpper`
- `path_without_query` is the request path exactly as sent, excluding everything after `?`
- query string is **not** part of the signature
- `X-Api-Key-Id` is **not** part of the signature payload
- `content-type` is **not** part of the signature payload
- `X-Api-Timestamp` is Unix time in whole seconds
- `X-Api-Signature` is base64-encoded Ed25519 signature over the canonical UTF-8 bytes
- default allowed clock skew is `+-300s` on the monolith side

The worker should hash the exact raw bytes it sends on the wire when populating
`X-Api-Body-Hash`. The current monolith signing guard validates the signature against the supplied
header value and does **not** independently recompute the body hash from the raw request body.

**Retry policy:** transient network errors and 5xx responses — up to 3 attempts with exponential
backoff. 4xx responses (except the typed errors above) — immediate error return. `PostMessage` and
`PostResult` are safe to retry because the monolith enforces idempotency via idempotency key /
DB-level state respectively. `ClaimIntent` retries are safe only because the claim contract is
idempotent for the same authenticated API-key caller; otherwise the client returns
`ErrClaimOutcomeUnknown` and the worker does not start the MPC session blindly.

---

### HTTPTransport (`internal/transport/http_transport.go`)

Implements `tss.Transport` (`SendFrame` / `RecvFrame`) over HTTP polling.
Structurally similar to the removed `StreamTransport`.

```go
type FrameContext struct {
    SessionID string
    Stage     string
    Protocol  string
}

type HTTPTransport struct {
    client       *monolith.Client
    frameCtx     FrameContext
    pollInterval time.Duration
    inbound      chan protocol.Frame  // buffered, default size 256
    startOnce    sync.Once
    closeOnce    sync.Once
    done         chan struct{}
    log          *slog.Logger
}
```

Note: no `fromPartyID` field. `fromPartyId` is derived server-side by the monolith from the
claimed intent — the transport never needs it for `SendFrame`. The immutable `frameCtx` carries
the static inbound fields the monolith does not echo back on every message. Party membership for
MPC remains defined by `Payload.Parties`; the transport does not participate in party routing.

**Contract:**

- `Start(ctx)` — idempotent via `sync.Once`; launches exactly one polling goroutine bound to the
  session context; never returns
  an error.
- `Close()` — idempotent via `sync.Once`; closes `done` as a local stop signal. It does **not**
  cancel an in-flight `GetMessages` request by itself; the polling goroutine exits after the
  current request returns or the session context is cancelled. The transport makes no strong
  guarantee about already-buffered frames racing with `done` during shutdown. This is acceptable
  for MVP because `runSession` does not continue protocol execution after shutdown/return.
- `SendFrame(ctx, frame)` — calls `client.PostMessage`; propagates errors to caller.
- `RecvFrame(ctx)`:
  ```go
  select {
  case frame := <-t.inbound: return frame, nil
  case <-t.done:             return protocol.Frame{}, ErrTransportClosed
  case <-ctx.Done():         return protocol.Frame{}, ctx.Err()
  }
  ```

**Background polling goroutine:**

```
afterSeq := uint64(0)
for {
    select { case <-done: return; default: }

    msgs, err := client.GetMessages(ctx, sessionID, afterSeq)
    if err:
        if ctx is done → return
        log.Warn; sleep(pollInterval); continue

    sort msgs by deliverySeq ASC  // transport sorts if API does not guarantee order

    for each msg:
        // toFrame(msg) merges msg fields with frameCtx so SessionID/Stage stay stable
        select { case inbound <- toFrame(msg): ; case <-done: return }
        afterSeq = max(afterSeq, msg.DeliverySeq)

    if len(msgs) == 0:
        sleep(pollInterval)
    // if msgs were non-empty: loop immediately (more may be available)
}
```

Frame polling uses a **fixed** `pollInterval` (no adaptive backoff) because session latency
takes priority over reduced request volume.

---

### Scheduler (`internal/worker/scheduler.go`)

```go
type SchedulerConfig struct {
    MinInterval   time.Duration  // lower bound on poll interval (backoff floor)
    MaxInterval   time.Duration  // upper bound on poll interval (backoff ceiling)
    BackoffFactor float64        // multiplier applied on empty / failed polls
}

type Scheduler struct {
    client            *monolith.Client
    runner            tssRunner
    localPartyID      string
    framePollInterval time.Duration
    sem               chan struct{}     // buffered cap=maxConcurrent; acquire=send, release=recv
    repollCh          chan struct{}     // buffered cap=1; best-effort immediate-repoll signal
    cfg               SchedulerConfig
    log               *slog.Logger
}
```

**Main loop:**

```go
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
        s.log.Warn("monolith request retry exhausted", "operation", "getPendingIntents", "err", err)
        backoff = nextBackoff(backoff, s.cfg)
        continue
    }

    if len(intents) == 0 {
        backoff = nextBackoff(backoff, s.cfg)
        continue
    }
    backoff = s.cfg.MinInterval  // reset on any intents returned

dispatch:
    for _, intent := range intents {
        select {
        case s.sem <- struct{}{}:
            go runSession(ctx, intent, s.client, s.runner, s.localPartyID,
                s.framePollInterval, s.sem, s.repollCh, s.log)
        default:
            // No free slots. Remaining intents stay PENDING and will be re-fetched
            // on the next poll cycle. Labeled break exits the for loop (bare `break`
            // inside a `select` would only exit the select).
            break dispatch
        }
    }
}

func nextBackoff(current time.Duration, cfg SchedulerConfig) time.Duration {
    next := time.Duration(float64(current) * cfg.BackoffFactor)
    if next > cfg.MaxInterval {
        return cfg.MaxInterval
    }
    return next
}
```

---

### SessionWorker (`internal/worker/session_worker.go`)

The worker uses the current `brosettlement-mpc-core` facade directly. `RunDKGSession` still
returns `tss.DKGOutput` in the core API, but the worker does not forward DKG output through
`PostResult`; the co-signer result endpoint is terminal ack only.

```go
type tssRunner interface {
    RunDKGSession(ctx context.Context, req tss.DKGSessionRequest) (tss.DKGOutput, error)
    RunSignSession(ctx context.Context, req tss.SignSessionRequest) error
}
```

`mpc-co-signer` does **not** perform a second post-DKG read to assemble result metadata for
`PostResult`. `RunDKGSession` may still return `tss.DKGOutput` because that is the current
`brosettlement-mpc-core` API, but the worker only uses the success/failure outcome on the
co-signer wire path. Any signer-side persistence of DKG key metadata remains outside this worker
flow.

```go
func runSession(ctx context.Context, intent Intent, client *monolith.Client,
    runner tssRunner, localPartyID string, framePollInterval time.Duration,
    sem chan struct{}, repollCh chan struct{}, log *slog.Logger) {

    defer releaseSem(sem, repollCh)  // invariant: always releases slot

    log = log.With("intentId", intent.IntentID, "sessionId", intent.SessionID,
                   "type", intent.Type)

    // --- Claim (side-effect free until here) ---
    claim, err := client.ClaimIntent(ctx, intent.IntentID)
    if err != nil {
        switch {
        case errors.Is(err, monolith.ErrAlreadyClaimed):
            log.Info("intent skipped", "reason", "already_claimed")
        case errors.Is(err, monolith.ErrNotFound):
            log.Info("intent skipped", "reason", "not_found")
        case errors.Is(err, monolith.ErrClaimOutcomeUnknown):
            log.Warn("claim outcome unknown", "err", err)
        default:
            log.Warn("claim failed after retries", "err", err)
        }
        return  // no local session starts unless ownership of the claim is confirmed
    }
    log.Info("intent claimed", "expiresAt", claim.ExpiresAt)

    // --- From here: intent is CLAIMED ---
    // All failures must either reach PostResult or be left to monolith timeout sweep.

    if err := validateIntent(intent); err != nil {
        log.Error("invalid intent payload", "err", err)
        postResult(ctx, client, intent.IntentID, IntentResult{
            Status:       "FAILED",
            ErrorCode:    "INVALID_INTENT",
            ErrorMessage: err.Error(),
        }, log)
        return
    }

    // Edge case: if claim.ExpiresAt is already in the past (clock skew, slow network,
    // monolith contract mismatch), skip running MPC and report FAILED immediately.
    if !claim.ExpiresAt.After(time.Now()) {
        log.Warn("claimed intent already expired", "expiresAt", claim.ExpiresAt)
        postResult(ctx, client, intent.IntentID, IntentResult{
            Status:    "FAILED",
            ErrorCode: "ALREADY_EXPIRED",
        }, log)
        return
    }

    // Session deadline: use the post-claim expiresAt returned by the monolith.
    // This is the extended value (original + 10 min) and is the authoritative
    // deadline — not intent.ExpiresAt from the pre-claim GetPendingIntents response.
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

    log.Info("session started", "expiresAt", claim.ExpiresAt, "partyId", localPartyID)

    start := time.Now()
    var runErr error
    switch intent.Type {
    case "DKG":
        _, runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, localPartyID, tr))
    case "SIGN":
        runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
    default:
        runErr = fmt.Errorf("unknown intent type: %s", intent.Type)
    }
    log.Info("session finished", "result", outcomeOf(runErr, sessionCtx),
             "durationMs", time.Since(start).Milliseconds())

    result := buildResult(runErr, sessionCtx, intent)

    // PostResult uses a background context on shutdown (main ctx may be cancelled).
    postCtx := ctx
    if ctx.Err() != nil {
        var postCancel context.CancelFunc
        postCtx, postCancel = context.WithTimeout(context.Background(), 5*time.Second)
        defer postCancel()
    }
    if err := client.PostResult(postCtx, intent.IntentID, result); err != nil {
        // Best-effort. Intent stays CLAIMED; monolith timeout sweep will reconcile.
        log.Error("failed to post result", "err", err, "result", result.Status)
    } else {
        log.Info("result posted", "status", result.Status)
    }
}

func releaseSem(sem chan struct{}, repollCh chan struct{}) {
    <-sem
    select {
    case repollCh <- struct{}{}:
    default:
    }
}
```

**Result classification:**

The worker owns the mapping from internal/core errors to wire-level `errorCode`. Known
`brosettlement-mpc-core` failures must be mapped explicitly and must not be collapsed into
`INTERNAL_ERROR`.

| Condition | Status | errorCode |
|---|---|---|
| `runErr == nil` | `COMPLETED` | — |
| `context.DeadlineExceeded` | `FAILED` | `"SESSION_TIMEOUT"` |
| `context.Canceled` (shutdown) | `FAILED` | `"WORKER_SHUTDOWN"` |
| Missing / malformed intent payload | `FAILED` | `"INVALID_INTENT"` |
| Claimed intent already expired | `FAILED` | `"ALREADY_EXPIRED"` |
| Platform share not found (`shares.ErrShareNotFound`) | `FAILED` | `"SHARE_NOT_FOUND"` |
| Platform share disabled (`shares.ErrShareDisabled`) | `FAILED` | `"SHARE_DISABLED"` |
| Corrupt share payload (`shares.ErrInvalidSharePayload`) | `FAILED` | `"INVALID_SHARE_PAYLOAD"` |
| Share metadata mismatch (`shares.ErrMetadataMismatch`) | `FAILED` | `"SHARE_METADATA_MISMATCH"` |
| DKG result missing public key (`tss.ErrMissingDKGPublicKey`) | `FAILED` | `"DKG_MISSING_PUBLIC_KEY"` |
| DKG result missing address (`tss.ErrMissingDKGAddress`) | `FAILED` | `"DKG_MISSING_ADDRESS"` |
| MPC protocol error | `FAILED` | `"MPC_PROTOCOL_ERROR"` |
| Unknown intent type | `FAILED` | `"INVALID_INTENT"` |
| Other | `FAILED` | `"INTERNAL_ERROR"` |

`errorCode` is a string on the wire. Internally, constants are defined in `worker/errors.go`, and
`buildResult` should use `errors.Is`-based matching so wrapped core errors still map to the stable
wire codes above. Successful DKG and SIGN completion both post `COMPLETED` with no extra payload on
the co-signer result endpoint. Share-store I/O, decrypt, or OS errors that do not map to typed
core sentinels remain `INTERNAL_ERROR` in MVP.

---

## Configuration

### New variables

| Variable | Default | Description |
|---|---|---|
| `CO_SIGNER_MONOLITH_URL` | — (required) | Base URL of the BroSettlement monolith |
| `CO_SIGNER_API_KEY_ID` | — (required) | `X-Api-Key-Id` for request signing |
| `CO_SIGNER_API_PRIVATE_KEY` | — (required) | Ed25519 private key, hex or base64 |
| `CO_SIGNER_SHARE_ENCRYPTION_KEY` | — (required) | Opaque secret used to derive the local AES-256 share-store key |
| `CO_SIGNER_MAX_CONCURRENT` | `4` | Max parallel MPC sessions |
| `CO_SIGNER_POLL_MIN_INTERVAL` | `2s` | Min intent poll interval |
| `CO_SIGNER_POLL_MAX_INTERVAL` | `60s` | Max intent poll interval (backoff ceiling) |
| `CO_SIGNER_POLL_BACKOFF_FACTOR` | `1.5` | Backoff multiplier on empty response |
| `CO_SIGNER_FRAME_POLL_INTERVAL` | `500ms` | Fixed interval for inbound frame polling |
| `CO_SIGNER_HTTP_TIMEOUT` | `30s` | HTTP client timeout per request |

### Removed variables

`CO_SIGNER_API_KEY`, `CO_SIGNER_GRPC_ADDR`

### Retained variables

`CO_SIGNER_HTTP_ADDR`, `CO_SIGNER_SHARES_DIR`, `CO_SIGNER_PARTY_ID`

**Share-store key migration:** existing deployments should initially set
`CO_SIGNER_SHARE_ENCRYPTION_KEY` to the previous `CO_SIGNER_API_KEY` secret value so persisted
shares remain decryptable after the gRPC/API-key config split. The share-store encryption contract
stays local to this service and must not depend on public wire identifiers such as `APIKeyID`.

**Note on `CO_SIGNER_PARTY_ID`:** `CO_SIGNER_PARTY_ID` is the authoritative and fixed local
identity of this co-signer deployment. It is passed to `brosettlement-mpc-core` as the process
`LocalPartyID`; it is not a routing hint. The worker never switches party identity per intent, and
the monolith does not assign or override party identity at runtime. If horizontal scaling is
introduced, all replicas must use the same `CO_SIGNER_PARTY_ID`.

## Observability

All logging via `slog` (JSON format). No external metrics in MVP.

**Info-level events:**

| Event | Key fields |
|---|---|
| `intent claimed` | `intentId`, `sessionId`, `type`, `expiresAt` |
| `session started` | `intentId`, `sessionId`, `type`, `partyId`, `expiresAt` |
| `session finished` | `intentId`, `sessionId`, `type`, `result`, `durationMs` |
| `result posted` | `intentId`, `status` |
| `intent skipped` | `intentId`, `reason` (`already_claimed`, `not_found`) |
| `monolith request retry exhausted` | `operation`, `err` |

**Debug-level:** each `SendFrame` / `RecvFrame` with `sessionId`, `round`, `frameSeq`, and
inbound `deliverySeq` when available.

**Warn-level:** backoff increase, transient errors during polling, `claim outcome unknown`,
`claim failed after retries`.

**Error-level:** `invalid intent payload`, `failed to post result`.

---

## Graceful Shutdown

```
SIGINT / SIGTERM
  │
  ├── root ctx cancelled → Scheduler.Run exits (no new intents dispatched)
  │
  ├── all runSession goroutines receive ctx.Done()
  │     ├── sessionCtx (derived from ctx) also cancelled
  │     ├── tss.RunSession returns error
  │     ├── PostResult called with context.Background() + 5s timeout (best-effort)
  │     │     errorCode = "WORKER_SHUTDOWN"
  │     │     on failure: intent stays CLAIMED; monolith timeout will reconcile
  │     └── defer: tr.Close(), releaseSem (slot returned + repollCh signal)
  │
  ├── main drains semaphore to confirm all workers have exited:
  │     for i := 0; i < cap(sem); i++ {
  │         select {
  │         case sem <- struct{}{}:
  │         case <-shutdownCtx.Done(): goto done
  │         }
  │     }
  │     Invariant: each worker releases exactly one slot in defer → drain always terminates.
  │     Shutdown timeout: 30s.
  │
  └── httpServer.Shutdown(5s) → health endpoint goes dark
```

---

## Monolith Auth Contract

The current BroSettlement implementation defines the following auth/error behavior for API-key
requests:

- missing signing headers → `401 Unauthorized` / `api_key.errors.MISSING_HEADERS`
- malformed timestamp → `401 Unauthorized` / `api_key.errors.INVALID_TIMESTAMP`
- clock skew beyond allowed window → `401 Unauthorized` / `api_key.errors.CLOCK_SKEW`
- unknown or revoked key id → `401 Unauthorized` / `api_key.errors.INVALID_OR_REVOKED_KEY`
- source IP blocked by key policy → `401 Unauthorized` / `api_key.errors.IP_NOT_ALLOWED`
- invalid stored public key → `401 Unauthorized` / `api_key.errors.INVALID_PUBLIC_KEY`
- malformed signature encoding / length → `401 Unauthorized` / `api_key.errors.INVALID_SIGNATURE_FORMAT`
- signature verification failure → `401 Unauthorized` / `api_key.errors.INVALID_SIGNATURE`
- missing `X-Idempotency-Key` on `POST` / `PUT` / `PATCH` → `400 Bad Request` /
  `api_key.errors.IDEMPOTENCY_KEY_REQUIRED`

Replay protection in the currently deployed monolith is limited to the timestamp skew window.
`X-Idempotency-Key` is enforced for mutating requests, but there is no active nonce store or
server-side replay ledger beyond that in the signing layer today.
