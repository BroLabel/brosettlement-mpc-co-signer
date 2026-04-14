# MPC Co-Signer: Monolith Integration Design

## Goal

Replace the existing gRPC-based interface (where BroSettlement pushes work to `mpc-co-signer`) with
an HTTP polling model where `mpc-co-signer` actively fetches work from the BroSettlement monolith,
participates in MPC sessions over HTTP-based frame exchange, and reports results back.

The HTTP API of the monolith becomes the **sole external contract** of this service. No gRPC
interface remains after this change.

---

## Context

### Current state (being removed)

BroSettlement called `ControlService.StartDkg` / `StartSign` over gRPC, then connected
`RelayService.Connect` for bidirectional frame streaming. `mpc-co-signer` was a passive server.

### New model

`mpc-co-signer` becomes the **initiator**:
1. Polls `GET /api/v1/co-signer/intents/pending` for work
2. Claims intents atomically via `POST /intents/:id/claim`
3. Drives MPC protocol rounds by polling inbound frames and pushing outbound frames over HTTP
4. Reports final status via `POST /intents/:id/result`

### Deployment model

Single process, single instance for MVP. No distributed coordination required — the monolith's
`POST /claim` compare-and-set provides sufficient mutual exclusion if multiple instances are ever
deployed in the future.

### Crash recovery

On restart, the service polls only `PENDING` intents. Intents left `CLAIMED` by a crashed instance
are handled by the monolith's timeout sweep. The service takes no action to rehydrate or explicitly
fail orphaned intents.

---

## What Changes

### Removed

```
internal/grpc/            — ControlService, RelayService, auth interceptors, all tests
internal/session/         — in-memory business session store (state is owned by monolith)
internal/transport/stream_transport.go  — replaced by HTTPTransport
proto/                    — stripped gRPC contract
api/proto/mpc/v1/         — generated protobuf code
buf.yaml, buf.gen.yaml    — buf codegen config
go.mod: grpc, protobuf dependencies
```

Note: runtime state (active transports, polling goroutines) is still maintained — it moves into
the `transport` and `worker` layers.

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
  ├── tr := NewHTTPTransport(client, sessionID, framePollInterval, log)
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
    workerID   string         // "<hostname>-<pid>", generated at startup
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
    → OutboundFrame = { Seq uint64, Round uint32, RoundHint uint32, Broadcast bool,
                        ToPartyID string, MessageType string, CorrelationID string,
                        Payload []byte, MessageID string }
    → frame.Seq is the sender-local protocol sequence from `protocol.Frame.Seq`, not the polling cursor
    → fromPartyId is derived server-side — the client never sends it
GetMessages(ctx, sessionID string, afterCursor uint64) ([]InboundMessage, error)
    → returns only messages where cursor > afterCursor
    → `InboundMessage.Cursor` is a transport-level delivery offset, distinct from `InboundMessage.Frame.Seq`
PostResult(ctx, intentID string, result IntentResult) error
    → IntentResult = { Status "COMPLETED"|"FAILED", ErrorCode string, ErrorMessage string }
    → `ErrorCode` is a stable machine-readable contract owned by this service
```

`ClaimIntent` sends `{ "claimedBy": client.workerID }` in the request body.
The monolith claim contract must be **idempotent per worker**: if the same `claimedBy` retries a
claim after a lost response, the endpoint returns `200` with the same `ClaimResult` instead of `409`.

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
    OrgID      string
    KeyID      string   // required for SIGN, ignored for DKG
    Parties    []string
    Threshold  uint32
    Algorithm  string
    Curve      string
    Chain      string
    Digest     []byte   // required for SIGN, empty for DKG
    PartyID    string   // optional consistency-check field
}
```

Validation happens **after claim succeeds**. If required fields are missing or inconsistent, the
worker posts `FAILED / INVALID_INTENT` and does not start the MPC session.

**Sequence model:** the design uses **two different sequence spaces** and they must not be
collapsed into one field:

- `protocol.Frame.Seq` is generated by the sender-side TSS engine and is preserved end-to-end
  because the core deduper keys on `(sessionID, stage, fromParty, frame.Seq, payloadHash)`.
- `InboundMessage.Cursor` is generated by the monolith as a polling cursor so the worker can fetch
  `cursor > afterCursor` without replaying already-delivered rows.

The monolith may store both values, but it must not overwrite the protocol frame sequence with the
polling cursor.

**Frame mapping contract:** outbound HTTP payloads may omit fields that the monolith can derive
from the claimed intent (`OrgID`, `Stage`, `Protocol`, `FromParty`, `PayloadHash`, `SentAt`).
Inbound messages returned to the worker must reconstruct a complete `protocol.Frame` with the
canonical protocol fields populated, at minimum:

- `SessionID`
- `OrgID`
- `Stage`
- `FromParty`
- `Seq`
- `Payload`
- `PayloadHash` (or enough information for the worker to recompute it deterministically)
- `Broadcast` and/or `ToParty`

Without these fields, core frame validation / dedupe semantics are no longer reliable.

**Ed25519 signing:** encapsulated in a private `signRequest(req)` middleware — adds
`X-Api-Key-Id`, `X-Api-Timestamp`, `X-Api-Signature`. Exact signing scheme (what bytes are
signed) must be aligned with the monolith before implementation; it is an implementation detail,
not a design decision.

