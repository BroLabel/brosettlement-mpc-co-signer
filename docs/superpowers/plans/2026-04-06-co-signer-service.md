# MPC Co-Signer Service Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement a production-ready gRPC service that participates in MPC/TSS sessions as a co-signer party, storing key shares on disk and exchanging protocol frames via bidirectional gRPC streaming.

**Architecture:** The service wraps `tss.Service` from `brosettlement-mpc-core`. BroSettlement calls `ControlService.StartDkg`/`StartSign` to trigger sessions, then connects via `RelayService.Connect` to exchange TSS protocol frames. A `StreamTransport` adapter bridges the gRPC stream to the `tss.Transport` interface expected by core.

**Tech Stack:** Go 1.24, gRPC (`google.golang.org/grpc`), protobuf (`google.golang.org/protobuf`), `buf` for codegen, `brosettlement-mpc-core v0.1.1`, standard library `net/http` for health.

---

## File Map

```
proto/
  mpc.v1.proto                          # stripped gRPC contract

api/proto/mpc/v1/
  mpc.v1.pb.go                          # generated (do not edit)
  mpc.v1_grpc.pb.go                     # generated (do not edit)

cmd/co-signer/
  main.go                               # wiring, startup, graceful shutdown

internal/
  config/
    config.go                           # env-based config + validation
    config_test.go
  transport/
    stream_transport.go                 # StreamTransport: tss.Transport over gRPC stream
    stream_transport_test.go
  session/
    store.go                            # in-memory session store + cleanup ticker
    store_test.go
  grpc/
    server.go                           # gRPC server construction, auth interceptors
    auth.go                             # API key interceptor (unary + stream)
    auth_test.go
    control.go                          # ControlService: StartDkg, StartSign
    control_test.go
    relay.go                            # RelayService: Connect
    relay_test.go
  health/
    server.go                           # HTTP GET /health handler + server
    server_test.go
```

---

## Task 1: Proto file + code generation setup

**Files:**
- Create: `proto/mpc.v1.proto`
- Create: `buf.yaml`
- Create: `buf.gen.yaml`

- [ ] **Step 1: Add buf config files**

`buf.yaml`:
```yaml
version: v2
modules:
  - path: proto
deps:
  - buf.build/googleapis/googleapis
```

`buf.gen.yaml`:
```yaml
version: v2
plugins:
  - remote: buf.build/protocolbuffers/go
    out: .
    opt:
      - paths=source_relative
      - module=github.com/BroLabel/brosettlement-mpc-co-signer
  - remote: buf.build/grpc/go
    out: .
    opt:
      - paths=source_relative
      - module=github.com/BroLabel/brosettlement-mpc-co-signer
```

- [ ] **Step 2: Write proto/mpc.v1.proto**

```protobuf
syntax = "proto3";

package mpc.v1;

option go_package = "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1;mpcv1";

import "google/protobuf/timestamp.proto";

service ControlService {
  rpc StartDkg(StartDkgRequest) returns (StartDkgResponse);
  rpc StartSign(StartSignRequest) returns (StartSignResponse);
}

service RelayService {
  rpc Connect(stream Frame) returns (stream Frame);
}

enum SessionStatus {
  SESSION_STATUS_UNSPECIFIED = 0;
  SESSION_STATUS_RUNNING = 1;
  SESSION_STATUS_COMPLETED = 2;
  SESSION_STATUS_FAILED = 3;
  SESSION_STATUS_TIMED_OUT = 4;
}

message SessionConfig {
  string org_id = 1;
  string chain = 2;
  string algorithm = 3;
  string curve = 4;
  uint32 threshold = 5;
  repeated string parties = 6;
  uint32 session_ttl_seconds = 7;
}

message StartDkgRequest {
  string request_id = 1;
  string correlation_id = 2;
  string session_id = 3;
  SessionConfig config = 4;
}

message StartDkgResponse {
  string session_id = 1;
  SessionStatus status = 2;
}

message StartSignRequest {
  string request_id = 1;
  string correlation_id = 2;
  string session_id = 3;
  string key_id = 4;
  bytes digest = 5;
  SessionConfig config = 6;
  repeated string participants = 7;
}

message StartSignResponse {
  string session_id = 1;
  SessionStatus status = 2;
}

message Frame {
  string session_id = 1;
  string org_id = 2;
  string message_id = 3;
  uint64 seq = 4;
  uint32 round = 5;
  string from_party = 6;
  string to_party = 7;
  bytes payload = 8;
  string correlation_id = 9;
  google.protobuf.Timestamp sent_at = 10;
  string stage = 11;
  uint32 round_hint = 12;
  bool broadcast = 13;
  string protocol = 14;
  string message_type = 15;
  string payload_hash = 16;
}
```

- [ ] **Step 3: Install buf and generate code**

```bash
brew install bufbuild/buf/buf   # or: go install github.com/bufbuild/buf/cmd/buf@latest
buf dep update
buf generate
```

Expected: `api/proto/mpc/v1/mpc.v1.pb.go` and `api/proto/mpc/v1/mpc.v1_grpc.pb.go` created.

- [ ] **Step 4: Add gRPC dependencies**

```bash
go get google.golang.org/grpc@v1.70.0
go get google.golang.org/protobuf@v1.36.5
go get google.golang.org/grpc/codes
go get google.golang.org/grpc/status
go get google.golang.org/grpc/metadata
go mod tidy
```

- [ ] **Step 5: Verify build**

```bash
go build ./...
```

Expected: compiles with no errors.

- [ ] **Step 6: Commit**

```bash
git add proto/ buf.yaml buf.gen.yaml api/ go.mod go.sum
git commit -m "feat: add proto contract and generate gRPC stubs"
```

---

## Task 2: Config

**Files:**
- Create: `internal/config/config.go`
- Create: `internal/config/config_test.go`

