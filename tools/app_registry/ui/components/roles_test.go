package components

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxauth"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// --- HasRole (FR-42/FR-44, NFR-14 presentation-only design) ---------------

func TestHasRole_NilUser_Denies(t *testing.T) {
	if HasRole(nil, RoleAdmin) {
		t.Error("HasRole(nil, ...) = true, want false")
	}
}

// A nil Roles slice means the realm_access claim was absent from the token
// entirely (deployment misconfiguration) -- must be treated as "holds
// nothing", never panic, never silently grant.
func TestHasRole_NilRoles_DeniesEverything(t *testing.T) {
	user := &htmxauth.UserInfo{Roles: nil}
	for _, role := range []string{RoleAdmin, EnvironmentPromoterRole("dev")} {
		if HasRole(user, role) {
			t.Errorf("HasRole(user with nil Roles, %q) = true, want false", role)
		}
	}
}

// A present-but-empty Roles slice is a legitimate read-only viewer -- also
// denies every role, but for a different reason than the nil case (see
// RolesMisconfigured, which must distinguish the two).
func TestHasRole_EmptyRoles_DeniesEverything(t *testing.T) {
	user := &htmxauth.UserInfo{Roles: []string{}}
	for _, role := range []string{RoleAdmin, EnvironmentPromoterRole("dev")} {
		if HasRole(user, role) {
			t.Errorf("HasRole(user with empty Roles, %q) = true, want false", role)
		}
	}
}

// No role implies another (design note on HasRole / server/auth.Require):
// holding app-registry-admin must not grant a promoter role and vice versa.
func TestHasRole_NoRoleImpliesAnother(t *testing.T) {
	admin := &htmxauth.UserInfo{Roles: []string{RoleAdmin}}
	if !HasRole(admin, RoleAdmin) {
		t.Fatal("admin user does not hold RoleAdmin -- test setup broken")
	}
	if HasRole(admin, EnvironmentPromoterRole("dev")) {
		t.Error("holding app-registry-admin must not imply app-registry-promoter-dev (no role implies another)")
	}

	promoter := &htmxauth.UserInfo{Roles: []string{EnvironmentPromoterRole("dev")}}
	if !HasRole(promoter, EnvironmentPromoterRole("dev")) {
		t.Fatal("promoter-dev user does not hold its own role -- test setup broken")
	}
	if HasRole(promoter, RoleAdmin) {
		t.Error("holding app-registry-promoter-dev must not imply app-registry-admin (no role implies another)")
	}
	// A dev promoter must not implicitly gain a DIFFERENT environment's
	// promoter role either -- literal membership only, per environment.
	if HasRole(promoter, EnvironmentPromoterRole("stage")) {
		t.Error("holding app-registry-promoter-dev must not imply app-registry-promoter-stage")
	}
}

// AuthModeNone's AllRoles sentinel ([]string{"*"}) must be checked
// explicitly by name -- it grants every role, including one the caller
// never enumerated by name (e.g. a brand-new environment's promoter role).
func TestHasRole_AllRolesSentinel_GrantsEverything(t *testing.T) {
	user := &htmxauth.UserInfo{Roles: htmxauth.AllRoles}
	for _, role := range []string{RoleAdmin, EnvironmentPromoterRole("dev"), EnvironmentPromoterRole("a-brand-new-environment")} {
		if !HasRole(user, role) {
			t.Errorf("HasRole(AllRoles sentinel user, %q) = false, want true", role)
		}
	}
}

// A real role happening to be literally named "*" must never be confused
// with the sentinel by coincidence -- HasRole's literal-membership loop
// already handles this (r == role matches "*" == "*" too), documented here
// so the distinction is never accidentally broken by a future refactor.
func TestHasRole_LiteralAsteriskRole_StillMatchesSentinelCheck(t *testing.T) {
	user := &htmxauth.UserInfo{Roles: []string{"*"}}
	if !HasRole(user, RoleAdmin) {
		t.Error("a user whose Roles literally contains \"*\" grants every role by the same sentinel semantics as AllRoles")
	}
}

// --- EnvironmentPromoterRole(s) derivation (FR-42/FR-43) -------------------

