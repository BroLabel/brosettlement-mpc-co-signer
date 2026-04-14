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
  types.go                — Intent, Frame, InboundMessage, IntentResult structs

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
  ├── creates Scheduler (client, tssRunner, semaphore, backoff config)
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
  │     ErrAlreadyClaimed / ErrNotFound → log + return  (intent was PENDING, no claim made)
  │     network/5xx after retries       → log + return  (intent stays PENDING)
  ├── [intent is now CLAIMED — all subsequent errors lead to PostResult or monolith timeout]
  ├── tr := NewHTTPTransport(client, sessionID, partyID)
  ├── defer tr.Close()
  ├── tr.Start(ctx)                     [idempotent, always succeeds, no error returned]
  ├── sessionCtx = derived from intent.expiresAt (+ optional local safety buffer)
  ├── tssRunner.Run{DKG,Sign}Session(sessionCtx, ..., tr)
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
ClaimIntent(ctx, intentID string) error
    → ErrAlreadyClaimed on 409, ErrNotFound on 404
PostMessage(ctx, sessionID string, frame OutboundFrame) (seq int64, error)
    → X-Idempotency-Key = frame.MessageID
GetMessages(ctx, sessionID string, afterSeq int64) ([]InboundMessage, error)
PostResult(ctx, intentID string, result IntentResult) error
```

`ClaimIntent` sends `{ "claimedBy": client.workerID }` in the request body.

**Ed25519 signing:** encapsulated in a private `signRequest(req)` middleware — adds
`X-Api-Key-Id`, `X-Api-Timestamp`, `X-Api-Signature`. Exact signing scheme (what bytes are
signed) must be aligned with the monolith before implementation; it is an implementation detail,
not a design decision.

**Retry policy:** transient network errors and 5xx responses — up to 3 attempts with exponential
backoff. 4xx responses (except the typed errors above) — immediate error return. `PostMessage` and
`PostResult` are safe to retry because the monolith enforces idempotency via idempotency key /
DB-level state respectively.

---

### HTTPTransport (`internal/transport/http_transport.go`)

Implements `tss.Transport` (`SendFrame` / `RecvFrame`) over HTTP polling.
Structurally similar to the removed `StreamTransport`.

```go
type HTTPTransport struct {
    client      *monolith.Client
    sessionID   string
    fromPartyID string
    inbound     chan protocol.Frame  // buffered, default size 256
    startOnce   sync.Once
    closeOnce   sync.Once
    done        chan struct{}
    log         *slog.Logger
}
```

**Contract:**

- `Start(ctx)` — idempotent via `sync.Once`; launches exactly one polling goroutine; never returns
  an error.
- `Close()` — idempotent via `sync.Once`; closes `done`; polling goroutine exits on next iteration.
  Does **not** drain `inbound` — any buffered frames are discarded. This is acceptable for MVP:
  the MPC session has already ended by the time `Close()` is called (via `defer`).
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
afterSeq := int64(0)
for {
    select { case <-done: return; default: }

    msgs, err := client.GetMessages(ctx, sessionID, afterSeq)
    if err:
        if ctx is done → return
        log.Warn; sleep(framePollInterval); continue

    sort msgs by seq ASC  // transport sorts if API does not guarantee order

    for each msg:
        select { case inbound <- toFrame(msg): ; case <-done: return }
        afterSeq = max(afterSeq, msg.Seq)

    if len(msgs) == 0:
        sleep(framePollInterval)
    // if msgs were non-empty: loop immediately (more may be available)
}
```

Frame polling uses a **fixed** `framePollInterval` (no adaptive backoff) because session latency
takes priority over reduced request volume.

---

### Scheduler (`internal/worker/scheduler.go`)

```go
type Scheduler struct {
    client   *monolith.Client
    runner   tssRunner
    sem      chan struct{}      // buffered cap=maxConcurrent; acquire=send, release=recv
    repollCh chan struct{}      // buffered cap=1; best-effort immediate-repoll signal
    cfg      SchedulerConfig   // MinInterval, MaxInterval, BackoffFactor
    log      *slog.Logger
}
```

**Main loop:**

```go
backoff := cfg.MinInterval
for {
    select {
    case <-ctx.Done():
        return
    case <-time.After(backoff):
    case <-s.repollCh:
    }

    intents, err := s.client.GetPendingIntents(ctx)
    if err != nil {
        s.log.Warn("monolith request failed", "err", err)
        backoff = min(backoff*cfg.BackoffFactor, cfg.MaxInterval)
        continue
    }

    if len(intents) == 0 {
        backoff = min(backoff*cfg.BackoffFactor, cfg.MaxInterval)
        continue
    }
    backoff = cfg.MinInterval  // reset on any intents

    for _, intent := range intents {
        select {
        case s.sem <- struct{}{}:
            go runSession(ctx, intent, s.client, s.runner, s.sem, s.repollCh, s.log)
        default:
            // No free slots. Remaining intents stay PENDING and will be re-fetched
            // on the next poll cycle. We stop trying to dispatch further intents
            // in this batch — use a labeled break to exit the for loop.
            goto waitNext
        }
    }
waitNext:
}
```

