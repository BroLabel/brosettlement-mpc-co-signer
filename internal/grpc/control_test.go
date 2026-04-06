package grpc

import (
	"context"
	"net"
	"testing"
	"time"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type fakeTSSRunner struct {
	runSignCalls chan runSignCall
	signErr      error
	waitSign     chan struct{}
}

type runSignCall struct {
	ctx context.Context
	req tss.SignSessionRequest
}

func newFakeTSSRunner() *fakeTSSRunner {
	return &fakeTSSRunner{
		runSignCalls: make(chan runSignCall, 1),
		waitSign:     make(chan struct{}),
	}
}

func (f *fakeTSSRunner) RunDKGSession(context.Context, tss.DKGSessionRequest) error {
	return nil
}

func (f *fakeTSSRunner) RunSignSession(ctx context.Context, req tss.SignSessionRequest) error {
	f.runSignCalls <- runSignCall{ctx: ctx, req: req}
	<-f.waitSign
	return f.signErr
}

func newControlTestClient(t *testing.T, server mpcv1.ControlServiceServer) (mpcv1.ControlServiceClient, func()) {
	t.Helper()

	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	mpcv1.RegisterControlServiceServer(grpcServer, server)
	go func() {
		_ = grpcServer.Serve(listener)
	}()

	dialer := func(context.Context, string) (net.Conn, error) {
		return listener.Dial()
	}
	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(dialer),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}

	cleanup := func() {
		_ = conn.Close()
		grpcServer.Stop()
		_ = listener.Close()
	}

	return mpcv1.NewControlServiceClient(conn), cleanup
}

func TestStartSign_MissingKeyID_ReturnsInvalidArgument(t *testing.T) {
	tssRunner := newFakeTSSRunner()
	store := session.NewStore()
	server := NewControlServer(tssRunner, store, "co-signer-1")
	client, cleanup := newControlTestClient(t, server)
	defer cleanup()

	_, err := client.StartSign(context.Background(), &mpcv1.StartSignRequest{
		SessionId: "session-1",
		Digest:    []byte("digest"),
		Config: &mpcv1.SessionConfig{
			OrgId:             "org-1",
			Threshold:         2,
			Parties:           []string{"p1", "p2", "p3"},
			SessionTtlSeconds: 30,
		},
		Participants: []string{"p1", "p2", "p3"},
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", err)
	}

	select {
	case <-tssRunner.runSignCalls:
		t.Fatal("RunSignSession should not be called on invalid request")
	default:
	}
}

func TestStartSign_CreatesSession(t *testing.T) {
	tssRunner := newFakeTSSRunner()
	store := session.NewStore()
	server := NewControlServer(tssRunner, store, "co-signer-1")
	client, cleanup := newControlTestClient(t, server)
	defer cleanup()
	defer close(tssRunner.waitSign)

	resp, err := client.StartSign(context.Background(), &mpcv1.StartSignRequest{
		SessionId: "session-2",
		KeyId:     "key-1",
		Digest:    []byte("digest"),
		Config: &mpcv1.SessionConfig{
			OrgId:             "org-1",
			Threshold:         2,
			Parties:           []string{"p1", "p2", "p3"},
			SessionTtlSeconds: 45,
			Algorithm:         "ecdsa",
			Chain:             "bnb",
		},
		Participants: []string{"p1", "p2", "p3"},
	})
	if err != nil {
		t.Fatalf("StartSign: %v", err)
	}
	if resp.GetSessionId() != "session-2" {
		t.Fatalf("got session_id=%q, want %q", resp.GetSessionId(), "session-2")
	}
	if resp.GetStatus() != mpcv1.SessionStatus_SESSION_STATUS_RUNNING {
		t.Fatalf("got status=%v, want RUNNING", resp.GetStatus())
	}

	sess, ok := store.Get("session-2")
	if !ok {
		t.Fatal("session not created in store")
	}
	if sess.State != session.StateRunning {
		t.Fatalf("got session state=%v, want Running", sess.State)
	}

	select {
	case call := <-tssRunner.runSignCalls:
		if call.req.Session.SessionID != "session-2" {
			t.Fatalf("got sign session id=%q, want %q", call.req.Session.SessionID, "session-2")
		}
		if call.req.Session.OrgID != "org-1" {
			t.Fatalf("got org id=%q, want %q", call.req.Session.OrgID, "org-1")
		}
		if call.req.Session.KeyID != "key-1" {
			t.Fatalf("got key id=%q, want %q", call.req.Session.KeyID, "key-1")
		}
		if len(call.req.Digest) == 0 {
			t.Fatal("expected non-empty digest")
		}
		if _, ok := call.ctx.Deadline(); !ok {
			t.Fatal("expected sign context to have deadline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunSignSession was not called")
	}
}
