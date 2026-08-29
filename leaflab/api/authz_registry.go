package main

import "github.com/whale-net/everything/leaflab/api/authz"

// rpcKind classifies an RPC for NFR1.b's fail-closed household-scope
// conformance check (leaflab/conformance/nfr1b_test.go): a read RPC only
// queries; a write RPC may set a live reference on an entity and must
// route every one it sets through authz.AssertSameHousehold (FR1.2).
type rpcKind string

const (
	rpcKindRead  rpcKind = "read"
	rpcKindWrite rpcKind = "write"
)

// rpcAuthzRegistration is one LeafLabAPI RPC's NFR1.b classification. It is
// parsed as source text by leaflab/conformance (leaflab/api is package
// main and cannot be imported as a library) -- keep every field a literal
// composite-literal value (string literals, bare package-local
// rpcKind consts, authz.EntityKind selector expressions), never a computed
// expression, so enumerateRPCAuthzRegistrations in nfr1b_test.go can parse
// it with go/ast.
type rpcAuthzRegistration struct {
	// Kind is this RPC's read/write classification.
	Kind rpcKind

	// ForeignRefFields lists the authz.EntityKind values this RPC's write
	// path may set a live reference to (a request field named
	// region_id/plant_id/board_id/household_id per the issue text) --
	// empty for read RPCs, and for write RPCs that set no cross-entity
	// reference. Each entry here is a claim that a corresponding
	// authz.AssertSameHousehold call site exists in this package's
	// source, reachable from this RPC's handler, for that entity kind;
	// leaflab/conformance's foreign-FK assertion checks that claim
	// against the real source in both directions -- a registry entry
	// with no matching call site, or a call site with no matching
	// registry entry, both fail the build.
	ForeignRefFields []authz.EntityKind

	// NonMemberTest names the leaflab/api test function (found by exact
	// name across this package's *_test.go sources) that asserts a
	// caller outside this RPC's target entity's household is refused.
	// Required for every registered RPC unless ScopeGapReason is also
	// set.
	NonMemberTest string

	// ForeignRefTest names the leaflab/api test function asserting a
	// foreign-household reference named in ForeignRefFields is rejected
	// (FR1.2). Required whenever ForeignRefFields is non-empty.
	ForeignRefTest string

	// ScopeGapReason, when non-empty, is this RPC's explicit, reviewed
	// exception from strict NFR1.b enforcement: it must name the tracked
	// issue that closes the gap (a "#<number>" reference), and its
	// presence is itself what exempts this RPC from requiring
	// NonMemberTest and from PreAuthzExemptRepoCalls's ordering check.
	// Adding an entry here is a decision that must be visible in
	// review -- exactly like leaflab/conformance's bffPublicRoutes/
	// anonymousMethods carve-outs -- never inferred, only declared.
	//
	// Today's sole use: PushDeviceConfig does not yet check the pushing
	// caller against the target board's household (#1403), pending
	// FR76's self-service claim RPC (#1342) -- see authorizeBoardAccess's
	// doc comment in server.go for the full reasoning.
	ScopeGapReason string

	// PreAuthzExemptRepoCalls names deviceRepository method names this
	// RPC's handler may call before its first leaflab/api/authz/ call,
	// per NFR1.b's fail-closed assertion. Empty for every RPC except one
	// whose ScopeGapReason documents why a specific repository call
	// (e.g. a self-registration upsert) has no authorization gate to put
	// ahead of it yet. A repository call reached with no prior authz
	// call, and not named here, fails the build.
	PreAuthzExemptRepoCalls []string
}

// rpcAuthzRegistrations is NFR1.b's read/write-kind registry: keyed by
// each RPC's short name as declared in leaflab/api/proto/api.proto's
// `service LeafLabAPI` block (e.g. "PushDeviceConfig") -- matching
// leaflab/conformance's enumerateRPCsFromProto/shortRPCName convention,
// not audit_registry.go's declaredWriteMethods/auditRegistrations above,
// which key on full gRPC method name for a different, independent concern
// (audit-record presence, not household-scope enforcement -- see that
// file's doc comment).
//
// Every RPC in LeafLabAPI other than the FR63/NFR1.a anonymous allowlist
// (GetHealth) must have an entry here:
// leaflab/conformance/nfr1b_test.go's coverage assertion fails the build
// for a newly added RPC with none, and for any RPC whose entry claims
// coverage the real test suite in leaflab/api doesn't actually carry (see
// that test's doc comment for exactly what "coverage" means per kind).
var rpcAuthzRegistrations = map[string]rpcAuthzRegistration{
	"PushDeviceConfig": {
		Kind: rpcKindWrite,
		// Every sensors[i].region_id named in the payload is a live
		// reference validatePushRegions resolves through
		// authz.AssertSameHousehold before PushDeviceConfig stores or
		// publishes anything (FR1.2/FR1.3) -- see server.go's
		// validatePushRegions.
		ForeignRefFields: []authz.EntityKind{authz.EntityRegion},
		ForeignRefTest:   "TestPushDeviceConfig_ForeignHouseholdRegion_Refused_WritesNothing",
		ScopeGapReason:   "PushDeviceConfig does not yet check the pushing caller against the target board's household (#1403), pending FR76's self-service claim RPC (#1342) -- see authorizeBoardAccess's doc comment.",
		// GetOrCreateBoard is the self-registration upsert #1403 covers:
		// a never-claimed board's first push creates its own board row
		// before any household can be resolved for it.
		PreAuthzExemptRepoCalls: []string{"GetOrCreateBoard"},
	},
	"GetDeviceConfig": {
		Kind:          rpcKindRead,
		NonMemberTest: "TestGetDeviceConfig_OutOfScopeBoard_SameFailureAsNonexistent_RealDB",
	},
	"ListBoards": {
		Kind:          rpcKindRead,
		NonMemberTest: "TestListBoards_ScopedToCallerHousehold_ExcludesOtherHouseholds",
	},
	"GetReadingSeries": {
		Kind:          rpcKindRead,
		NonMemberTest: "TestGetReadingSeries_NonMemberGetsNotFound_ForAnotherHouseholdsSensor",
	},
	"GetCurrentValues": {
		Kind:           rpcKindRead,
		ScopeGapReason: "No non-member-refusal test exists yet for this RPC -- see #1465.",
	},
	"GetPeriodSummary": {
		Kind:           rpcKindRead,
		ScopeGapReason: "No non-member-refusal test exists yet for this RPC -- see #1465.",
	},
	"CompareSeries": {
		Kind:           rpcKindRead,
		ScopeGapReason: "No non-member-refusal test exists yet for this RPC -- see #1465.",
	},
}