- [ ] **Step 1: Write failing tests**

`internal/config/config_test.go`:
```go
package config_test

import (
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "test-key")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != "0.0.0.0:50051" {
		t.Errorf("got GRPCAddr=%q, want 0.0.0.0:50051", cfg.GRPCAddr)
	}
	if cfg.HTTPAddr != "0.0.0.0:8081" {
		t.Errorf("got HTTPAddr=%q, want 0.0.0.0:8081", cfg.HTTPAddr)
	}
	if cfg.SharesDir != "./data/shares" {
		t.Errorf("got SharesDir=%q, want ./data/shares", cfg.SharesDir)
	}
}

func TestLoadMissingAPIKey(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "")
	t.Setenv("CO_SIGNER_PARTY_ID", "party-1")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
}

func TestLoadMissingPartyID(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "test-key")
	t.Setenv("CO_SIGNER_PARTY_ID", "")

	_, err := config.Load()
	if err == nil {
		t.Fatal("expected error for missing party ID")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("CO_SIGNER_API_KEY", "secret")
	t.Setenv("CO_SIGNER_PARTY_ID", "p2")
	t.Setenv("CO_SIGNER_GRPC_ADDR", "127.0.0.1:9090")
	t.Setenv("CO_SIGNER_HTTP_ADDR", "127.0.0.1:9091")
	t.Setenv("CO_SIGNER_SHARES_DIR", "/tmp/shares")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GRPCAddr != "127.0.0.1:9090" {
		t.Errorf("got %q, want 127.0.0.1:9090", cfg.GRPCAddr)
	}
	if cfg.SharesDir != "/tmp/shares" {
		t.Errorf("got %q, want /tmp/shares", cfg.SharesDir)
	}
	if cfg.APIKey != "secret" {
		t.Error("APIKey not loaded")
	}
	if cfg.PartyID != "p2" {
		t.Error("PartyID not loaded")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/config/... -v
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement config**

`internal/config/config.go`:
```go
package config

import (
	"errors"
	"os"
)

type Config struct {
	APIKey    string
	PartyID   string
	GRPCAddr  string
	HTTPAddr  string
	SharesDir string
}

func Load() (Config, error) {
	cfg := Config{
		APIKey:    os.Getenv("CO_SIGNER_API_KEY"),
		PartyID:   os.Getenv("CO_SIGNER_PARTY_ID"),
		GRPCAddr:  envString("CO_SIGNER_GRPC_ADDR", "0.0.0.0:50051"),
		HTTPAddr:  envString("CO_SIGNER_HTTP_ADDR", "0.0.0.0:8081"),
		SharesDir: envString("CO_SIGNER_SHARES_DIR", "./data/shares"),
	}
	if cfg.APIKey == "" {
		return Config{}, errors.New("CO_SIGNER_API_KEY is required")
	}
	if cfg.PartyID == "" {
		return Config{}, errors.New("CO_SIGNER_PARTY_ID is required")
	}
	return cfg, nil
}

