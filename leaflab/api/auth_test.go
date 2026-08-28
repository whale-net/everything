package main

import (
	"context"
	"testing"

	"github.com/whale-net/everything/leaflab/api/contract"
	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// alwaysOKUnary/alwaysOKStream are minimal grpc.UnaryHandler/grpc.StreamHandler
// stand-ins that record whether they were reached, so these tests assert on
// NewAuthEnforcementUnaryInterceptor/NewAuthEnforcementStreamInterceptor in
// isolation -- ContextWithClaims (exported by grpcauth exactly for this
// purpose, see its doc comment) stands in for grpcauth's own interceptor
// having already run.
func alwaysOKUnary(reached *bool) grpc.UnaryHandler {
	return func(ctx context.Context, req interface{}) (interface{}, error) {
		*reached = true
		return "ok", nil
	}
}

func alwaysOKStream(reached *bool) grpc.StreamHandler {
	return func(srv interface{}, ss grpc.ServerStream) error {
		*reached = true
		return nil
	}
}

func TestAuthEnforcementUnary_NoClaims_Unauthenticated(t *testing.T) {
	interceptor := NewAuthEnforcementUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/leaflab.api.v1.LeafLabAPI/PushDeviceConfig"}

	var reached bool
	_, err := interceptor(context.Background(), nil, info, alwaysOKUnary(&reached))

	if reached {
		t.Fatal("handler was reached despite no Claims in context")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got code %v, want Unauthenticated: %v", status.Code(err), err)
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureUnauthenticated) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureUnauthenticated)
	}
}

func TestAuthEnforcementUnary_WithClaims_Reaches(t *testing.T) {
	interceptor := NewAuthEnforcementUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: "/leaflab.api.v1.LeafLabAPI/PushDeviceConfig"}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "device-owner"})

	var reached bool
	resp, err := interceptor(ctx, nil, info, alwaysOKUnary(&reached))

	if !reached {
		t.Fatal("handler was not reached despite valid Claims in context")
	}
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp != "ok" {
		t.Errorf("resp = %v, want %q", resp, "ok")
	}
}

func TestAuthEnforcementUnary_GetHealth_AnonymousReaches(t *testing.T) {
	interceptor := NewAuthEnforcementUnaryInterceptor()
	info := &grpc.UnaryServerInfo{FullMethod: healthFullMethod}

	var reached bool
	_, err := interceptor(context.Background(), nil, info, alwaysOKUnary(&reached))

	if !reached {
		t.Fatal("GetHealth handler was not reached despite no Claims -- it must be the one anonymous RPC (FR63.2)")
	}
	if err != nil {
		t.Fatalf("unexpected error for the anonymous allowlisted method: %v", err)
	}
}

func TestAnonymousMethods_ContainsExactlyGetHealth(t *testing.T) {
	if len(anonymousMethods) != 1 {
		t.Fatalf("anonymousMethods has %d entries, want exactly 1 (FR63.2/validation criteria): %v", len(anonymousMethods), anonymousMethods)
	}
	if !anonymousMethods[healthFullMethod] {
		t.Errorf("anonymousMethods does not contain %q", healthFullMethod)
	}
}

type fakeServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (f *fakeServerStream) Context() context.Context { return f.ctx }

func TestAuthEnforcementStream_NoClaims_Unauthenticated(t *testing.T) {
	interceptor := NewAuthEnforcementStreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/leaflab.api.v1.LeafLabAPI/ListBoards"}
	ss := &fakeServerStream{ctx: context.Background()}

	var reached bool
	err := interceptor(nil, ss, info, alwaysOKStream(&reached))

	if reached {
		t.Fatal("stream handler was reached despite no Claims in context")
	}
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("got code %v, want Unauthenticated: %v", status.Code(err), err)
	}
}

func TestAuthEnforcementStream_WithClaims_Reaches(t *testing.T) {
	interceptor := NewAuthEnforcementStreamInterceptor()
	info := &grpc.StreamServerInfo{FullMethod: "/leaflab.api.v1.LeafLabAPI/ListBoards"}
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "device-owner"})
	ss := &fakeServerStream{ctx: ctx}

	var reached bool
	if err := interceptor(nil, ss, info, alwaysOKStream(&reached)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reached {
		t.Fatal("stream handler was not reached despite valid Claims in context")
	}
}

func TestIsAdminEligible(t *testing.T) {
	cases := []struct {
		name string
		ctx  context.Context
		want bool
	}{
		{"no claims", context.Background(), false},
		{"claims, no roles", grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "s"}), false},
		{"claims, unrelated role", grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "s", Roles: []string{"leaflab-viewer"}}), false},
		{"claims, admin role", grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "s", Roles: []string{"leaflab-admin"}}), true},
		{"claims, admin role among others", grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "s", Roles: []string{"leaflab-viewer", "leaflab-admin"}}), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isAdminEligible(tc.ctx); got != tc.want {
				t.Errorf("isAdminEligible() = %v, want %v", got, tc.want)
			}
		})
	}
}
