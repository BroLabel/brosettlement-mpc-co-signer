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
	first  *protocol.Frame
}

func (s *grpcRelayStream) Send(frame protocol.Frame) error {
	return s.stream.Send(protoFromFrame(frame))
}

func (s *grpcRelayStream) Recv() (protocol.Frame, error) {
	if s.first != nil {
		frame := *s.first
		s.first = nil
		return frame, nil
	}

	pb, err := s.stream.Recv()
	if err != nil {
		return protocol.Frame{}, err
	}
	return frameFromProto(pb), nil
}

func (s *grpcRelayStream) Context() context.Context {
	return s.stream.Context()
}

func (s *RelayServer) Connect(stream mpcv1.RelayService_ConnectServer) error {
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
	if first.GetOrgId() != "" && sess.OrgID != "" && first.GetOrgId() != sess.OrgID {
		return status.Error(codes.FailedPrecondition, "session org mismatch")
	}

	firstFrame := frameFromProto(first)
	relayStream := &grpcRelayStream{
		stream: stream,
		first:  &firstFrame,
	}
	if err := sess.Transport.Attach(relayStream); err != nil {
		return status.Errorf(codes.FailedPrecondition, "attach stream: %v", err)
	}
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
		Payload:       append([]byte(nil), pb.GetPayload()...),
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

func protoFromFrame(frame protocol.Frame) *mpcv1.Frame {
	resp := &mpcv1.Frame{
		SessionId:     frame.SessionID,
		OrgId:         frame.OrgID,
		MessageId:     frame.MessageID,
		Seq:           frame.Seq,
		Round:         frame.Round,
		FromParty:     frame.FromParty,
		ToParty:       frame.ToParty,
		Payload:       append([]byte(nil), frame.Payload...),
		CorrelationId: frame.CorrelationID,
		Stage:         frame.Stage,
		RoundHint:     frame.RoundHint,
		Broadcast:     frame.Broadcast,
		Protocol:      frame.Protocol,
		MessageType:   frame.MessageType,
		PayloadHash:   frame.PayloadHash,
	}
	if !frame.SentAt.IsZero() {
		resp.SentAt = timestamppb.New(frame.SentAt)
	}
	return resp
}
