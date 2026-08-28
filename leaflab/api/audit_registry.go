package main

import (
	"fmt"

	"github.com/whale-net/everything/leaflab/api/audit"
)

// pushDeviceConfigFullMethod is PushDeviceConfig's full gRPC method name,
// matching the format grpc.UnaryServerInfo.FullMethod uses -- see auth.go's
// healthFullMethod.
const pushDeviceConfigFullMethod = "/leaflab.api.v1.LeafLabAPI/PushDeviceConfig"

// Admin RPC full method names (FR10, FR12 activation). resolveToHousehold
// is a read RPC (it writes no board/household/config row), but FR10.4
// requires it to write an audit row on every call regardless -- one row
// per call, at query granularity, not per returned board -- so it is
// listed here too, alongside the genuine writes.
const (
	resolveToHouseholdFullMethod = "/leaflab.api.v1.LeafLabAPI/ResolveToHousehold"
	elevateFullMethod            = "/leaflab.api.v1.LeafLabAPI/Elevate"
	renewElevationFullMethod     = "/leaflab.api.v1.LeafLabAPI/RenewElevation"
	endElevationFullMethod       = "/leaflab.api.v1.LeafLabAPI/EndElevation"
)

// declaredWriteMethods is FR8's "every write produces an audit record"
// registry for this service: every RPC that performs a write, plus
// ResolveToHousehold (see its doc comment above). A method listed here
// with no corresponding entry in auditRegistrations fails
// MustValidateAuditRegistrations at startup -- adding a write RPC without
// wiring its audit registration is structurally hard to ship, rather than
// a silently missing audit row discovered later in production.
//
// GetDeviceConfig, ListBoards, GetHealth and GetElevationStatus are reads
// with no FR8/FR10.4 audit requirement of their own and are deliberately
// absent. RetireBoard has no RPC surface yet (leaflab/api/repository.go's
// RetireBoard is called directly by tests only, per #1337's scaffold) --
// it will be added here in the task that gives it one.
//
// This registry is scoped to audit coverage only. #1351 (NFR1.b) adds a
// separate read/write-kind registry for authorization-conformance
// purposes over the same RPC set; the two check different things
// (audit-record presence vs. household-scope enforcement) and are
// independent, though worth a look for consolidation once #1351 lands.
var declaredWriteMethods = []string{
	pushDeviceConfigFullMethod,
	resolveToHouseholdFullMethod,
	elevateFullMethod,
	renewElevationFullMethod,
	endElevationFullMethod,
}

// auditRegistrations maps each declaredWriteMethods entry to the
// action/entity_kind its audit.Entry must carry (matched against
// audit.Entry.Action/EntityKind at the call site -- see server.go's
// PushDeviceConfig and the admin handlers).
var auditRegistrations = map[string]audit.Registration{
	pushDeviceConfigFullMethod:   {Action: "PushConfig", EntityKind: "device_config"},
	resolveToHouseholdFullMethod: {Action: "ResolveToHousehold", EntityKind: "admin_resolution"},
	elevateFullMethod:            {Action: audit.ActionElevate, EntityKind: "household"},
	renewElevationFullMethod:     {Action: "RenewElevation", EntityKind: "household"},
	endElevationFullMethod:       {Action: "EndElevation", EntityKind: "household"},
}

// MustValidateAuditRegistrations panics if any declaredWriteMethods entry
// has no corresponding auditRegistrations entry. Called once from run()
// at startup, alongside interceptor chain construction -- the runtime half
// of NFR1.b's build-failing conformance check (#1351 is the build-failing
// half).
func MustValidateAuditRegistrations() {
	if err := audit.ValidateRegistrations(declaredWriteMethods, auditRegistrations); err != nil {
		panic(fmt.Sprintf("leaflab-api: %v", err))
	}
}
