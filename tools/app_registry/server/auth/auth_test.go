package auth

import (
	"context"
	"testing"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func ctxWithRoles(roles ...string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{
		Subject: "test-user",
		Roles:   roles,
	})
}

func TestRequire_NoClaimsIsUnauthenticated(t *testing.T) {
	err := Require(context.Background(), RoleBuilder)
	if status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated with no claims, got %v", err)
	}
}

func TestRequire_CorrectRoleAllowed(t *testing.T) {
	err := Require(ctxWithRoles(RoleBuilder), RoleBuilder)
	if err != nil {
		t.Fatalf("expected nil error for a principal holding the role, got %v", err)
	}
}

func TestRequire_WrongRoleIsPermissionDenied(t *testing.T) {
	err := Require(ctxWithRoles(RolePromoterProd), RoleBuilder)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected PermissionDenied for a principal lacking the role, got %v", err)
	}
}

// TestRequire_AdminDoesNotImplyBuilder pins the "flat and explicit roles, no
// implication" decision documented on Require: a principal holding only
// RoleAdmin must still be rejected from a builder-only check.
func TestRequire_AdminDoesNotImplyBuilder(t *testing.T) {
	err := Require(ctxWithRoles(RoleAdmin), RoleBuilder)
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected admin-only claims to be rejected by a builder check, got %v", err)
	}
}

func TestRequirePromoter_MapsEnvironmentToRole(t *testing.T) {
	if err := RequirePromoter(ctxWithRoles(RolePromoterProd), "prod"); err != nil {
		t.Fatalf("expected prod promoter to pass the prod check, got %v", err)
	}
	err := RequirePromoter(ctxWithRoles(RolePromoterDev), "prod")
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("expected dev promoter to be rejected from the prod check, got %v", err)
	}
}

func TestRequireAuthenticated(t *testing.T) {
	if err := RequireAuthenticated(context.Background()); status.Code(err) != codes.Unauthenticated {
		t.Fatalf("expected Unauthenticated with no claims, got %v", err)
	}
	if err := RequireAuthenticated(ctxWithRoles()); err != nil {
		t.Fatalf("expected any authenticated principal (even with no roles) to pass, got %v", err)
	}
}