func TestEnvironmentPromoterRole_Derivation(t *testing.T) {
	cases := []struct {
		envKey string
		want   string
	}{
		{"dev", "app-registry-promoter-dev"},
		{"stage", "app-registry-promoter-stage"},
		{"prod", "app-registry-promoter-prod"},
		// FR-43: the derivation must work for an operator-defined
		// environment key that is not one of the three usual suspects --
		// never a hardcoded dev/stage/prod list.
		{"canary-us-east", "app-registry-promoter-canary-us-east"},
	}
	for _, c := range cases {
		if got := EnvironmentPromoterRole(c.envKey); got != c.want {
			t.Errorf("EnvironmentPromoterRole(%q) = %q, want %q", c.envKey, got, c.want)
		}
	}
}

func TestEnvironmentPromoterRoles_MapsEveryEnvironmentInOrder(t *testing.T) {
	envs := []*pb.Environment{
		{Key: "dev"},
		{Key: "canary-us-east"},
		{Key: "prod"},
	}
	got := EnvironmentPromoterRoles(envs)
	want := []string{
		"app-registry-promoter-dev",
		"app-registry-promoter-canary-us-east",
		"app-registry-promoter-prod",
	}
	if len(got) != len(want) {
		t.Fatalf("EnvironmentPromoterRoles returned %d roles, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("EnvironmentPromoterRoles[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// --- Gate / GateCellAction (FR-8, FR-44) -----------------------------------

func TestGate_AllowedAndDenied(t *testing.T) {
	admin := &htmxauth.UserInfo{Roles: []string{RoleAdmin}}
	viewer := &htmxauth.UserInfo{Roles: []string{}}

	if d := Gate(admin, RoleAdmin); !d.Allowed {
		t.Error("Gate(admin, RoleAdmin).Allowed = false, want true")
	}
	d := Gate(viewer, RoleAdmin)
	if d.Allowed {
		t.Error("Gate(viewer, RoleAdmin).Allowed = true, want false")
	}
	if d.MissingRole != RoleAdmin {
		t.Errorf("Gate(viewer, RoleAdmin).MissingRole = %q, want %q", d.MissingRole, RoleAdmin)
	}
}

// A mutating control must never be offered against historical ("as of")
// state, even when the caller genuinely holds the role -- FR-8.
func TestGateCellAction_AsOfActive_DeniesRegardlessOfRole(t *testing.T) {
	promoter := &htmxauth.UserInfo{Roles: []string{EnvironmentPromoterRole("dev")}}
	d := GateCellAction(promoter, EnvironmentPromoterRole("dev"), true)
	if d.Allowed {
		t.Error("GateCellAction with asOfActive=true must deny even a role-holding caller")
	}
	if d.Reason == "" {
		t.Error("GateCellAction with asOfActive=true must set a non-role Reason, not the default MissingRole message")
	}
	if strings.Contains(d.Reason, "app-registry-promoter") {
		t.Errorf("as-of denial reason must not read as a missing-role message; got %q", d.Reason)
	}
}

func TestGateCellAction_AsOfInactive_DelegatesToRoleCheck(t *testing.T) {
	promoter := &htmxauth.UserInfo{Roles: []string{EnvironmentPromoterRole("dev")}}
	d := GateCellAction(promoter, EnvironmentPromoterRole("dev"), false)
	if !d.Allowed {
		t.Error("GateCellAction with asOfActive=false and a held role must allow")
	}

	viewer := &htmxauth.UserInfo{Roles: []string{}}
	d2 := GateCellAction(viewer, EnvironmentPromoterRole("dev"), false)
	if d2.Allowed {
		t.Error("GateCellAction with asOfActive=false must still deny a caller lacking the role")
	}
	if d2.MissingRole != EnvironmentPromoterRole("dev") {
		t.Errorf("GateCellAction denial MissingRole = %q, want %q", d2.MissingRole, EnvironmentPromoterRole("dev"))
	}
}

// --- RolesMisconfigured (FR-59 absent-vs-empty) -----------------------------

func TestRolesMisconfigured(t *testing.T) {
	cases := []struct {
		name string
		user *htmxauth.UserInfo
		want bool
	}{
		{"nil user", nil, false},
		{"roles claim absent (nil Roles)", &htmxauth.UserInfo{Roles: nil}, true},
		{"roles claim present but empty (read-only viewer)", &htmxauth.UserInfo{Roles: []string{}}, false},
		{"roles claim present and non-empty", &htmxauth.UserInfo{Roles: []string{RoleAdmin}}, false},
		{"AUTH_MODE=none sentinel", &htmxauth.UserInfo{Roles: htmxauth.AllRoles}, false},
	}
	for _, c := range cases {
		if got := RolesMisconfigured(c.user); got != c.want {
			t.Errorf("%s: RolesMisconfigured = %v, want %v", c.name, got, c.want)
		}
	}
}
