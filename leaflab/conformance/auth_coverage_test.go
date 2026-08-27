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

	allowlist := map[string]bool{}
	for _, name := range enumerateAnonymousAllowlist(t, authGoSrc) {
		allowlist[name] = true
	}
	wired := authInterceptorWired(mainGoSrc)

	for _, rpc := range rpcs {
		if allowlist[rpc] {
			continue
		}
		if !wired {
			t.Errorf("RPC %s is not in leaflab/api/auth.go's anonymousMethods allowlist, and "+
				"leaflab/api/main.go does not wire the authenticating interceptor "+
				"(NewAuthEnforcementUnaryInterceptor/NewAuthEnforcementStreamInterceptor) into the "+
				"server -- either wire the interceptor into buildServer's ChainUnaryInterceptor/"+
				"ChainStreamInterceptor, or add %s to anonymousMethods if it is truly meant to be "+
				"anonymous", rpc, rpc)
		}
	}
}

// TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry is NFR1.a's guard
// on leaflab/api/auth.go's anonymousMethods allowlist: it must contain
// exactly one entry (GetHealth, FR63's health signal). A second entry
// fails this test.
func TestAuthCoverage_AnonymousAllowlistHasExactlyOneEntry(t *testing.T) {
	authGoSrc := mustReadFile(t, "api/auth.go")

	entries := enumerateAnonymousAllowlist(t, authGoSrc)
	if len(entries) != 1 {
		t.Errorf("leaflab/api/auth.go's anonymousMethods allowlist has %d entries (%v); it must "+
			"contain exactly one (GetHealth, FR63's health signal) -- every other RPC must require "+
			"an authenticated principal, not be added here", len(entries), entries)
		return
	}
	if entries[0] != "GetHealth" {
		t.Errorf("leaflab/api/auth.go's anonymousMethods allowlist's sole entry is %q, want "+
			"\"GetHealth\" -- GetHealth is the only RPC FR63.2 permits to be reachable without an "+
			"authenticated principal", entries[0])
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

	// Explicit, enumerated public set (NFR1.a): login/logout (the sign-in
	// flow can't require a session to reach it), the OIDC callback,
	// static assets (favicon), and the health probe (FR63, mirrors the
	// API's own anonymous GetHealth). Adding a route here is itself a
	// decision that must be visible in review -- it is not derived from
	// any pattern in main.go.
	publicRoutes := map[string]bool{
		"/favicon.ico":   true,
		"/health":        true,
		"/auth/login":    true,
		"/auth/callback": true,
		"/auth/logout":   true,
	}

	for _, r := range routes {
		if r.requiresAuth {
			continue
		}
		if publicRoutes[r.pattern] {
			continue
		}
		t.Errorf("BFF route %q is registered without app.auth.RequireAuthFunc and is not in the "+
			"enumerated public route set (login/logout, callback, static assets, health) -- wrap "+
			"it in app.auth.RequireAuthFunc(...), or add it to this test's publicRoutes set if it "+
			"is truly meant to be reachable without a session", r.pattern)
	}
}
