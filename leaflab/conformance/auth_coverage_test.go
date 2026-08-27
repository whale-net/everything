package conformance

import "testing"

// TestAuthCoverage_EveryRPCIsAuthenticatedOrAllowlisted is NFR1.a's RPC
// coverage check for the LeafLab API: every `rpc` declared on LeafLabAPI in
// leaflab/api/proto/api.proto must be either (a) in the server's anonymous
// allowlist -- which must contain exactly GetHealth (leaflab/api/auth.go's
// anonymousMethods) -- or (b) reached through the authenticating
// interceptor (leaflab/api/auth.go's NewAuthEnforcementUnaryInterceptor /
// NewAuthEnforcementStreamInterceptor, wired in leaflab/api/main.go). A
// newly added RPC with neither fails this test, and the failure message
// names the offending RPC.
//
// TODO(Implementation phase): enumerateRPCsFromProto(api.proto) against
// leaflab/api's anonymousMethods allowlist and interceptor wiring
// (globGoFiles(t, "api")'s auth.go/main.go).
func TestAuthCoverage_EveryRPCIsAuthenticatedOrAllowlisted(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.a RPC coverage): parse api.proto's rpc list against leaflab/api/auth.go's anonymousMethods allowlist and interceptor wiring; fail naming any RPC in neither.")
}

// TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry is NFR1.a's guard
// on leaflab/api/auth.go's anonymousMethods allowlist: it must contain
// exactly one entry (GetHealth, FR63's health signal). A second entry
// fails this test.
//
// TODO(Implementation phase): parse leaflab/api/auth.go's anonymousMethods
// map literal (or count healthFullMethod-style const-keyed entries) and
// assert len == 1.
func TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.a): parse leaflab/api/auth.go's anonymousMethods map literal and assert it has exactly one entry (GetHealth).")
}

// TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic is NFR1.a's BFF
// route coverage check -- the half of NFR1.a that names the browser-
// reachable surface, not only the RPCs: every route registered in
// leaflab/ui/main.go's setupRoutes must be wrapped in
// app.auth.RequireAuthFunc except an explicit, enumerated public set
// (login, callback, static assets, health). A newly added route outside
// that set and not wrapped in RequireAuthFunc fails this test, and the
// failure message names the offending route.
//
// TODO(Implementation phase): enumerateBFFRoutes(ui/main.go) against the
// enumerated public-route set.
func TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.a BFF route coverage): parse leaflab/ui/main.go's setupRoutes mux.HandleFunc registrations; every route must be wrapped in app.auth.RequireAuthFunc except the enumerated public set (login, callback, static assets, health).")
}
