// Package auth implements the App Registry's role model and its enforcement,
// on top of libs/go/grpcauth. See ../../../../libs/go/grpcauth/KEYCLOAK.md
// for the Keycloak-side design this package assumes (realm roles only,
// service-name-prefixed) and ../../ARCHITECTURE.md "Authorization" for the
// role table.
package auth

import (
	"context"

	"github.com/whale-net/everything/libs/go/grpcauth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Realm role names, exactly as configured in Keycloak — see KEYCLOAK.md
// section 2 "Decide your roles before you start". Keep these in sync with
// ARCHITECTURE.md's Authorization table; both must match the Keycloak realm
// role names character for character or every check silently fails.
const (
	RoleBuilder       = "app-registry-builder"
	RolePromoterDev   = "app-registry-promoter-dev"
	RolePromoterStage = "app-registry-promoter-stage"
	RolePromoterProd  = "app-registry-promoter-prod"
	RoleAdmin         = "app-registry-admin"
)

// AllRoles lists every role this service defines. Used to build the
// AuthModeNone dev-claims override (see main.go) so local development and
// dev-mode CI hold every role, and by tests that want an "authenticated as
// everything" principal.
func AllRoles() []string {
	return []string{RoleBuilder, RolePromoterDev, RolePromoterStage, RolePromoterProd, RoleAdmin}
}

// Require returns nil if ctx's claims hold role, codes.Unauthenticated if
// ctx carries no claims at all (the interceptor never ran, or auth mode is
// misconfigured), and codes.PermissionDenied if the principal is
// authenticated but lacks role.
//
// Roles are flat and explicit by design — no role implies another. admin
// does NOT imply builder or promoter, and a promoter role does not imply
// another environment's promoter role. A principal must hold literally
// every role a call requires. This keeps "who can do what" answerable by
// grep instead of by chasing an implication graph, and it is the property
// KEYCLOAK.md's credential model relies on: a builder credential's token
// simply does not carry a promoter role, so a compromised build job cannot
// promote no matter what it does with the secret it holds.
func Require(ctx context.Context, role string) error {
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return status.Error(codes.Unauthenticated, "request carries no credentials")
	}
	if !hasRole(claims, role) {
		return status.Errorf(codes.PermissionDenied, "requires role %q", role)
	}
	return nil
}

// RequirePromoter maps an environment name to its promoter role
// (app-registry-promoter-<env>) and requires it. AR-3b/3c call this
// unmodified for PromotionRegistry writes — the whole point of doing auth
// first is that this mapping needs no change once promotion logic exists.
func RequirePromoter(ctx context.Context, env string) error {
	return Require(ctx, "app-registry-promoter-"+env)
}

// RequireAuthenticated returns codes.Unauthenticated if ctx carries no
// claims, and nil otherwise — no specific role is checked. For read RPCs,
// which ARCHITECTURE.md's Authorization table opens to any authenticated
// principal.
func RequireAuthenticated(ctx context.Context) error {
	if _, ok := grpcauth.ClaimsFromContext(ctx); !ok {
		return status.Error(codes.Unauthenticated, "request carries no credentials")
	}
	return nil
}

func hasRole(claims *grpcauth.Claims, role string) bool {
	for _, r := range claims.Roles {
		if r == role {
			return true
		}
	}
	return false
}
