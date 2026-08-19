package grpcauth

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type mockVerifier struct {
	verifyFn func(ctx context.Context, rawToken string) (*Claims, error)
}

func (m *mockVerifier) Verify(ctx context.Context, rawToken string) (*Claims, error) {
	if m.verifyFn != nil {
		return m.verifyFn(ctx, rawToken)
	}
	return nil, errors.New("unimplemented")
}

func TestAuthenticate_AuthModeNone(t *testing.T) {
	devClaims := &Claims{Subject: "dev", Roles: []string{"admin"}}
	ctx, err := authenticate(context.Background(), AuthModeNone, nil, devClaims)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		t.Fatal("expected claims in context")
	}
	if claims.Subject != "dev" {
		t.Errorf("got subject %q, want 'dev'", claims.Subject)
	}
}

func TestAuthenticate_AuthModeOIDC_Anonymous(t *testing.T) {
	verifier := &mockVerifier{}
	devClaims := &Claims{Subject: "dev", Roles: []string{"admin"}}

	// Case 1: No metadata in context
	ctx, err := authenticate(context.Background(), AuthModeOIDC, verifier, devClaims)
	if err != nil {
		t.Fatalf("unexpected error for anonymous request (no metadata): %v", err)
	}
	if _, ok := ClaimsFromContext(ctx); ok {
		t.Fatal("expected no claims in anonymous context")
	}

	// Case 2: Metadata present, but no authorization key
	ctxWithMD := metadata.NewIncomingContext(context.Background(), metadata.Pairs("user-agent", "grpc-go"))
	ctx, err = authenticate(ctxWithMD, AuthModeOIDC, verifier, devClaims)
	if err != nil {
		t.Fatalf("unexpected error for anonymous request (missing auth header): %v", err)
	}
	if _, ok := ClaimsFromContext(ctx); ok {
		t.Fatal("expected no claims in anonymous context")
	}

	// Case 3: Empty authorization header
	ctxWithEmptyAuth := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", ""))
	ctx, err = authenticate(ctxWithEmptyAuth, AuthModeOIDC, verifier, devClaims)
	if err != nil {
		t.Fatalf("unexpected error for empty auth header: %v", err)
	}
	if _, ok := ClaimsFromContext(ctx); ok {
		t.Fatal("expected no claims in anonymous context")
	}
}

func TestAuthenticate_AuthModeOIDC_InvalidFormat(t *testing.T) {
	verifier := &mockVerifier{}
	devClaims := &Claims{Subject: "dev", Roles: []string{"admin"}}

	ctxWithBasic := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Basic dXNlcjpwYXNz"))
	_, err := authenticate(ctxWithBasic, AuthModeOIDC, verifier, devClaims)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated for non-Bearer auth header, got %v", err)
	}
}

func TestAuthenticate_AuthModeOIDC_InvalidToken(t *testing.T) {
	verifier := &mockVerifier{
		verifyFn: func(ctx context.Context, rawToken string) (*Claims, error) {
			return nil, errors.New("token expired")
		},
	}
	devClaims := &Claims{Subject: "dev", Roles: []string{"admin"}}

	ctxWithToken := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer invalid-token"))
	_, err := authenticate(ctxWithToken, AuthModeOIDC, verifier, devClaims)
	if status.Code(err) != codes.Unauthenticated {
		t.Errorf("expected Unauthenticated for invalid token, got %v", err)
	}
}

func TestAuthenticate_AuthModeOIDC_ValidToken(t *testing.T) {
	expectedClaims := &Claims{Subject: "service-acct", Roles: []string{"app-registry-builder"}}
	verifier := &mockVerifier{
		verifyFn: func(ctx context.Context, rawToken string) (*Claims, error) {
			if rawToken == "valid-token" {
				return expectedClaims, nil
			}
			return nil, errors.New("invalid")
		},
	}
	devClaims := &Claims{Subject: "dev", Roles: []string{"admin"}}

	ctxWithToken := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer valid-token"))
	ctx, err := authenticate(ctxWithToken, AuthModeOIDC, verifier, devClaims)
	if err != nil {
		t.Fatalf("unexpected error for valid token: %v", err)
	}
	claims, ok := ClaimsFromContext(ctx)
	if !ok {
		t.Fatal("expected claims in context")
	}
	if claims.Subject != "service-acct" {
		t.Errorf("got subject %q, want 'service-acct'", claims.Subject)
	}
}