Note: `goto waitNext` is used instead of `break` because a bare `break` inside a `select` in Go
exits the `select` statement, not the enclosing `for` loop.

---

### SessionWorker (`internal/worker/session_worker.go`)

```go
func runSession(ctx context.Context, intent Intent, client *monolith.Client,
    runner tssRunner, sem chan struct{}, repollCh chan struct{}, log *slog.Logger) {

    defer releaseSem(sem, repollCh)  // invariant: always releases slot

    log = log.With("intentId", intent.IntentID, "sessionId", intent.SessionID,
                   "type", intent.Type)

    // --- Claim (side-effect free until here) ---
    if err := client.ClaimIntent(ctx, intent.IntentID); err != nil {
        switch {
        case errors.Is(err, monolith.ErrAlreadyClaimed),
             errors.Is(err, monolith.ErrNotFound):
            log.Info("intent skipped", "reason", err)
        default:
            log.Warn("claim failed after retries", "err", err)
        }
        return  // intent remains PENDING; monolith or another worker will handle it
    }
    log.Info("intent claimed")

    // --- From here: intent is CLAIMED ---
    // All failures must either reach PostResult or be left to monolith timeout sweep.

    tr := transport.NewHTTPTransport(client, intent.SessionID, intent.Payload.PartyID, log)
    defer tr.Close()
    tr.Start(ctx)

    // Session deadline: intent.expiresAt extended by monolith on claim (+10 min).
    // Use that as the hard deadline; add a small local safety buffer if needed.
    sessionCtx, cancel := context.WithDeadline(ctx, intent.ExpiresAt)
    defer cancel()

    start := time.Now()
    var runErr error
    switch intent.Type {
    case "DKG":
        runErr = runner.RunDKGSession(sessionCtx, buildDKGRequest(intent, tr))
    case "SIGN":
        runErr = runner.RunSignSession(sessionCtx, buildSignRequest(intent, tr))
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

| Condition | Status | errorCode |
|---|---|---|
| `runErr == nil` | `COMPLETED` | — |
| `context.DeadlineExceeded` | `FAILED` | `"SESSION_TIMEOUT"` |
| `context.Canceled` (shutdown) | `FAILED` | `"WORKER_SHUTDOWN"` |
| MPC protocol error | `FAILED` | `"MPC_PROTOCOL_ERROR"` |
| Key not found | `FAILED` | `"KEY_NOT_FOUND"` |
| Unknown intent type | `FAILED` | `"INVALID_INTENT"` |
| Other | `FAILED` | `"INTERNAL_ERROR"` |

`errorCode` is a string on the wire. Internally, constants are defined in `worker/errors.go`.

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

**Note on `CO_SIGNER_PARTY_ID`:** if `partyId` is provided per-intent by the monolith (in
`payload.partyId`), the config variable becomes a fallback or consistency check, not the source
of truth. This must be clarified before implementation. For MVP, `intent.Payload.PartyID` takes
precedence; `CO_SIGNER_PARTY_ID` is used only if the intent payload omits it.

### Worker identity

`workerID` is generated at startup as `<hostname>-<pid>`. Not configurable.

---

## Observability

All logging via `slog` (JSON format). No external metrics in MVP.

**Info-level events:**

| Event | Key fields |
|---|---|
| `intent claimed` | `intentId`, `sessionId`, `type`, `workerID` |
| `session started` | `intentId`, `sessionId`, `type`, `expiresAt` |
| `session finished` | `intentId`, `sessionId`, `type`, `result`, `durationMs` |
| `result posted` | `intentId`, `status` |
| `intent skipped` | `intentId`, `reason` (`already_claimed`, `not_found`) |
| `monolith request retry exhausted` | `operation`, `err` |
| `claim failed after retries` | `intentId`, `err` |
| `failed to post result` | `intentId`, `err`, `result` |

**Debug-level:** each `SendFrame` / `RecvFrame` with `sessionId`, `round`, `seq`.

**Warn-level:** backoff increase, transient errors during polling.

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

2. **`CO_SIGNER_PARTY_ID` source of truth** — clarify whether `payload.partyId` from the intent
   is always present. If yes, the config variable can be deprecated; if it may be absent, keep it
   as a required fallback.

3. **`expiresAt` after claim** — the monolith extends `expiresAt` by 10 minutes on successful
   claim. Confirm whether the extended value is returned in the `200` response body of
   `POST /claim`, or whether the service must compute it locally (`claimedAt + 10min`).
