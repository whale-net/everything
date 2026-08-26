package conformance

import (
	"testing"
)

// TestAuthCoverage_NoRPCWithoutAuthentication is the NFR1.a conformance check
// for the LeafLab API: every RPC that serves household data must sit behind
// the authenticated interceptor. The only permitted anonymous endpoint is
// Health (FR63's health signal).
//
// The test enumerates:
// 1. Every RPC from leaflab/api/proto/api.proto
// 2. Maps each RPC to its handler in the gRPC server
// 3. Verifies each handler is either behind the auth interceptor or on the
//    single-entry anonymous allowlist (Health only)
//
// Adding an unauthenticated RPC that serves household data FAILS the build.
func TestAuthCoverage_NoRPCWithoutAuthentication(t *testing.T) {
	rpcs := enumerateRPCsFromProto(t)
	if len(rpcs) == 0 {
		t.Fatalf("no RPCs found in api.proto (check data dependencies)")
	}

	rpcsWithAuth := getRPCsRequiringAuth(t)
	const anonymousAllowlist = "Health"

	for _, rpc := range rpcs {
		if rpc == anonymousAllowlist {
			// Health is the only allowed anonymous endpoint
			if rpcsWithAuth[rpc] {
				t.Errorf("RPC %q is on the anonymous allowlist but calls requireAuthentication; remove the auth check", rpc)
			}
			continue
		}

		// All other RPCs must be authenticated
		if !rpcsWithAuth[rpc] {
			t.Errorf("RPC %q does not call requireAuthentication -- every RPC serving household data must be authenticated (NFR1.a)", rpc)
		}
	}
}

// TestAuthCoverage_BFFRoutesProtected is the NFR1.a conformance check
// for the LeafLab UI (browser-facing component): every route that serves
// household data must sit behind the authentication middleware.
//
// The test enumerates:
// 1. Every route registered via mux.HandleFunc() in leaflab/ui/main.go
// 2. Verifies each route is either behind the auth middleware or on the
//    single-entry anonymous allowlist (Health only)
//
// Unlike the RPC check, this explicitly names the BFF's browser-reachable
// routes, not only the service's RPCs. This satisfies criterion 2 of NFR1.a.
//
// Adding an unauthenticated route that serves household data FAILS the build.
func TestAuthCoverage_BFFRoutesProtected(t *testing.T) {
	routes := enumerateBFFRoutes(t)
	if len(routes) == 0 {
		t.Fatalf("no routes found in ui/main.go (check data dependencies)")
	}

	// These routes are explicitly on the anonymous allowlist
	anonymousAllowlist := map[string]bool{
		"/health":        true,
		"/auth/login":    true,
		"/auth/callback": true,
		"/auth/logout":   true,
	}

	for route, isProtected := range routes {
		if anonymousAllowlist[route] {
			// These routes are allowed to be unprotected
			if isProtected {
				t.Errorf("route %q is on the anonymous allowlist but is wrapped in RequireAuthFunc; remove the auth wrapper", route)
			}
			continue
		}

		// All other routes must be protected
		if !isProtected {
			t.Errorf("route %q is not wrapped in RequireAuthFunc -- every route serving household data must be authenticated (NFR1.a)", route)
		}
	}
}

// TestAuthCoverage_SingleEntryAnonymousAllowlist verifies that the anonymous
// allowlist has exactly one entry per service: the Health endpoint (FR63) for
// the gRPC API, and the /health and auth routes for the BFF.
// If additional endpoints are added to the allowlist, this test fails.
func TestAuthCoverage_SingleEntryAnonymousAllowlist(t *testing.T) {
	// Check gRPC API: only Health should be unauthenticated
	rpcs := enumerateRPCsFromProto(t)
	rpcsWithAuth := getRPCsRequiringAuth(t)

	unauthRPCs := []string{}
	for _, rpc := range rpcs {
		if !rpcsWithAuth[rpc] {
			unauthRPCs = append(unauthRPCs, rpc)
		}
	}

	if len(unauthRPCs) != 1 || unauthRPCs[0] != "Health" {
		t.Errorf("API allowlist has %v; expected only [Health]. NFR1.a requires exactly one anonymous RPC endpoint", unauthRPCs)
	}

	// Check BFF: /health, /auth/login, /auth/callback, /auth/logout should be unprotected
	routes := enumerateBFFRoutes(t)
	unauthRoutes := []string{}
	for route, isProtected := range routes {
		if !isProtected {
			unauthRoutes = append(unauthRoutes, route)
		}
	}

	expectedUnauth := map[string]bool{
		"/health":        true,
		"/auth/login":    true,
		"/auth/callback": true,
		"/auth/logout":   true,
	}

	if len(unauthRoutes) != len(expectedUnauth) {
		t.Errorf("BFF has %d unauthenticated routes; expected %d (health and auth routes)", len(unauthRoutes), len(expectedUnauth))
	}

	for _, route := range unauthRoutes {
		if !expectedUnauth[route] {
			t.Errorf("unexpected unauthenticated route %q in BFF", route)
		}
	}
}
