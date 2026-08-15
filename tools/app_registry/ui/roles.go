package main

import "github.com/whale-net/everything/libs/go/htmxauth"

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
