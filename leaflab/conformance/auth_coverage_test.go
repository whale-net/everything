package conformance

import (
	"fmt"
	"testing"
)

// bffPublicRoutes is NFR1.a's explicit, enumerated public BFF route set:
// login/logout (the sign-in flow can't require a session to reach it), the
// OIDC callback, static assets (favicon), and the health probe (FR63,
// mirrors the API's own anonymous GetHealth). Adding a route here is
// itself a decision that must be visible in review -- it is not derived
// from any pattern in main.go. Shared between
// TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic and
// negative_fixtures_test.go so the negative fixtures assert against the
// same public set the real check uses, not a copy that could drift.
var bffPublicRoutes = map[string]bool{
	"/favicon.ico":   true,
	"/health":        true,
	"/auth/login":    true,
	"/auth/callback": true,
	"/auth/logout":   true,
}

// rpcCoverageOffenses computes NFR1.a's RPC coverage failures: for every
// name in rpcs that is not in allowlist, when wired is false, returns the
// failure message TestAuthCoverage_EveryRPCIsAuthenticatedOrAllowlisted
// would report for it (in the order rpcs was given). Factored out of that
// test's loop so a synthetic fixture (negative_fixtures_test.go) can
// assert on violation messages directly, without needing to invoke
// testing.T.Errorf against its own *testing.T to observe what the real
// check would say.
func rpcCoverageOffenses(rpcs rpcNames, allowlist []string, wired bool) []string {
	allow := map[string]bool{}
	for _, name := range allowlist {
		allow[name] = true
	}
	var msgs []string
	for _, rpc := range rpcs {
		if allow[rpc] {
			continue
		}
		if !wired {
			msgs = append(msgs, fmt.Sprintf("RPC %s is not in leaflab/api/auth.go's anonymousMethods allowlist, and "+
				"leaflab/api/main.go does not wire the authenticating interceptor "+
				"(NewAuthEnforcementUnaryInterceptor/NewAuthEnforcementStreamInterceptor) into the "+
				"server -- either wire the interceptor into buildServer's ChainUnaryInterceptor/"+
				"ChainStreamInterceptor, or add %s to anonymousMethods if it is truly meant to be "+
				"anonymous", rpc, rpc))
		}
	}
	return msgs
}

// allowlistSizeOffense computes NFR1.a's anonymousMethods-allowlist-shape
// failure: the message
// TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry would report for
// entries, or "" if entries is exactly []string{"GetHealth"}. See
// rpcCoverageOffenses's doc comment for why this is factored out.
func allowlistSizeOffense(entries []string) string {
	if len(entries) != 1 {
		return fmt.Sprintf("leaflab/api/auth.go's anonymousMethods allowlist has %d entries (%v); it must "+
			"contain exactly one (GetHealth, FR63's health signal) -- every other RPC must require "+
			"an authenticated principal, not be added here", len(entries), entries)
	}
	if entries[0] != "GetHealth" {
		return fmt.Sprintf("leaflab/api/auth.go's anonymousMethods allowlist's sole entry is %q, want "+
			"\"GetHealth\" -- GetHealth is the only RPC FR63.2 permits to be reachable without an "+
			"authenticated principal", entries[0])
	}
	return ""
}

// bffRouteOffenses computes NFR1.a's BFF route coverage failures: for
// every route in routes that neither requires auth nor is in
// publicRoutes, returns the failure message
// TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic would report for
// it. See rpcCoverageOffenses's doc comment for why this is factored out.
func bffRouteOffenses(routes []bffRoute, publicRoutes map[string]bool) []string {
	var msgs []string
	for _, r := range routes {
		if r.requiresAuth {
			continue
		}
		if publicRoutes[r.pattern] {
			continue
		}
		msgs = append(msgs, fmt.Sprintf("BFF route %q is registered without app.auth.RequireAuthFunc and is not in the "+
			"enumerated public route set (login/logout, callback, static assets, health) -- wrap "+
			"it in app.auth.RequireAuthFunc(...), or add it to this test's publicRoutes set if it "+
			"is truly meant to be reachable without a session", r.pattern))
	}
	return msgs
}

// TestAuthCoverage_EveryRPCIsAuthenticatedOrAllowlisted is NFR1.a's RPC
// coverage check for the LeafLab API: every `rpc` declared on LeafLabAPI in
// leaflab/api/proto/api.proto must be either (a) in the server's anonymous
// allowlist -- which must contain exactly GetHealth (leaflab/api/auth.go's
// anonymousMethods) -- or (b) reached through the authenticating
// interceptor (leaflab/api/auth.go's NewAuthEnforcementUnaryInterceptor /
// NewAuthEnforcementStreamInterceptor, wired in leaflab/api/main.go). A
// newly added RPC with neither fails this test, and the failure message
// names the offending RPC.
func TestAuthCoverage_EveryRPCIsAuthenticatedOrAllowlisted(t *testing.T) {
	proto := mustReadFile(t, "api/proto/api.proto")
	apiFiles := globGoFiles(t, "api")

	authGoSrc, ok := apiFiles["api/auth.go"]
	if !ok {
		t.Fatal("api/auth.go not found -- check the api:conformance_srcs data dependency in BUILD.bazel")
	}
	mainGoSrc, ok := apiFiles["api/main.go"]
	if !ok {
		t.Fatal("api/main.go not found -- check the api:conformance_srcs data dependency in BUILD.bazel")
	}

	rpcs := enumerateRPCsFromProto(t, proto)
	if len(rpcs) == 0 {
		// Guards the guard: if the proto/data dependency silently stopped
		// resolving, every check below would vacuously pass.
		t.Fatal("found no RPCs in leaflab.api.v1.LeafLabAPI -- check the api/proto:proto_file data dependency in BUILD.bazel")
	}

	allowlist := enumerateAnonymousAllowlist(t, authGoSrc)
	wired := authInterceptorWired(mainGoSrc)

	for _, msg := range rpcCoverageOffenses(rpcs, allowlist, wired) {
		t.Error(msg)
	}
}

// TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry is NFR1.a's guard
// on leaflab/api/auth.go's anonymousMethods allowlist: it must contain
// exactly one entry (GetHealth, FR63's health signal). A second entry
// fails this test.
func TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry(t *testing.T) {
	authGoSrc := mustReadFile(t, "api/auth.go")

	entries := enumerateAnonymousAllowlist(t, authGoSrc)
	if msg := allowlistSizeOffense(entries); msg != "" {
		t.Error(msg)
	}
}

// TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic is NFR1.a's BFF
// route coverage check -- the half of NFR1.a that names the browser-
// reachable surface, not only the RPCs: every route registered in
// leaflab/ui/main.go's setupRoutes must be wrapped in
// app.auth.RequireAuthFunc except an explicit, enumerated public set
// (login/logout, callback, static assets, health). A newly added route
// outside that set and not wrapped in RequireAuthFunc fails this test, and
// the failure message names the offending route.
func TestAuthCoverage_EveryBFFRouteIsAuthenticatedOrPublic(t *testing.T) {
	mainGoSrc := mustReadFile(t, "ui/main.go")

	routes := enumerateBFFRoutes(t, mainGoSrc)
	if len(routes) == 0 {
		// Guards the guard: if the data dependency silently stopped
		// resolving, this check would vacuously pass.
		t.Fatal("found no mux.HandleFunc registrations in leaflab/ui/main.go -- check the ui:conformance_srcs data dependency in BUILD.bazel")
	}

	for _, msg := range bffRouteOffenses(routes, bffPublicRoutes) {
		t.Error(msg)
	}
}
