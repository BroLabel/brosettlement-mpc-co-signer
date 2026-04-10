package grpc

import (
	"context"
	"crypto/subtle"

	grpcpkg "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func UnaryAuthInterceptor(expectedAPIKey string) grpcpkg.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		req any,
		info *grpcpkg.UnaryServerInfo,
		handler grpcpkg.UnaryHandler,
	) (any, error) {
		if err := checkAPIKey(ctx, expectedAPIKey); err != nil {
			return nil, err
		}

		return handler(ctx, req)
	}
}

func StreamAuthInterceptor(expectedAPIKey string) grpcpkg.StreamServerInterceptor {
	return func(
		srv any,
		ss grpcpkg.ServerStream,
		info *grpcpkg.StreamServerInfo,
		handler grpcpkg.StreamHandler,
	) error {
		if err := checkAPIKey(ss.Context(), expectedAPIKey); err != nil {
			return err
		}

		return handler(srv, ss)
	}
}

func checkAPIKey(ctx context.Context, expectedAPIKey string) error {
	if expectedAPIKey == "" {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}

	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}

	values := md.Get("x-api-key")
	if len(values) == 0 {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}

	if subtle.ConstantTimeCompare([]byte(values[0]), []byte(expectedAPIKey)) != 1 {
		return status.Error(codes.Unauthenticated, "unauthenticated")
	}

	return nil
}
