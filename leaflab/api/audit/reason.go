package audit

import "fmt"

// Actions naming the FR8-named operations whose audit record must carry a
// reason (FR10 elevation, FR48 multi-board push, FR77 transfer, FR40
// rollback). The constructors below exist so that building one of these
// Entry values goes through a function that cannot compile without a
// reason, rather than through the general-purpose Entry literal where
// Reason is an easily-forgotten *string.
const (
	ActionElevate        = "Elevate"
	ActionMultiBoardPush = "MultiBoardPush"
	ActionTransfer       = "Transfer"
	// ActionRollback is FR40's "rollback writes forward" audit action --
	// see NewRollbackEntry.
	ActionRollback = "Rollback"
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

// NewRollbackEntry builds the audit Entry for an FR40 rollback. reason is a
// plain string, not *string: a rollback cannot be audited without one --
// unlike FR48 multi-board push, this is required regardless of how many
// boards the rollback targets.
//
// FR40/FR8 require the audit record to carry the reason, the source
// version and the new version. The new version is recorded the same way
// PushConfig's own audit entry is: the repository write path
// (InsertDeviceConfigNextVersion) fills in EntityID with the version it
// just assigned. Entry has only that one EntityID slot, so the source
// version (toVersion) is recorded here, in Reason itself -- the only other
// place this Entry carries free text -- rather than dropped.
func NewRollbackEntry(actorSubject string, actorKind ActorKind, targetHouseholdID *int64, toVersion uint64, reason string, correlationID string) Entry {
	fullReason := fmt.Sprintf("Rollback to version %d: %s", toVersion, reason)
	return Entry{
		ActorSubject:      actorSubject,
		ActorKind:         actorKind,
		TargetHouseholdID: targetHouseholdID,
		Action:            ActionRollback,
		EntityKind:        "device_config",
		Reason:            &fullReason,
		CorrelationID:     correlationID,
	}
}
