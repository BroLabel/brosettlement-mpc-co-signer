package grpc

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

func TestUnaryAuth_ValidAPIKey_AllowsRequest(t *testing.T) {
	interceptor := UnaryAuthInterceptor("test-api-key")
	called := false

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-api-key", "test-api-key"),
	)

	_, err := interceptor(
		ctx,
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/mpc.v1.ControlService/StartSign"},
		func(context.Context, any) (any, error) {
			called = true
			return "ok", nil
		},
	)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if !called {
		t.Fatal("expected handler to be called")
	}
}

func TestUnaryAuth_InvalidAPIKey_ReturnsUnauthenticated(t *testing.T) {
	interceptor := UnaryAuthInterceptor("test-api-key")
	called := false

	ctx := metadata.NewIncomingContext(
		context.Background(),
		metadata.Pairs("x-api-key", "wrong-key"),
	)

	_, err := interceptor(
		ctx,
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/mpc.v1.ControlService/StartSign"},
		func(context.Context, any) (any, error) {
			called = true
			return "ok", nil
		},
	)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if called {
		t.Fatal("expected handler not to be called")
	}
}

func TestUnaryAuth_MissingAPIKey_ReturnsUnauthenticated(t *testing.T) {
	interceptor := UnaryAuthInterceptor("test-api-key")
	called := false

	_, err := interceptor(
		context.Background(),
		struct{}{},
		&grpc.UnaryServerInfo{FullMethod: "/mpc.v1.ControlService/StartSign"},
		func(context.Context, any) (any, error) {
			called = true
			return "ok", nil
		},
	)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated, got %v", err)
	}
	if called {
		t.Fatal("expected handler not to be called")
	}
}
