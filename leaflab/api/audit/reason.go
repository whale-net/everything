package audit

// Actions naming the three FR8-named operations whose audit record must
// carry a reason (FR10 elevation, FR48 multi-board push, FR77 transfer).
// None of these RPCs exist yet -- each lands in a later Phase 2/4 task --
// but the constructors below exist now so that when they do, building
// their audit Entry goes through a function that cannot compile without a
// reason, rather than through the general-purpose Entry literal where
// Reason is an easily-forgotten *string.
const (
	ActionElevate        = "Elevate"
	ActionMultiBoardPush = "MultiBoardPush"
	ActionTransfer       = "Transfer"
)

// ActionApplyConfigRegionSkip and EntityKindSensor name the FR8 audit
// action leaflab/processor's ApplyConfigRegions (repository.go) records
// each time it skips a config entry instead of applying it (FR1.3):
// household drift or a stale push, re-validated immediately before the
// write. Exported here -- rather than as a local constant in
// leaflab/processor, which writes these rows -- because leaflab/api's
// GetDeviceConfig (server.go) also reads them back by this same action
// name (FR1.3's caller-visible skip surface); a shared constant keeps the
// writer and reader from drifting out of agreement even though they are
// two separate binaries with no other shared code path for this.
const (
	ActionApplyConfigRegionSkip = "ApplyConfigRegionSkip"
	EntityKindSensor            = "sensor"
)

// NewElevationEntry builds the audit Entry for an FR10 admin elevation.
// reason is a plain string, not *string: elevation cannot be audited
// without one.
func NewElevationEntry(actorSubject string, actorKind ActorKind, targetHouseholdID *int64, reason string, correlationID string) Entry {
	return Entry{
		ActorSubject:      actorSubject,
		ActorKind:         actorKind,
		TargetHouseholdID: targetHouseholdID,
		Action:            ActionElevate,
		EntityKind:        "household",
		Reason:            &reason,
		CorrelationID:     correlationID,
	}
}

// NewMultiBoardPushEntry builds the audit Entry for an FR48 multi-board
// config push. reason is a plain string, not *string: a multi-board push
// cannot be audited without one.
func NewMultiBoardPushEntry(actorSubject string, actorKind ActorKind, targetHouseholdID *int64, entityID *string, reason string, correlationID string) Entry {
	return Entry{
		ActorSubject:      actorSubject,
		ActorKind:         actorKind,
		TargetHouseholdID: targetHouseholdID,
		Action:            ActionMultiBoardPush,
		EntityKind:        "device_config",
		EntityID:          entityID,
		Reason:            &reason,
		CorrelationID:     correlationID,
	}
}

// NewTransferEntry builds the audit Entry for an FR77 household-to-household
// transfer. reason is a plain string, not *string: a transfer cannot be
// audited without one.
func NewTransferEntry(actorSubject string, actorKind ActorKind, targetHouseholdID *int64, entityID *string, reason string, correlationID string) Entry {
	return Entry{
		ActorSubject:      actorSubject,
		ActorKind:         actorKind,
		TargetHouseholdID: targetHouseholdID,
		Action:            ActionTransfer,
		EntityKind:        "board",
		EntityID:          entityID,
		Reason:            &reason,
		CorrelationID:     correlationID,
	}
}