func envString(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/config/... -v
```

Expected: PASS — all 4 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/config/
git commit -m "feat: add env-based config with validation"
```

---

## Task 3: StreamTransport

**Files:**
- Create: `internal/transport/stream_transport.go`
- Create: `internal/transport/stream_transport_test.go`

- [ ] **Step 1: Write failing tests**

`internal/transport/stream_transport_test.go`:
```go
package transport_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

// fakeStream implements transport.GRPCStream for tests.
type fakeStream struct {
	recv chan protocol.Frame
	sent []protocol.Frame
	ctx  context.Context
}

func newFakeStream(ctx context.Context) *fakeStream {
	return &fakeStream{recv: make(chan protocol.Frame, 16), ctx: ctx}
}

func (f *fakeStream) Send(frame protocol.Frame) error {
	f.sent = append(f.sent, frame)
	return nil
}

func (f *fakeStream) Recv() (protocol.Frame, error) {
	select {
	case fr, ok := <-f.recv:
		if !ok {
			return protocol.Frame{}, errors.New("stream closed")
		}
		return fr, nil
	case <-f.ctx.Done():
		return protocol.Frame{}, f.ctx.Err()
	}
}

func (f *fakeStream) Context() context.Context { return f.ctx }

func TestSendFrameAndRecvFrame(t *testing.T) {
	ctx := context.Background()
	tr := transport.NewStreamTransport(8)
	stream := newFakeStream(ctx)
	stream.recv <- protocol.Frame{SessionID: "s1", FromParty: "p2"}

	if err := tr.Attach(stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	// RecvFrame should read the frame pushed into the stream
	got, err := tr.RecvFrame(ctx)
	if err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}
	if got.SessionID != "s1" {
		t.Errorf("got SessionID=%q, want s1", got.SessionID)
	}

	// SendFrame should write to the stream
	if err := tr.SendFrame(ctx, protocol.Frame{SessionID: "s1", FromParty: "p1"}); err != nil {
		t.Fatalf("SendFrame: %v", err)
	}
	// Give writer goroutine time to flush
	time.Sleep(10 * time.Millisecond)
	if len(stream.sent) != 1 {
		t.Fatalf("expected 1 sent frame, got %d", len(stream.sent))
	}
}

func TestDoubleAttachReturnsError(t *testing.T) {
	ctx := context.Background()
	tr := transport.NewStreamTransport(8)
	s1 := newFakeStream(ctx)
	s2 := newFakeStream(ctx)

	if err := tr.Attach(s1); err != nil {
		t.Fatalf("first Attach: %v", err)
	}
	if err := tr.Attach(s2); err == nil {
		t.Fatal("expected error on second Attach")
	}
}

func TestRecvFrameContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := transport.NewStreamTransport(8)
	stream := newFakeStream(ctx)

	if err := tr.Attach(stream); err != nil {
		t.Fatalf("Attach: %v", err)
	}

	cancel()
	_, err := tr.RecvFrame(ctx)
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
}

func TestRecvFrameBeforeAttach(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	tr := transport.NewStreamTransport(8)

	_, err := tr.RecvFrame(ctx)
	if err == nil {
		t.Fatal("expected timeout error when no stream attached")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/transport/... -v
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement StreamTransport**

`internal/transport/stream_transport.go`:
```go
package transport

import (
	"context"
	"errors"
	"sync"

	"github.com/BroLabel/brosettlement-mpc-core/protocol"
)

var ErrAlreadyAttached = errors.New("stream already attached")

// GRPCStream is the subset of a gRPC bidirectional stream needed by StreamTransport.
type GRPCStream interface {
	Send(protocol.Frame) error
	Recv() (protocol.Frame, error)
	Context() context.Context
}

// StreamTransport implements tss.Transport over a gRPC bidirectional stream.
// Call Attach once the gRPC stream is available; SendFrame/RecvFrame block until then.
type StreamTransport struct {
	inbound  chan protocol.Frame
	outbound chan protocol.Frame
	attachMu sync.Mutex
	attached bool
}

func NewStreamTransport(bufSize int) *StreamTransport {
	return &StreamTransport{
		inbound:  make(chan protocol.Frame, bufSize),
		outbound: make(chan protocol.Frame, bufSize),
	}
}

// Attach wires a live gRPC stream to the transport channels.
// Must be called exactly once; returns ErrAlreadyAttached on subsequent calls.
func (t *StreamTransport) Attach(stream GRPCStream) error {
	t.attachMu.Lock()
	defer t.attachMu.Unlock()
	if t.attached {
		return ErrAlreadyAttached
	}
	t.attached = true

	// reader: stream → inbound
	go func() {
		defer close(t.inbound)
		for {
			frame, err := stream.Recv()
			if err != nil {
				return
			}
			select {
			case t.inbound <- frame:
			case <-stream.Context().Done():
				return
			}
		}
	}()

	// writer: outbound → stream
	go func() {
		for {
			select {
			case frame, ok := <-t.outbound:
				if !ok {
					return
				}
				_ = stream.Send(frame)
			case <-stream.Context().Done():
				return
			}
		}
	}()

	return nil
}

// SendFrame implements tss.Transport.
func (t *StreamTransport) SendFrame(ctx context.Context, frame protocol.Frame) error {
	select {
	case t.outbound <- frame:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// RecvFrame implements tss.Transport.
func (t *StreamTransport) RecvFrame(ctx context.Context) (protocol.Frame, error) {
	select {
	case frame, ok := <-t.inbound:
		if !ok {
			return protocol.Frame{}, errors.New("stream closed")
		}
		return frame, nil
	case <-ctx.Done():
		return protocol.Frame{}, ctx.Err()
	}
}

// Close drains and closes the outbound channel, signalling the writer goroutine to exit.
func (t *StreamTransport) Close() {
	close(t.outbound)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/transport/... -v
```

Expected: PASS — all 4 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/transport/
git commit -m "feat: add StreamTransport gRPC stream adapter"
```

---

## Task 4: SessionStore

**Files:**
- Create: `internal/session/store.go`
- Create: `internal/session/store_test.go`

- [ ] **Step 1: Write failing tests**

`internal/session/store_test.go`:
```go
package session_test

import (
	"testing"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
)

func newTransport() *transport.StreamTransport {
	return transport.NewStreamTransport(8)
}

func TestCreateAndGet(t *testing.T) {
	store := session.NewStore()
	sess := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())

	got, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("expected session to be found")
	}
	if got.ID != sess.ID {
		t.Errorf("got ID=%q, want %q", got.ID, sess.ID)
	}
}

func TestGetNotFound(t *testing.T) {
	store := session.NewStore()
	_, ok := store.Get("missing")
	if ok {
		t.Fatal("expected session not found")
	}
}

func TestCreateIdempotent(t *testing.T) {
	store := session.NewStore()
	s1 := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())
	s2 := store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())

	if s1 != s2 {
		t.Fatal("expected same session returned for duplicate sessionId")
	}
}

func TestSetStateAndExpiredCleanup(t *testing.T) {
	store := session.NewStore()
	store.Create("sess-1", "org-1", time.Now().Add(time.Minute), newTransport())
	store.SetState("sess-1", session.StateCompleted)

	got, ok := store.Get("sess-1")
	if !ok {
		t.Fatal("session should still exist before cleanup")
	}
	if got.State != session.StateCompleted {
		t.Errorf("got state=%v, want Completed", got.State)
	}

	// After cleanup with a retention window of 0, completed session should be removed
	store.Cleanup(0)
	_, ok = store.Get("sess-1")
	if ok {
		t.Fatal("expected session to be removed after cleanup")
	}
}

func TestCleanupDoesNotRemoveRunning(t *testing.T) {
	store := session.NewStore()
	store.Create("sess-running", "org-1", time.Now().Add(time.Minute), newTransport())

	store.Cleanup(0)
	_, ok := store.Get("sess-running")
	if !ok {
		t.Fatal("running session should not be cleaned up")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/session/... -v
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement SessionStore**

`internal/session/store.go`:
```go
package session

import (
	"sync"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
)

type State int

const (
	StateRunning State = iota
	StateCompleted
	StateFailed
	StateTimedOut
)

type Session struct {
	ID        string
	OrgID     string
	Transport *transport.StreamTransport
	State     State
	StartedAt time.Time
	ExpiresAt time.Time
	doneAt    time.Time
}

func (s *Session) isTerminal() bool {
	return s.State == StateCompleted || s.State == StateFailed || s.State == StateTimedOut
}

type Store struct {
	mu       sync.Mutex
	sessions map[string]*Session
}

func NewStore() *Store {
	return &Store{sessions: make(map[string]*Session)}
}

// Create adds a new session. If a session with the same ID already exists, it is returned unchanged.
func (s *Store) Create(id, orgID string, expiresAt time.Time, tr *transport.StreamTransport) *Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.sessions[id]; ok {
		return existing
	}
	sess := &Session{
		ID:        id,
		OrgID:     orgID,
		Transport: tr,
		State:     StateRunning,
		StartedAt: time.Now(),
		ExpiresAt: expiresAt,
	}
	s.sessions[id] = sess
	return sess
}

// Get returns a session by ID.
func (s *Store) Get(id string) (*Session, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	return sess, ok
}

// SetState transitions a session to a new state and records the time if terminal.
func (s *Store) SetState(id string, state State) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[id]
	if !ok {
		return
	}
	sess.State = state
	if sess.isTerminal() {
		sess.doneAt = time.Now()
	}
}

// Cleanup removes terminal sessions whose doneAt is older than retention.
func (s *Store) Cleanup(retention time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := time.Now().Add(-retention)
	for id, sess := range s.sessions {
		if sess.isTerminal() && sess.doneAt.Before(cutoff) {
			delete(s.sessions, id)
		}
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/session/... -v
```

Expected: PASS — all 5 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/session/
git commit -m "feat: add in-memory session store"
```

---

## Task 5: Auth interceptor

**Files:**
- Create: `internal/grpc/auth.go`
- Create: `internal/grpc/auth_test.go`

- [ ] **Step 1: Write failing tests**

`internal/grpc/auth_test.go`:
```go
package grpc_test

import (
	"context"
	"testing"

	cosgrpc "github.com/BroLabel/brosettlement-mpc-co-signer/internal/grpc"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func noopHandler(ctx context.Context, req any) (any, error) { return nil, nil }

func TestUnaryAuthValidKey(t *testing.T) {
	interceptor := cosgrpc.UnaryAuthInterceptor("secret")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "secret"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, noopHandler)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestUnaryAuthInvalidKey(t *testing.T) {
	interceptor := cosgrpc.UnaryAuthInterceptor("secret")
	ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("x-api-key", "wrong"))
	_, err := interceptor(ctx, nil, &grpc.UnaryServerInfo{}, noopHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}

func TestUnaryAuthMissingKey(t *testing.T) {
	interceptor := cosgrpc.UnaryAuthInterceptor("secret")
	_, err := interceptor(context.Background(), nil, &grpc.UnaryServerInfo{}, noopHandler)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/grpc/... -v -run TestUnaryAuth
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement auth interceptor**

`internal/grpc/auth.go`:
```go
package grpc

import (
	"context"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func UnaryAuthInterceptor(apiKey string) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if err := checkAPIKey(ctx, apiKey); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

func StreamAuthInterceptor(apiKey string) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if err := checkAPIKey(ss.Context(), apiKey); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

func checkAPIKey(ctx context.Context, expected string) error {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "missing metadata")
	}
	vals := md.Get("x-api-key")
	if len(vals) == 0 || vals[0] != expected {
		return status.Error(codes.Unauthenticated, "invalid api key")
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/grpc/... -v -run TestUnaryAuth
```

Expected: PASS — all 3 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/grpc/auth.go internal/grpc/auth_test.go
git commit -m "feat: add API key auth interceptors"
```

---

## Task 6: ShareStore adapter

The `tss.ShareStore` interface (`SaveShare`, `LoadShare`, `DisableShare`) is satisfied by a thin
adapter over the file-based store from core. Since the file store in core uses a different
interface (`SourceSink`), we need a small adapter that implements `tss.ShareStore`.

**Files:**
- Create: `internal/sharestore/file_store.go`

- [ ] **Step 1: Implement file-backed ShareStore adapter**

`internal/sharestore/file_store.go`:
```go
package sharestore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	coreshares "github.com/BroLabel/brosettlement-mpc-core/tss"
)

// onDiskShare is the JSON envelope stored per key share file.
type onDiskShare struct {
	KeyID      string    `json:"key_id"`
	OrgID      string    `json:"org_id"`
	Algorithm  string    `json:"algorithm"`
	Curve      string    `json:"curve"`
	Version    uint32    `json:"version"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	Ciphertext []byte    `json:"ciphertext"`
}

// FileStore implements tss.ShareStore using the filesystem.
// Each key share is stored as a separate AES-256-GCM encrypted JSON file under dir/<keyID>.json.
type FileStore struct {
	dir string
	key []byte // 32-byte AES-256 key
}

// NewFileStore creates a FileStore. key must be exactly 32 bytes (AES-256).
// The directory is created if it does not exist.
func NewFileStore(dir string, key []byte) (*FileStore, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("share encryption key must be 32 bytes, got %d", len(key))
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create shares dir: %w", err)
	}
	return &FileStore{dir: dir, key: key}, nil
}

func (s *FileStore) path(keyID string) string {
	return filepath.Join(s.dir, keyID+".json")
}

func (s *FileStore) encrypt(plaintext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, err
	}
	return gcm.Seal(nonce, nonce, plaintext, nil), nil
}

func (s *FileStore) decrypt(ciphertext []byte) ([]byte, error) {
	block, err := aes.NewCipher(s.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, ct := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, ct, nil)
}

func (s *FileStore) SaveShare(_ context.Context, keyID string, blob []byte, meta coreshares.ShareMeta) error {
	ciphertext, err := s.encrypt(blob)
	if err != nil {
		return fmt.Errorf("encrypt share: %w", err)
	}
	disk := onDiskShare{
		KeyID:      keyID,
		OrgID:      meta.OrgID,
		Algorithm:  meta.Algorithm,
		Curve:      meta.Curve,
		Version:    meta.Version,
		Status:     meta.Status,
		CreatedAt:  meta.CreatedAt,
		Ciphertext: ciphertext,
	}
	encoded, err := json.Marshal(disk)
	if err != nil {
		return fmt.Errorf("encode share: %w", err)
	}
	tmp := s.path(keyID) + ".tmp"
	if err := os.WriteFile(tmp, encoded, 0o600); err != nil {
		return fmt.Errorf("write share: %w", err)
	}
	return os.Rename(tmp, s.path(keyID))
}

func (s *FileStore) LoadShare(_ context.Context, keyID string) (*coreshares.StoredShare, error) {
	encoded, err := os.ReadFile(s.path(keyID))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, coreshares.ErrShareNotFound
		}
		return nil, fmt.Errorf("read share: %w", err)
	}
	var disk onDiskShare
	if err := json.Unmarshal(encoded, &disk); err != nil {
		return nil, fmt.Errorf("%w: decode share: %v", coreshares.ErrInvalidSharePayload, err)
	}
	plaintext, err := s.decrypt(disk.Ciphertext)
	if err != nil {
		return nil, fmt.Errorf("decrypt share: %w", err)
	}
	return &coreshares.StoredShare{
		Blob: plaintext,
		Meta: coreshares.ShareMeta{
			KeyID:     disk.KeyID,
			OrgID:     disk.OrgID,
			Algorithm: disk.Algorithm,
			Curve:     disk.Curve,
			Version:   disk.Version,
			Status:    disk.Status,
			CreatedAt: disk.CreatedAt,
		},
	}, nil
}

