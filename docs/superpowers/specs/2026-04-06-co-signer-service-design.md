# MPC Co-Signer Service Design

## Goal

Implement `brosettlement-mpc-co-signer` as a production-ready Go service that participates in
MPC (TSS) sessions orchestrated by the BroSettlement NestJS monolith. The co-signer holds one
key share, participates in DKG and signing sessions, and exposes a minimal gRPC + HTTP interface.

## Context

- **BroSettlement** (NestJS monolith) acts as relay proxy and session orchestrator.
- **mpc-signer** is the primary TSS party; BroSettlement calls it for session control and
  polls it for status.
- **mpc-co-signer** (this service) is a secondary participant. BroSettlement calls it to start
  sessions and relays protocol frames to it via a bidirectional gRPC stream.
- The TSS protocol implementation lives in `github.com/BroLabel/brosettlement-mpc-core`.

## Proto Contract

A stripped-down `proto/mpc.v1.proto`, derived from the mpc-signer contract with
mpc-signer-specific fields removed:

```protobuf
service ControlService {
  rpc StartDkg(StartDkgRequest) returns (StartDkgResponse);
  rpc StartSign(StartSignRequest) returns (StartSignResponse);
}

service RelayService {
  rpc Connect(stream Frame) returns (stream Frame);
}
```

**Removed vs mpc-signer proto:**
- `Abort`, `GetSessionStatus`, `GetSessionStatuses`, `GetKeyMeta` — BroSettlement does not
  call these on co-signer
- `SessionType`, `GetSessionStatusResponse`, `SessionStatusResult`, `SessionLookupError`,
  `SessionTelemetry`, `SignatureResult` — unused after removing the above RPCs
- `StartDkgRequest.derivation_path`, `StartDkgRequest.descriptor` — orchestrator metadata
- `StartSignRequest.intent_id`, `StartSignRequest.policy_context` — BroSettlement internals
- `StartDkgResponse.idempotent`, `StartSignResponse.idempotent` — mpc-signer operation-log
  concept
- `SessionStatus.CREATED`, `SESSION_STATUS_ABORTED`, `SESSION_STATUS_ABORTED_ON_RESTART` —
  not applicable to co-signer lifecycle

**Retained `SessionStatus` values:** `UNSPECIFIED`, `RUNNING`, `COMPLETED`, `FAILED`,
`TIMED_OUT`.

## Architecture

```
cmd/co-signer/
  main.go                  # dependency wiring, graceful shutdown

proto/
  mpc.v1.proto             # stripped contract

internal/
  config/
    config.go              # env-based config (5 variables)
  grpc/
    server.go              # gRPC server, interceptor registration
    control.go             # ControlService: StartDkg, StartSign
    relay.go               # RelayService: Connect (bidirectional stream)
  transport/
    stream_transport.go    # gRPC stream ↔ tss.Transport adapter
  session/
    store.go               # in-memory session store
  health/
    server.go              # HTTP GET /health
```

## Data Flow

### StartSign + Relay

```
BroSettlement
  │
  ├─ StartSign ──► control.go
  │                  ├─ creates Session{State:Running, Transport: new StreamTransport}
  │                  ├─ stores in SessionStore
  │                  ├─ launches goroutine: tss.Service.RunSignSession(ctx, req)
  │                  └─ returns {session_id, status:RUNNING}
  │
  └─ RelayService.Connect ──► relay.go
          │                     ├─ reads sessionId from first frame
          │                     ├─ looks up session in store
          │                     └─ calls session.Transport.Attach(stream)
          │                              │
          │                       reader goroutine: stream → inbound chan
          │                       writer goroutine: outbound chan → stream
          │
        frames exchanged until session completes or stream closes
```

DKG follows the same flow via `tss.Service.RunDKGSession`.

## Components

### StreamTransport (`internal/transport`)

Implements `tss.Transport` (`SendFrame` / `RecvFrame`) using two buffered channels:

```go
type StreamTransport struct {
    outbound chan protocol.Frame  // SendFrame writes here
    inbound  chan protocol.Frame  // RecvFrame reads from here
    done     chan struct{}
}
```

`Attach(stream)` wires a live gRPC stream to the channels by launching reader/writer goroutines.
Calling `Attach` a second time on the same transport returns an error.

**Edge cases:**
- Relay connects before `RunSignSession` reads — frames buffer in `inbound`.
- Relay never connects — `RecvFrame` blocks until `ctx` deadline, session times out.
- Stream drops mid-session — reader closes `done`; `RecvFrame` returns error;
  `RunSignSession` terminates with failure.

