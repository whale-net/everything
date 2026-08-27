package conformance

import "testing"

// This file is NFR1.b's fail-closed household-scope conformance check:
// "an endpoint with no explicit household-scope check denies, and no write
// path may set a foreign-household foreign key. Every RPC is covered by an
// authorization test asserting that a non-member is refused and that a
// foreign-household reference is rejected (FR1.2); adding an RPC without
// one fails the build." It extends this package's conformance_test target
// (see paths_test.go's package doc comment and this package's BUILD.bazel)
// rather than forking a second one, per the issue's instruction.
//
// It reads leaflab/api's rpcAuthzRegistrations (authz_registry.go) --
// the registry the API declares, per RPC, of its read/write kind and, for
// a write RPC, the authz.EntityKind values its write path may set a live
// reference to -- as the source-of-truth for what each RPC is claimed to
// do, and cross-checks that claim against the real source (test function
// names/bodies in leaflab/api, and server.go's handler/AssertSameHousehold
// call sites) exactly as auth_coverage_test.go does for NFR1.a's allowlist
// claim, mirroring tools/app_registry/conformance/'s source-analysis
// pattern.

// TestNFR1b_EveryRPCHasRegistryCoverage is NFR1.b's first-line coverage
// assertion: every RPC declared in leaflab/api/proto/api.proto's
// LeafLabAPI service, other than the FR63/NFR1.a anonymous allowlist
// (GetHealth), must have an entry in leaflab/api/authz_registry.go's
// rpcAuthzRegistrations. A newly added RPC with no entry fails the build,
// naming the offending RPC.
//
// TODO(Implementation phase): parse leaflab/api/authz_registry.go's
// rpcAuthzRegistrations map literal (mirroring
// enumerateAnonymousAllowlist's approach to auth.go's anonymousMethods)
// and diff its key set against enumerateRPCsFromProto(api.proto) minus the
// anonymous allowlist.
func TestNFR1b_EveryRPCHasRegistryCoverage(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.b registry coverage): every RPC in api.proto other than GetHealth must have an entry in leaflab/api/authz_registry.go's rpcAuthzRegistrations; fail naming any RPC with none.")
}

// TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests is NFR1.b's
// coverage assertion proper: for every RPC covered by
// TestNFR1b_EveryRPCHasRegistryCoverage, there must exist a test in
// leaflab/api asserting (a) a non-member (a caller outside the entity's
// household, or with no current household membership at all) is refused,
// and (b) a foreign-household reference is rejected (FR1.2) -- found by
// source analysis over leaflab/api's *_test.go function names, mirroring
// this package's other coverage checks. An RPC with neither test fails
// the build, naming the RPC and which of the two assertions is missing.
//
// TODO(Implementation phase): decide and document the test-name/body
// pattern that counts as "asserts a non-member is refused" and "asserts a
// foreign-household reference is rejected" per RPC kind (read vs. write --
// see rpcAuthzRegistration's doc comment), then scan
// globGoFiles(t, "api") for matches. Failure messages must name the RPC
// and the missing test, and say what to add (per the issue's
// Implementation section).
func TestNFR1b_EveryRPCHasNonMemberAndForeignReferenceTests(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.b coverage): for every registered RPC, find a leaflab/api test asserting a non-member is refused and a test asserting a foreign-household reference is rejected; fail naming the RPC, which assertion is missing, and what to add.")
}

// TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository is NFR1.b's
// fail-closed assertion: every LeafLabAPIServer RPC handler in server.go,
// other than the anonymous allowlist (GetHealth), must obtain an
// authz.Scope (or resolve/validate an entity through leaflab/api/authz/)
// before it calls anything on its deviceRepository -- a handler that
// queries the pool without first doing so fails.
//
// TODO(Implementation phase): go/ast walk of server.go's
// *LeafLabAPIServer method bodies (mirroring enumerateBFFRoutes'
// go/parser approach) -- for each method whose name matches a
// rpcAuthzRegistrations key, assert a call reaching into
// leaflab/api/authz/ (scopeForCaller, authorizeBoardAccess,
// authz.AssertSameHousehold, or a direct authzSvc.Resolve/ResolveInScope)
// occurs before the first s.repo.* call in that method's statement order;
// fail naming the RPC and the repository call reached with no prior
// authz call.
func TestNFR1b_EveryHandlerObtainsScopeBeforeTouchingRepository(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.b fail-closed): walk each RPC handler in leaflab/api/server.go and assert a leaflab/api/authz/ call (Scope resolution, ResolveInScope, or AssertSameHousehold) precedes its first repository call; fail naming the RPC and the repository call reached unguarded.")
}

// TestNFR1b_EveryForeignFKWriteAssertsSameHousehold is NFR1.b's foreign-FK
// assertion: every write path in leaflab/api that sets a region_id,
// plant_id, board_id or household_id -- as declared by that RPC's
// ForeignRefFields in rpcAuthzRegistrations -- must route through
// authz.AssertSameHousehold (FR1.2) before the write. A registry entry
// naming an entity kind with no corresponding AssertSameHousehold call
// site fails the build, as does an AssertSameHousehold call site for an
// entity kind the registry doesn't declare (the two must agree in both
// directions).
//
// TODO(Implementation phase): source-scan server.go (and any future write
// path file) for authz.AssertSameHousehold call sites and the
// authz.EntityKind values named in the authz.LiveRef{EntityRef: ...}
// literals passed to them, then diff that set, per RPC, against
// rpcAuthzRegistrations[rpc].ForeignRefFields in both directions; fail
// naming the RPC and the missing/extra entity kind.
func TestNFR1b_EveryForeignFKWriteAssertsSameHousehold(t *testing.T) {
	t.Skip("TODO(Implementation phase, NFR1.b foreign-FK): for every write RPC's ForeignRefFields in leaflab/api/authz_registry.go's rpcAuthzRegistrations, find a matching authz.AssertSameHousehold call site (by authz.EntityKind) in leaflab/api/server.go, and vice versa; fail naming the RPC and the entity kind with no match on either side.")
}
