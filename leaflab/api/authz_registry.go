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

// rpcAuthzRegistration is one LeafLabAPI RPC's NFR1.b classification: its
// kind, and -- for a write RPC -- every entity kind its write path may set
// a live reference to.
type rpcAuthzRegistration struct {
	// Kind is this RPC's read/write classification.
	Kind rpcKind
	// ForeignRefFields lists the authz.EntityKind values this RPC's write
	// path may set a live reference to (a request field named
	// region_id/plant_id/board_id/household_id per the issue text) --
	// empty for read RPCs, and for write RPCs that set no cross-entity
	// reference. Each entry here is a claim that a corresponding
	// authz.AssertSameHousehold call site exists in this package's
	// source for that entity kind; leaflab/conformance's foreign-FK
	// assertion checks that claim against the real source, not this
	// registry alone -- a registry entry with no matching call site, or a
	// call site with no matching registry entry, both fail the build.
	ForeignRefFields []authz.EntityKind
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
// for a newly added RPC with none, and for any RPC whose entry claims read
// or write coverage the real test suite in leaflab/api doesn't actually
// carry (see that test's doc comment for exactly what "coverage" means per
// kind).
var rpcAuthzRegistrations = map[string]rpcAuthzRegistration{
	"PushDeviceConfig": {
		Kind: rpcKindWrite,
		// Every sensors[i].region_id named in the payload is a live
		// reference validatePushRegions resolves through
		// authz.AssertSameHousehold before PushDeviceConfig stores or
		// publishes anything (FR1.2/FR1.3) -- see server.go's
		// validatePushRegions.
		ForeignRefFields: []authz.EntityKind{authz.EntityRegion},
	},
	"GetDeviceConfig": {Kind: rpcKindRead},
	"ListBoards":      {Kind: rpcKindRead},
}