### SessionStore (`internal/session`)

In-memory map protected by a mutex.

```go
type Session struct {
    ID        string
    OrgID     string
    Transport *StreamTransport
    State     SessionState   // Running | Completed | Failed | TimedOut
    StartedAt time.Time
    ExpiresAt time.Time
}
```

- **Idempotency**: duplicate `sessionId` on StartDkg/StartSign returns the existing session.
- **Cleanup**: a background ticker removes terminal sessions after a 5-minute retention window.

### ControlService (`internal/grpc/control.go`)

Handles `StartDkg` and `StartSign`:
1. Validates required fields:
   - both: `orgId`, `parties`, `threshold`, `session_ttl_seconds`
   - StartSign only: `keyId`, `digest`
2. Creates session + transport, stores them.
3. Launches `tss.Service.RunDKGSession` / `RunSignSession` in a goroutine with a context
   derived from `session_ttl_seconds`.
4. Returns `{session_id, status: RUNNING}`.

### RelayService (`internal/grpc/relay.go`)

Handles `Connect`:
1. Reads the first frame; extracts `session_id` (and `org_id` for validation).
2. Looks up the session; returns `NOT_FOUND` if absent.
3. Calls `transport.Attach(stream)`; blocks until stream or session ends.

### gRPC Server (`internal/grpc/server.go`)

- Registers both services.
- Applies a single auth interceptor (UnaryInterceptor + StreamInterceptor) that checks
  `x-api-key` gRPC metadata against `CO_SIGNER_API_KEY`. Returns `Unauthenticated` on mismatch.

### Health Server (`internal/health`)

`GET /health` returns:

```json
{
  "status": "ok|degraded|fail",
  "ready": true,
  "version": "0.1.0",
  "timestamp": "...",
  "capabilities": { "sign": true, "dkg": true },
  "checks": {
    "shares_dir": { "status": "ok", "critical": true, "message": "" }
  }
}
```

Critical check: shares directory is readable/writable. Pre-params pool fullness is non-critical
(degraded if empty, service still operates via sync fallback).

### Config (`internal/config`)

Five env variables owned by this service:

| Variable | Default | Description |
|---|---|---|
| `CO_SIGNER_API_KEY` | — (required) | Shared secret for gRPC auth |
| `CO_SIGNER_GRPC_ADDR` | `0.0.0.0:50051` | gRPC listen address |
| `CO_SIGNER_HTTP_ADDR` | `0.0.0.0:8081` | HTTP health listen address |
| `CO_SIGNER_SHARES_DIR` | `./data/shares` | File share store directory |
| `CO_SIGNER_PARTY_ID` | — (required) | Local party ID in TSS protocol |

`TSS_PREPARAMS_*` variables are consumed directly by `brosettlement-mpc-core` with sensible
defaults — they do not need to be set unless performance tuning is required.

## Startup Sequence

1. Load and validate config (fail fast if `CO_SIGNER_API_KEY` or `CO_SIGNER_PARTY_ID` missing).
2. Open file share store (`CO_SIGNER_SHARES_DIR`).
3. Create `tss.Service` via `tss.NewBnbServiceWithConfigAndShareStoreAndMetrics`.
4. Start pre-params pool (`service.StartPreParamsPool(ctx)`).
5. Start gRPC server.
6. Start HTTP health server.
7. Wait for `SIGTERM`/`SIGINT`.

## Graceful Shutdown

`SIGTERM`/`SIGINT` → stop accepting new gRPC requests → wait up to 30 s for active sessions
to finish → stop pre-params pool → stop HTTP server.

## Error Handling

- Invalid request fields → `codes.InvalidArgument`
- Session not found (relay connect) → `codes.NotFound`
- Auth failure → `codes.Unauthenticated`
- Session already in terminal state → `codes.FailedPrecondition`
- Internal TSS failure → `codes.Internal` with opaque message (no internal detail leaked)

## Testing

**Unit tests:**
- `StreamTransport`: SendFrame/RecvFrame, Attach, double-Attach error, stream close, ctx
  cancellation.
- `SessionStore`: create, lookup, idempotency, TTL cleanup.
- `config`: env parsing, defaults, missing required variables.
- Auth interceptor: valid key passes, invalid key returns `Unauthenticated`.

**Integration tests:**
- Full flow: real gRPC server in-process, call StartSign, attach relay stream, exchange frames,
  assert session completes.
- Uses `transport/adapters/channel` from mpc-core as the in-process frame counterpart.
- TSS protocol itself is not tested here — that is mpc-core's responsibility.
