package audit

// Actions naming the three FR8-named operations whose audit record must
// carry a reason (FR10 elevation, FR48 multi-board push, FR77 transfer).
// FR10's Elevate RPC (leaflab/api/server.go) is wired to NewElevationEntry
// below; MultiBoardPush and Transfer have no RPC surface yet and land in a
// later Phase 2/4 task. The constructors exist now so that when a reasoned
// action's RPC lands, building its audit Entry goes through a function
// that cannot compile without a reason, rather than through the
// general-purpose Entry literal where Reason is an easily-forgotten
// *string.
const (
	ActionElevate        = "Elevate"
	ActionMultiBoardPush = "MultiBoardPush"
	ActionTransfer       = "Transfer"
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
