package grpc

import (
	"log/slog"

	mpcv1 "github.com/BroLabel/brosettlement-mpc-co-signer/api/proto/mpc/v1"
	"github.com/BroLabel/brosettlement-mpc-co-signer/internal/session"
	coretss "github.com/BroLabel/brosettlement-mpc-core/tss"
	"google.golang.org/grpc"
)

func NewServer(svc *coretss.Service, store *session.Store, partyID, apiKey string, log *slog.Logger) *grpc.Server {
	_ = log
	server := grpc.NewServer(
		grpc.UnaryInterceptor(UnaryAuthInterceptor(apiKey)),
		grpc.StreamInterceptor(StreamAuthInterceptor(apiKey)),
	)

	mpcv1.RegisterControlServiceServer(server, NewControlServer(svc, store, partyID))
	mpcv1.RegisterRelayServiceServer(server, NewRelayServer(store))

	return server
}