func (s *FileStore) DisableShare(_ context.Context, _ string) error {
	// Not needed for co-signer — shares are managed by mpc-signer
	return nil
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/sharestore/...
```

- [ ] **Step 3: Commit**

```bash
git add internal/sharestore/
git commit -m "feat: add file-backed share store adapter"
```

---

## Task 7: ControlService

**Files:**
- Create: `internal/grpc/control.go`
- Create: `internal/grpc/control_test.go`

- [ ] **Step 1: Write failing integration test**

`internal/grpc/control_test.go`:
```go
package grpc_test

import (
	"context"
	"log/slog"
	"net"
	"testing"
	"time"

	cosgrpc "github.com/BroLabel/brosettlement-mpc-co-signer/internal/grpc"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startTestServer(t *testing.T, svc *coretss.Service, store *session.Store, partyID string) mpcv1.ControlServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	mpcv1.RegisterControlServiceServer(srv, cosgrpc.NewControlServer(svc, store, partyID, slog.Default()))
	t.Cleanup(func() { srv.Stop() })
	go srv.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return mpcv1.NewControlServiceClient(conn)
}

func TestStartSignMissingKeyID(t *testing.T) {
	store := session.NewStore()
	svc := coretss.NewBnbService(slog.Default())
	client := startTestServer(t, svc, store, "p1")

	_, err := client.StartSign(context.Background(), &mpcv1.StartSignRequest{
		SessionId: "sess-1",
		Config: &mpcv1.SessionConfig{
			OrgId:            "org-1",
			Parties:          []string{"p1", "p2"},
			Threshold:        1,
			SessionTtlSeconds: 30,
		},
		Digest: []byte("deadbeef"),
		// KeyId missing
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}
}

func TestStartSignCreatesSession(t *testing.T) {
	store := session.NewStore()
	svc := coretss.NewBnbService(slog.Default())
	client := startTestServer(t, svc, store, "p1")

	resp, err := client.StartSign(context.Background(), &mpcv1.StartSignRequest{
		SessionId: "sess-2",
		KeyId:     "key-1",
		Digest:    []byte("deadbeef"),
		Config: &mpcv1.SessionConfig{
			OrgId:            "org-1",
			Parties:          []string{"p1", "p2"},
			Threshold:        1,
			SessionTtlSeconds: 30,
		},
	})
	if err != nil {
		t.Fatalf("StartSign: %v", err)
	}
	if resp.SessionId != "sess-2" {
		t.Errorf("got session_id=%q, want sess-2", resp.SessionId)
	}
	if resp.Status != mpcv1.SessionStatus_SESSION_STATUS_RUNNING {
		t.Errorf("got status=%v, want RUNNING", resp.Status)
	}

	// Session should exist in store
	time.Sleep(10 * time.Millisecond)
	_, ok := store.Get("sess-2")
	if !ok {
		t.Fatal("expected session in store")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/grpc/... -v -run TestStart
```

Expected: FAIL — `cosgrpc.NewControlServer` not defined.

- [ ] **Step 3: Implement ControlService**

`internal/grpc/control.go`:
```go
package grpc

import (
	"context"
	"log/slog"
	"time"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type ControlServer struct {
	mpcv1.UnimplementedControlServiceServer
	tss     *coretss.Service
	store   *session.Store
	partyID string
	log     *slog.Logger
}

func NewControlServer(svc *coretss.Service, store *session.Store, partyID string, log *slog.Logger) *ControlServer {
	return &ControlServer{tss: svc, store: store, partyID: partyID, log: log}
}

func (s *ControlServer) StartDkg(ctx context.Context, req *mpcv1.StartDkgRequest) (*mpcv1.StartDkgResponse, error) {
	cfg := req.GetConfig()
	if cfg.GetOrgId() == "" || len(cfg.GetParties()) == 0 || cfg.GetThreshold() == 0 || cfg.GetSessionTtlSeconds() == 0 {
		return nil, status.Error(codes.InvalidArgument, "org_id, parties, threshold and session_ttl_seconds are required")
	}
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	tr := transport.NewStreamTransport(64)
	expiresAt := time.Now().Add(time.Duration(cfg.GetSessionTtlSeconds()) * time.Second)
	sess := s.store.Create(req.GetSessionId(), cfg.GetOrgId(), expiresAt, tr)

	go func() {
		sctx, cancel := context.WithDeadline(context.Background(), expiresAt)
		defer cancel()
		err := s.tss.RunDKGSession(sctx, coretss.DKGSessionRequest{
			Session: coretss.SessionDescriptor{
				SessionID: sess.ID,
				OrgID:     cfg.GetOrgId(),
				Parties:   cfg.GetParties(),
				Threshold: cfg.GetThreshold(),
				Algorithm: cfg.GetAlgorithm(),
				Curve:     cfg.GetCurve(),
				Chain:     cfg.GetChain(),
			},
			LocalPartyID: s.partyID,
			Transport:    tr,
		})
		if err != nil {
			s.log.Error("DKG session failed", "session_id", sess.ID, "err", err)
			s.store.SetState(sess.ID, session.StateFailed)
		} else {
			s.store.SetState(sess.ID, session.StateCompleted)
		}
		tr.Close()
	}()

	return &mpcv1.StartDkgResponse{
		SessionId: sess.ID,
		Status:    mpcv1.SessionStatus_SESSION_STATUS_RUNNING,
	}, nil
}

func (s *ControlServer) StartSign(ctx context.Context, req *mpcv1.StartSignRequest) (*mpcv1.StartSignResponse, error) {
	cfg := req.GetConfig()
	if cfg.GetOrgId() == "" || len(cfg.GetParties()) == 0 || cfg.GetThreshold() == 0 || cfg.GetSessionTtlSeconds() == 0 {
		return nil, status.Error(codes.InvalidArgument, "org_id, parties, threshold and session_ttl_seconds are required")
	}
	if req.GetSessionId() == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if req.GetKeyId() == "" {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	if len(req.GetDigest()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "digest is required")
	}

	tr := transport.NewStreamTransport(64)
	expiresAt := time.Now().Add(time.Duration(cfg.GetSessionTtlSeconds()) * time.Second)
	sess := s.store.Create(req.GetSessionId(), cfg.GetOrgId(), expiresAt, tr)

	go func() {
		sctx, cancel := context.WithDeadline(context.Background(), expiresAt)
		defer cancel()
		err := s.tss.RunSignSession(sctx, coretss.SignSessionRequest{
			Session: coretss.SessionDescriptor{
				SessionID: sess.ID,
				OrgID:     cfg.GetOrgId(),
				KeyID:     req.GetKeyId(),
				Parties:   cfg.GetParties(),
				Threshold: cfg.GetThreshold(),
				Algorithm: cfg.GetAlgorithm(),
				Chain:     cfg.GetChain(),
			},
			LocalPartyID: s.partyID,
			Digest:       req.GetDigest(),
			Transport:    tr,
		})
		if err != nil {
			s.log.Error("Sign session failed", "session_id", sess.ID, "err", err)
			s.store.SetState(sess.ID, session.StateFailed)
		} else {
			s.store.SetState(sess.ID, session.StateCompleted)
		}
		tr.Close()
	}()

	return &mpcv1.StartSignResponse{
		SessionId: sess.ID,
		Status:    mpcv1.SessionStatus_SESSION_STATUS_RUNNING,
	}, nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/grpc/... -v -run TestStart
```

Expected: PASS — both tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/grpc/control.go internal/grpc/control_test.go
git commit -m "feat: implement ControlService (StartDkg, StartSign)"
```

---

## Task 8: RelayService

**Files:**
- Create: `internal/grpc/relay.go`
- Create: `internal/grpc/relay_test.go`

- [ ] **Step 1: Write failing test**

`internal/grpc/relay_test.go`:
```go
package grpc_test

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	cosgrpc "github.com/BroLabel/brosettlement-mpc-co-signer/internal/grpc"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
)

func startRelayServer(t *testing.T, store *session.Store) mpcv1.RelayServiceClient {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := grpc.NewServer()
	mpcv1.RegisterRelayServiceServer(srv, cosgrpc.NewRelayServer(store))
	t.Cleanup(func() { srv.Stop() })
	go srv.Serve(lis)

	conn, err := grpc.NewClient(lis.Addr().String(), grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	return mpcv1.NewRelayServiceClient(conn)
}

func TestRelayConnectUnknownSession(t *testing.T) {
	store := session.NewStore()
	client := startRelayServer(t, store)

	stream, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	// Send a frame for a session that doesn't exist
	_ = stream.Send(&mpcv1.Frame{SessionId: "unknown", OrgId: "org-1"})
	_, err = stream.Recv()
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
}

func TestRelayConnectForwardsFrames(t *testing.T) {
	store := session.NewStore()
	tr := transport.NewStreamTransport(8)
	store.Create("sess-relay", "org-1", time.Now().Add(time.Minute), tr)
	client := startRelayServer(t, store)

	stream, err := client.Connect(context.Background())
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// First frame identifies the session
	if err := stream.Send(&mpcv1.Frame{SessionId: "sess-relay", OrgId: "org-1", FromParty: "p2"}); err != nil {
		t.Fatalf("Send: %v", err)
	}

	// The frame should arrive on the transport's inbound channel
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := tr.RecvFrame(ctx)
	if err != nil {
		t.Fatalf("RecvFrame: %v", err)
	}
	if got.FromParty != "p2" {
		t.Errorf("got FromParty=%q, want p2", got.FromParty)
	}

	stream.CloseSend()
	_, err = stream.Recv()
	if err != nil && err != io.EOF {
		t.Logf("stream closed: %v", err)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/grpc/... -v -run TestRelay
```

Expected: FAIL — `cosgrpc.NewRelayServer` not defined.

- [ ] **Step 3: Implement RelayService**

`internal/grpc/relay.go`:
```go
package grpc

import (
	"context"
	"io"
	"time"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-core/protocol"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type RelayServer struct {
	mpcv1.UnimplementedRelayServiceServer
	store *session.Store
}

func NewRelayServer(store *session.Store) *RelayServer {
	return &RelayServer{store: store}
}

type grpcRelayStream struct {
	stream mpcv1.RelayService_ConnectServer
}

func (s grpcRelayStream) Send(f protocol.Frame) error {
	return s.stream.Send(protoFromFrame(f))
}

func (s grpcRelayStream) Recv() (protocol.Frame, error) {
	pb, err := s.stream.Recv()
	if err != nil {
		return protocol.Frame{}, err
	}
	return frameFromProto(pb), nil
}

func (s grpcRelayStream) Context() context.Context { return s.stream.Context() }

func (s *RelayServer) Connect(stream mpcv1.RelayService_ConnectServer) error {
	// Read the first frame to identify the session
	first, err := stream.Recv()
	if err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}

	sess, ok := s.store.Get(first.GetSessionId())
	if !ok {
		return status.Errorf(codes.NotFound, "session %q not found", first.GetSessionId())
	}

	wrapped := grpcRelayStream{stream: stream}
	// Attach starts reader/writer goroutines for frames 2..N
	if err := sess.Transport.Attach(wrapped); err != nil {
		return status.Errorf(codes.FailedPrecondition, "attach: %v", err)
	}

	// Deliver the first frame (already consumed by Recv above) directly to the inbound channel
	sess.Transport.PushInbound(frameFromProto(first))

	// Block until stream context is done
	<-stream.Context().Done()
	return nil
}

func frameFromProto(pb *mpcv1.Frame) protocol.Frame {
	var sentAt time.Time
	if pb.GetSentAt() != nil {
		sentAt = pb.GetSentAt().AsTime()
	}
	return protocol.Frame{
		SessionID:     pb.GetSessionId(),
		OrgID:         pb.GetOrgId(),
		MessageID:     pb.GetMessageId(),
		Seq:           pb.GetSeq(),
		Round:         pb.GetRound(),
		FromParty:     pb.GetFromParty(),
		ToParty:       pb.GetToParty(),
		Payload:       pb.GetPayload(),
		CorrelationID: pb.GetCorrelationId(),
		SentAt:        sentAt,
		Stage:         pb.GetStage(),
		RoundHint:     pb.GetRoundHint(),
		Broadcast:     pb.GetBroadcast(),
		Protocol:      pb.GetProtocol(),
		MessageType:   pb.GetMessageType(),
		PayloadHash:   pb.GetPayloadHash(),
	}
}

func protoFromFrame(f protocol.Frame) *mpcv1.Frame {
	return &mpcv1.Frame{
		SessionId:     f.SessionID,
		OrgId:         f.OrgID,
		MessageId:     f.MessageID,
		Seq:           f.Seq,
		Round:         f.Round,
		FromParty:     f.FromParty,
		ToParty:       f.ToParty,
		Payload:       f.Payload,
		CorrelationId: f.CorrelationID,
		SentAt:        timestamppb.New(f.SentAt),
		Stage:         f.Stage,
		RoundHint:     f.RoundHint,
		Broadcast:     f.Broadcast,
		Protocol:      f.Protocol,
		MessageType:   f.MessageType,
		PayloadHash:   f.PayloadHash,
	}
}
```

> **Note:** Add `"context"` to the import block in `relay.go` — it is used by `grpcRelayStream.Context()`.

- [ ] **Step 4: Add PushInbound to StreamTransport**

`relay.go` calls `sess.Transport.PushInbound(...)`. Add this method to
`internal/transport/stream_transport.go`:

```go
// PushInbound delivers a frame directly to the inbound channel without going through the stream.
// Used to replay frames consumed before Attach was called.
func (t *StreamTransport) PushInbound(frame protocol.Frame) {
    select {
    case t.inbound <- frame:
    default: // drop if full — caller should size the buffer generously
    }
}
```

- [ ] **Step 5: Run tests to verify they pass**

```bash
go test ./internal/grpc/... -v -run TestRelay
```

Expected: PASS — both tests green.

- [ ] **Step 6: Commit**

```bash
git add internal/grpc/relay.go internal/grpc/relay_test.go internal/transport/stream_transport.go
git commit -m "feat: implement RelayService with frame forwarding"
```

---

## Task 9: Health server

**Files:**
- Create: `internal/health/server.go`
- Create: `internal/health/server_test.go`

- [ ] **Step 1: Write failing test**

`internal/health/server_test.go`:
```go
package health_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
)

func TestHealthOK(t *testing.T) {
	dir := t.TempDir()
	h := health.NewHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("got status %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["status"] != "ok" {
		t.Errorf("got status=%q, want ok", body["status"])
	}
	if body["ready"] != true {
		t.Errorf("expected ready=true")
	}
}

func TestHealthFailMissingDir(t *testing.T) {
	h := health.NewHandler("0.1.0", "/nonexistent/path/shares")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("got status %d, want 503", rec.Code)
	}
	var body map[string]any
	_ = json.NewDecoder(rec.Body).Decode(&body)
	if body["status"] != "fail" {
		t.Errorf("got status=%q, want fail", body["status"])
	}
}

func TestHealthOnlyGetAllowed(t *testing.T) {
	dir := t.TempDir()
	h := health.NewHandler("0.1.0", dir)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/health", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("got status %d, want 405", rec.Code)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/health/... -v
```

Expected: FAIL — package does not exist yet.

- [ ] **Step 3: Implement health handler**

`internal/health/server.go`:
```go
package health

import (
	"encoding/json"
	"net/http"
	"os"
	"time"
)

type checkResult struct {
	Status   string `json:"status"`
	Critical bool   `json:"critical"`
	Message  string `json:"message,omitempty"`
}

type response struct {
	Status       string                 `json:"status"`
	Ready        bool                   `json:"ready"`
	Version      string                 `json:"version"`
	Timestamp    string                 `json:"timestamp"`
	Capabilities map[string]bool        `json:"capabilities"`
	Checks       map[string]checkResult `json:"checks"`
}

type Handler struct {
	version   string
	sharesDir string
}

func NewHandler(version, sharesDir string) http.Handler {
	return &Handler{version: version, sharesDir: sharesDir}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	shareCheck := checkResult{Critical: true}
	if _, err := os.Stat(h.sharesDir); err != nil {
		shareCheck.Status = "fail"
		shareCheck.Message = err.Error()
	} else {
		shareCheck.Status = "ok"
	}

	overall := "ok"
	ready := true
	if shareCheck.Status == "fail" {
		overall = "fail"
		ready = false
	}

	resp := response{
		Status:    overall,
		Ready:     ready,
		Version:   h.version,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Capabilities: map[string]bool{
			"sign": true,
			"dkg":  true,
		},
		Checks: map[string]checkResult{
			"shares_dir": shareCheck,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	statusCode := http.StatusOK
	if !ready {
		statusCode = http.StatusServiceUnavailable
	}
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(resp)
}
```

- [ ] **Step 4: Run tests to verify they pass**

```bash
go test ./internal/health/... -v
```

Expected: PASS — all 3 tests green.

- [ ] **Step 5: Commit**

```bash
git add internal/health/
git commit -m "feat: add HTTP health handler"
```

---

## Task 10: gRPC server assembly

**Files:**
- Create: `internal/grpc/server.go`

- [ ] **Step 1: Implement gRPC server constructor**

`internal/grpc/server.go`:
```go
package grpc

import (
	"log/slog"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc"
)

func NewServer(svc *coretss.Service, store *session.Store, partyID, apiKey string, log *slog.Logger) *grpc.Server {
	srv := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryAuthInterceptor(apiKey)),
		grpc.StreamInterceptor(StreamAuthInterceptor(apiKey)),
	)
	mpcv1.RegisterControlServiceServer(srv, NewControlServer(svc, store, partyID, log))
	mpcv1.RegisterRelayServiceServer(srv, NewRelayServer(store))
	return srv
}
```

- [ ] **Step 2: Verify build**

```bash
go build ./internal/grpc/...
```

Expected: no errors.

- [ ] **Step 3: Commit**

```bash
git add internal/grpc/server.go
git commit -m "feat: assemble gRPC server with auth interceptors"
```

---

## Task 11: main.go — wiring and graceful shutdown

**Files:**
- Create: `cmd/co-signer/main.go`
- Delete: `main.go` (root bootstrap, replaced by cmd layout)

- [ ] **Step 1: Write cmd/co-signer/main.go**

```go
package main

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/config"
	cosgrpc "github.com/BroLabel/brosettlement-mpc-co-signer/internal/grpc"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/health"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
)

const version = "0.1.0"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	if err := os.MkdirAll(cfg.SharesDir, 0o700); err != nil {
		log.Error("failed to create shares dir", "err", err)
		os.Exit(1)
	}

	tssSvc := coretss.NewBnbService(log)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := tssSvc.StartPreParamsPool(ctx); err != nil {
		log.Error("failed to start pre-params pool", "err", err)
		os.Exit(1)
	}

	store := session.NewStore()

	// Background cleanup: remove terminal sessions after 5 minutes
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				store.Cleanup(5 * time.Minute)
			case <-ctx.Done():
				return
			}
		}
	}()

	grpcSrv := cosgrpc.NewServer(tssSvc, store, cfg.PartyID, cfg.APIKey, log)

	grpcLis, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		log.Error("failed to listen", "addr", cfg.GRPCAddr, "err", err)
		os.Exit(1)
	}

	go func() {
		log.Info("gRPC server starting", "addr", cfg.GRPCAddr)
		if err := grpcSrv.Serve(grpcLis); err != nil {
			log.Error("gRPC server stopped", "err", err)
		}
	}()

	healthSrv := &http.Server{
		Addr:    cfg.HTTPAddr,
		Handler: health.NewHandler(version, cfg.SharesDir),
	}
	go func() {
		log.Info("HTTP health server starting", "addr", cfg.HTTPAddr)
		if err := healthSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("health server stopped", "err", err)
		}
	}()

	// Wait for signal
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	log.Info("shutting down")

	// Stop accepting new gRPC calls; wait up to 30s for active sessions
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	done := make(chan struct{})
	go func() {
		grpcSrv.GracefulStop()
		close(done)
	}()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		grpcSrv.Stop()
	}

	cancel() // stop pre-params pool and cleanup ticker

	_ = healthSrv.Shutdown(context.Background())
	log.Info("shutdown complete")
}
```

- [ ] **Step 2: Remove root main.go**

```bash
rm main.go
```

- [ ] **Step 3: Verify build**

```bash
go build ./cmd/co-signer/...
```

Expected: binary builds with no errors.

- [ ] **Step 4: Run all tests**

```bash
go test ./... -v
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/ && git rm main.go
git commit -m "feat: wire up main entry point with graceful shutdown"
```

---

## Task 12: Final verification

- [ ] **Step 1: Run full test suite**

```bash
go test ./... -race -count=1
```

Expected: all tests pass, no race conditions detected.

- [ ] **Step 2: Build binary**

```bash
go build -o bin/co-signer ./cmd/co-signer
```

Expected: `bin/co-signer` created.

- [ ] **Step 3: Smoke test**

```bash
CO_SIGNER_API_KEY=test CO_SIGNER_PARTY_ID=p1 ./bin/co-signer &
sleep 1
curl -s http://localhost:8081/health | jq .
kill %1
```

Expected: JSON response with `"status": "ok"` (or `"fail"` if `./data/shares` doesn't exist — create it first with `mkdir -p data/shares`).

- [ ] **Step 4: Commit**

```bash
git add bin/ .gitignore   # add bin/ to .gitignore if not already there
git commit -m "chore: final build verification"
```
