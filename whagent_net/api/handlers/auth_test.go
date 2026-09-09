package handlers_test

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"github.com/whale-net/everything/whagent_net/api/handlers"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestRequireClaimsUnaryInterceptor_NoClaims_RejectsBeforeHandlerRuns proves
// a call with no grpcauth.Claims in its context (the shape an anonymous or
// bad-token OIDC call reaches this interceptor with, since grpcauth's own
// OIDC interceptor lets such a call proceed with no claims rather than
// erroring itself -- see grpcauth.authenticate) is rejected with
// codes.Unauthenticated, and that the wrapped handler is never invoked.
// This is the mechanism the issue's Testing section means by "Unauthenticated
// and bad-token calls to every RPC are rejected by the interceptor before
// any handler runs" -- RequireClaimsUnaryInterceptor is wired unconditionally
// ahead of every SessionService method (whagent_net/api/main.go), so this
// one interceptor-level test covers all six RPCs at once; a per-RPC
// duplicate would only re-prove the same interceptor runs first.
func TestRequireClaimsUnaryInterceptor_NoClaims_RejectsBeforeHandlerRuns(t *testing.T) {
	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := handlers.RequireClaimsUnaryInterceptor(context.Background(), nil, &grpc.UnaryServerInfo{FullMethod: "/whagent.v1.SessionService/GetSession"}, handler)

	if handlerCalled {
		t.Fatal("handler must not run when no claims are present in the context")
	}
	if resp != nil {
		t.Fatalf("expected a nil response on rejection, got %v", resp)
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v", err)
	}
}

// TestRequireClaimsUnaryInterceptor_WithClaims_CallsHandler proves a call
// that already carries grpcauth.Claims (the shape AuthModeNone's dev-claims
// injection and a successfully-verified AuthModeOIDC token both produce)
// passes straight through to the handler.
func TestRequireClaimsUnaryInterceptor_WithClaims_CallsHandler(t *testing.T) {
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "dev-user"})
	handlerCalled := false
	handler := func(ctx context.Context, req interface{}) (interface{}, error) {
		handlerCalled = true
		return "ok", nil
	}

	resp, err := handlers.RequireClaimsUnaryInterceptor(ctx, nil, &grpc.UnaryServerInfo{FullMethod: "/whagent.v1.SessionService/GetSession"}, handler)

	if !handlerCalled {
		t.Fatal("handler must run once claims are present in the context")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("expected the handler's own response to pass through, got %v", resp)
	}
}

// fakeServerStream is the minimal grpc.ServerStream needed to exercise
// RequireClaimsStreamInterceptor without a real network connection.
type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

// TestRequireClaimsStreamInterceptor_NoClaims_RejectsBeforeHandlerRuns is
// RequireClaimsUnaryInterceptor's stream analog (no streaming RPC exists on
// SessionService today, but the interceptor is wired for the day one does --
// see auth.go's doc comment).
func TestRequireClaimsStreamInterceptor_NoClaims_RejectsBeforeHandlerRuns(t *testing.T) {
	handlerCalled := false
	handler := func(srv interface{}, ss grpc.ServerStream) error {
		handlerCalled = true
		return nil
	}

	err := handlers.RequireClaimsStreamInterceptor(nil, &fakeServerStream{ctx: context.Background()}, &grpc.StreamServerInfo{FullMethod: "/whagent.v1.SessionService/ReadTranscript"}, handler)

	if handlerCalled {
		t.Fatal("handler must not run when no claims are present in the stream context")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected codes.Unauthenticated, got %v", err)
	}
}