**Retry policy:** transient network errors and 5xx responses — up to 3 attempts with exponential
backoff. 4xx responses (except the typed errors above) — immediate error return. `PostMessage` and
`PostResult` are safe to retry because the monolith enforces idempotency via idempotency key /
DB-level state respectively. `ClaimIntent` retries are safe only because the claim contract is
idempotent for the same `claimedBy`; otherwise the client returns `ErrClaimOutcomeUnknown` and the
worker does not start the MPC session blindly.

---

### HTTPTransport (`internal/transport/http_transport.go`)

Implements `tss.Transport` (`SendFrame` / `RecvFrame`) over HTTP polling.
Structurally similar to the removed `StreamTransport`.

```go
type HTTPTransport struct {
    client       *monolith.Client
    sessionID    string
    pollInterval time.Duration
    inbound      chan protocol.Frame  // buffered, default size 256
    startOnce    sync.Once
    closeOnce    sync.Once
    done         chan struct{}
    log          *slog.Logger
}
```

Note: no `fromPartyID` field. `fromPartyId` is derived server-side by the monolith from the
claimed intent — the transport never needs it for `SendFrame`.

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
afterCursor := uint64(0)
for {
    select { case <-done: return; default: }

    msgs, err := client.GetMessages(ctx, sessionID, afterCursor)
    if err:
        if ctx is done → return
        log.Warn; sleep(pollInterval); continue

    sort msgs by cursor ASC  // transport sorts if API does not guarantee order

    for each msg:
        // toFrame(msg) preserves the original protocol fields, including frame.Seq and fromParty
        select { case inbound <- toFrame(msg): ; case <-done: return }
        afterCursor = max(afterCursor, msg.Cursor)

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

    if intent.Payload.PartyID != "" && intent.Payload.PartyID != localPartyID {
        log.Error("intent party mismatch", "intentPartyId", intent.Payload.PartyID,
                  "localPartyId", localPartyID)
        postResult(ctx, client, intent.IntentID, IntentResult{
            Status:    "FAILED",
            ErrorCode: "INVALID_PARTY",
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

    tr := transport.NewHTTPTransport(client, intent.SessionID, framePollInterval, log)
    defer tr.Close()
    tr.Start(sessionCtx)

    log.Info("session started", "expiresAt", claim.ExpiresAt, "partyId", localPartyID)

    start := time.Now()
    var runErr error
    switch intent.Type {
    case "DKG":
        runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, localPartyID, tr))
    case "SIGN":
        runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, localPartyID, tr))
    default:
        runErr = fmt.Errorf("unknown intent type: %s", intent.Type)
    }
    log.Info("session finished", "result", outcomeOf(runErr, sessionCtx),
             "durationMs", time.Since(start).Milliseconds())

    result := buildResult(runErr, sessionCtx)

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
| Party mismatch (`intent.payload.partyId` != local config) | `FAILED` | `"INVALID_PARTY"` |
| Platform share not found (`shares.ErrShareNotFound`) | `FAILED` | `"SHARE_NOT_FOUND"` |
| Share metadata mismatch (`shares.ErrMetadataMismatch`) | `FAILED` | `"SHARE_METADATA_MISMATCH"` |
| DKG result missing public key (`tss.ErrMissingDKGPublicKey`) | `FAILED` | `"DKG_MISSING_PUBLIC_KEY"` |
| DKG result missing address (`tss.ErrMissingDKGAddress`) | `FAILED` | `"DKG_MISSING_ADDRESS"` |
| MPC protocol error | `FAILED` | `"MPC_PROTOCOL_ERROR"` |
| Unknown intent type | `FAILED` | `"INVALID_INTENT"` |
| Other | `FAILED` | `"INTERNAL_ERROR"` |

`errorCode` is a string on the wire. Internally, constants are defined in `worker/errors.go`, and
`buildResult` should use `errors.Is`-based matching so wrapped core errors still map to the stable
wire codes above.

---

## Configuration

### New variables

| Variable | Default | Description |
|---|---|---|
| `CO_SIGNER_MONOLITH_URL` | — (required) | Base URL of the BroSettlement monolith |
| `CO_SIGNER_API_KEY_ID` | — (required) | `X-Api-Key-Id` for request signing |
| `CO_SIGNER_API_PRIVATE_KEY` | — (required) | Ed25519 private key, hex or base64 |
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

**Note on `CO_SIGNER_PARTY_ID`:** for MVP this remains the authoritative local identity of the
co-signer process because the current TSS service requires a stable `LocalPartyID` per worker.
If the monolith also includes `payload.partyId`, the worker treats it as a consistency check and
fails the intent with `INVALID_PARTY` on mismatch; it does not switch identities per intent.

### Worker identity

`workerID` is generated at startup as `<hostname>-<pid>`. Not configurable.

---

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
inbound `cursor` when available.

**Warn-level:** backoff increase, transient errors during polling, `claim outcome unknown`,
`claim failed after retries`.

**Error-level:** `intent party mismatch`, `failed to post result`.

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

## Open Questions for Implementation

1. **Ed25519 signing scheme** — the exact bytes signed for `X-Api-Signature` (e.g.,
   `timestamp + method + path + body_hash`) must be aligned with the monolith before implementing
   `signRequest`. This is an implementation detail, not a design decision.
