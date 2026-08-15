package components

import (
	"fmt"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// HasRole reports whether user holds role by literal membership. No role
// implies another — this mirrors tools/app_registry/server/auth.Require's
// design rationale (see that package's doc comment), except this check is
// presentation-only: it gates what the UI renders, not what the API
// accepts. Enforcement remains solely in libs/go/grpcauth on the server
// side (NFR-14) — a UI-side role check is never a substitute for the
// server rejecting an unauthorized call.
//
// A nil Roles slice means the realm_access claim was absent from the
// token (deployment misconfiguration); HasRole treats that as "holds
// nothing" rather than panicking or silently granting access.
//
// In AuthModeNone, htmxauth sets user.Roles to its AllRoles sentinel
// ([]string{"*"}) so the dev user is treated as holding everything. That
// sentinel is checked explicitly by name here rather than folded into the
// literal membership loop, so it can never be confused with a real,
// service-defined role happening to be named "*".
func HasRole(user *htmxauth.UserInfo, role string) bool {
	if user == nil || user.Roles == nil {
		return false
	}
	for _, r := range user.Roles {
		if r == role || r == "*" {
			return true
		}
	}
	return false
}

// EnvironmentPromoterRole derives the per-environment promoter role name
// for envKey. Callers must always pass a key drawn from the live
// EnvironmentRegistry.ListEnvironments result (see EnvironmentPromoterRoles)
// — never a hardcoded dev/stage/prod list, since environments are
// operator-defined data, not a fixed enum (FR-42/FR-43).
func EnvironmentPromoterRole(envKey string) string {
	return fmt.Sprintf("app-registry-promoter-%s", envKey)
}

// EnvironmentPromoterRoles maps every environment ListEnvironments returned
// to its promoter role, in the same order. This is the only place gating
// logic should derive the set of promoter roles that exist — never a
// hardcoded literal list of environment keys.
func EnvironmentPromoterRoles(envs []*pb.Environment) []string {
	roles := make([]string, 0, len(envs))
	for _, env := range envs {
		roles = append(roles, EnvironmentPromoterRole(env.GetKey()))
	}
	return roles
}

// GateDecision is the result of checking whether a principal may use a
// role-gated control. Rendering must use this rather than reinventing the
// allow/deny + reason pair per screen — see the DisabledWithReason /
// GatedAction templ components in gate.templ, and the FR-44 standard: never
// render an enabled control guaranteed to be denied. Either omit the
// control, or render it disabled with the missing role named.
type GateDecision struct {
	Allowed     bool
	MissingRole string

	// Reason overrides the "Requires role: <MissingRole>" default title
	// when denial has nothing to do with a missing role — e.g. a mutating
	// control disabled because an "as of" historical instant is active
	// (FR-8). Empty means "use the default role-based message".
	Reason string
}

// Gate evaluates whether user holds role and returns the decision plus the
// role name to surface if denied.
func Gate(user *htmxauth.UserInfo, role string) GateDecision {
	return GateDecision{
		Allowed:     HasRole(user, role),
		MissingRole: role,
	}
}

// GateCellAction is Gate plus the FR-8 as-of rule the deployments matrix's
// per-cell promote/rollback controls need: a control is only ever enabled
// when the caller holds role AND the cell is showing current state, never
// while asOfActive shows a historical snapshot — a mutation must never be
// offered against state that isn't "now".
func GateCellAction(user *htmxauth.UserInfo, role string, asOfActive bool) GateDecision {
	if asOfActive {
		return GateDecision{
			Allowed: false,
			Reason:  "Disabled while viewing historical state (\"as of\") — mutating actions only apply to the current state.",
		}
	}
	return Gate(user, role)
}

// RolesMisconfigured implements the FR-59 distinction: true only when the
// realm_access.roles claim was absent from the token entirely (nil Roles) —
// almost always a missing Keycloak "Add to ID token" realm-roles mapper.
// A present-but-empty Roles slice (len == 0, non-nil) is a legitimate
// read-only viewer and must NOT trigger this.
//
// Trap (do not rediscover): this must be derived from the loaded
// session/user-info record at render time, not from auth middleware.
// AuthModeNone short-circuits inside the authenticator
// (libs/go/htmxauth/auth.go RequireAuth) and sets Roles to the non-nil
// AllRoles sentinel, so deriving the check from the loaded UserInfo record
// (as this function does) gets the AUTH_MODE=none exemption for free —
// no separate mode branch required here. A check placed in the auth
// wrapper instead would need its own AuthModeNone branch to avoid false
// positives.
func RolesMisconfigured(user *htmxauth.UserInfo) bool {
	return user != nil && user.Roles == nil
}
