package grpc

import (
	"context"
	"errors"
	"strings"
	"time"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/transport"
	"github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type tssRunner interface {
	RunDKGSession(ctx context.Context, req tss.DKGSessionRequest) error
	RunSignSession(ctx context.Context, req tss.SignSessionRequest) error
}

type ControlServer struct {
	mpcv1.UnimplementedControlServiceServer

	runner       tssRunner
	store        *session.Store
	localPartyID string
}

func NewControlServer(runner tssRunner, store *session.Store, localPartyID string) *ControlServer {
	return &ControlServer{
		runner:       runner,
		store:        store,
		localPartyID: localPartyID,
	}
}

func (s *ControlServer) StartDkg(_ context.Context, req *mpcv1.StartDkgRequest) (*mpcv1.StartDkgResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	cfg, err := validateSessionConfig(req.GetConfig())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetSessionId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}

	if existing, ok := s.store.Get(req.GetSessionId()); ok {
		return &mpcv1.StartDkgResponse{
			SessionId: existing.ID,
			Status:    toProtoStatus(existing.State),
		}, nil
	}

	sessionTTL := time.Duration(cfg.GetSessionTtlSeconds()) * time.Second
	tr := transport.NewStreamTransport(128)
	s.store.Create(req.GetSessionId(), cfg.GetOrgId(), time.Now().Add(sessionTTL), tr)

	go s.runDKG(req, cfg, tr, sessionTTL)

	return &mpcv1.StartDkgResponse{
		SessionId: req.GetSessionId(),
		Status:    mpcv1.SessionStatus_SESSION_STATUS_RUNNING,
	}, nil
}

func (s *ControlServer) StartSign(_ context.Context, req *mpcv1.StartSignRequest) (*mpcv1.StartSignResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "request is required")
	}
	cfg, err := validateSessionConfig(req.GetConfig())
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(req.GetSessionId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "session_id is required")
	}
	if strings.TrimSpace(req.GetKeyId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "key_id is required")
	}
	if len(req.GetDigest()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "digest is required")
	}

	if existing, ok := s.store.Get(req.GetSessionId()); ok {
		return &mpcv1.StartSignResponse{
			SessionId: existing.ID,
			Status:    toProtoStatus(existing.State),
		}, nil
	}

	parties := req.GetParticipants()
	if len(parties) == 0 {
		parties = cfg.GetParties()
	}
	sessionTTL := time.Duration(cfg.GetSessionTtlSeconds()) * time.Second
	tr := transport.NewStreamTransport(128)
	s.store.Create(req.GetSessionId(), cfg.GetOrgId(), time.Now().Add(sessionTTL), tr)

	go s.runSign(req, cfg, parties, tr, sessionTTL)

	return &mpcv1.StartSignResponse{
		SessionId: req.GetSessionId(),
		Status:    mpcv1.SessionStatus_SESSION_STATUS_RUNNING,
	}, nil
}

func (s *ControlServer) runDKG(req *mpcv1.StartDkgRequest, cfg *mpcv1.SessionConfig, tr *transport.StreamTransport, ttl time.Duration) {
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()

	err := s.runner.RunDKGSession(ctx, tss.DKGSessionRequest{
		Session: tss.SessionDescriptor{
			SessionID: req.GetSessionId(),
			OrgID:     cfg.GetOrgId(),
			Parties:   append([]string(nil), cfg.GetParties()...),
			Threshold: cfg.GetThreshold(),
			Algorithm: cfg.GetAlgorithm(),
			Curve:     cfg.GetCurve(),
			Chain:     cfg.GetChain(),
		},
		LocalPartyID: s.localPartyID,
		Transport:    tr,
	})
	s.store.SetState(req.GetSessionId(), toSessionState(err, ctx))
}

func (s *ControlServer) runSign(req *mpcv1.StartSignRequest, cfg *mpcv1.SessionConfig, parties []string, tr *transport.StreamTransport, ttl time.Duration) {
	defer tr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), ttl)
	defer cancel()

	err := s.runner.RunSignSession(ctx, tss.SignSessionRequest{
		Session: tss.SessionDescriptor{
			SessionID: req.GetSessionId(),
			OrgID:     cfg.GetOrgId(),
			KeyID:     req.GetKeyId(),
			Parties:   append([]string(nil), parties...),
			Threshold: cfg.GetThreshold(),
			Algorithm: cfg.GetAlgorithm(),
			Curve:     cfg.GetCurve(),
			Chain:     cfg.GetChain(),
		},
		LocalPartyID: s.localPartyID,
		Digest:       append([]byte(nil), req.GetDigest()...),
		Transport:    tr,
	})
	s.store.SetState(req.GetSessionId(), toSessionState(err, ctx))
}

func validateSessionConfig(cfg *mpcv1.SessionConfig) (*mpcv1.SessionConfig, error) {
	if cfg == nil {
		return nil, status.Error(codes.InvalidArgument, "config is required")
	}
	if strings.TrimSpace(cfg.GetOrgId()) == "" {
		return nil, status.Error(codes.InvalidArgument, "config.org_id is required")
	}
	if len(cfg.GetParties()) == 0 {
		return nil, status.Error(codes.InvalidArgument, "config.parties is required")
	}
	if cfg.GetThreshold() == 0 {
		return nil, status.Error(codes.InvalidArgument, "config.threshold is required")
	}
	if cfg.GetSessionTtlSeconds() == 0 {
		return nil, status.Error(codes.InvalidArgument, "config.session_ttl_seconds is required")
	}
	return cfg, nil
}

func toSessionState(err error, runCtx context.Context) session.State {
	if err == nil {
		return session.StateCompleted
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return session.StateTimedOut
	}
	return session.StateFailed
}

func toProtoStatus(st session.State) mpcv1.SessionStatus {
	switch st {
	case session.StateRunning:
		return mpcv1.SessionStatus_SESSION_STATUS_RUNNING
	case session.StateCompleted:
		return mpcv1.SessionStatus_SESSION_STATUS_COMPLETED
	case session.StateTimedOut:
		return mpcv1.SessionStatus_SESSION_STATUS_TIMED_OUT
	case session.StateFailed:
		return mpcv1.SessionStatus_SESSION_STATUS_FAILED
	default:
		return mpcv1.SessionStatus_SESSION_STATUS_UNSPECIFIED
	}
}
