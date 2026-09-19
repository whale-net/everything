// This file (issue #2685, FR8, FR9, FR12, C28) is
// MilestoneStatusEventStore -- the append-only status history register
// (migration 012) that gives every `milestone_ref` row, of either Kind, a
// status drawn from MilestoneStatus's fixed seven-value set. Kept as a
// sibling accessor ((*Store).MilestoneStatus()), mirroring
// MilestoneAuthoringStore's own relationship to MilestoneStore, so the
// existing milestone_ref/entity_milestone write paths stay untouched --
// this file is additive. See migration 012's LB3 boundary comment for why
// this table is append-only rather than SCD2, unlike the milestone_ref
// row it hangs off.
//
// Scaffold-stage: interface signatures and the compile-time interface
// assertion are in place; method bodies land in the Implementation phase.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MilestoneStatusEventStore covers `milestone_status_event` (migration
// 012, issue #2685). Every write here is an INSERT only -- NFR2/LB3
// forbid an update or delete path anywhere in this package, so this
// interface deliberately has no such method, and none may be added later
// without revisiting that constraint.
type MilestoneStatusEventStore interface {
	// RecordTransition appends one MilestoneStatusEvent row for
	// milestoneID (FR8, FR9) and records the LB4 subject pair. Validates
	// that milestoneID names a current `milestone_ref` row in scopeID
	// whose Kind is MilestoneKindMilestone or MilestoneKindMilepebble --
	// any other target (e.g. a nonexistent id) is rejected loudly, never
	// silently accepted. Re-recording the same status as the prior
	// transition still appends a new row: a re-affirmation is history,
	// not a no-op.
	RecordTransition(ctx context.Context, scopeID, milestoneID uuid.UUID, status MilestoneStatus, note *string, acting, onBehalfOf Subject) (MilestoneStatusEvent, error)

	// CurrentStatus returns milestoneID's latest MilestoneStatusEvent by
	// CreatedAt, tie-broken deterministically (e.g. by ID) so two
	// transitions landing in the same transaction-commit instant never
	// produce a nondeterministic answer. Returns MilestoneStatusNotStarted
	// when milestoneID has no MilestoneStatusEvent row at all -- "not
	// started" is derived from the absence of history, never a seeded row
	// (FR8).
	CurrentStatus(ctx context.Context, milestoneID uuid.UUID) (MilestoneStatus, error)

	// CurrentStatuses is CurrentStatus's set form (the FR11 listing
	// surface depends on this shape): one query for every id in
	// milestoneIDs, never one call per id. An id with no
	// MilestoneStatusEvent row is present in the result map with
	// MilestoneStatusNotStarted, mirroring CurrentStatus's own derivation.
	CurrentStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]MilestoneStatus, error)

	// ListTransitions returns every MilestoneStatusEvent row for
	// milestoneID in chronological order (oldest first) -- FR12's full
	// history read, each entry carrying its own subject pair and
	// timestamp.
	ListTransitions(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneStatusEvent, error)
}

// milestoneStatusEventStore is the pgx-backed MilestoneStatusEventStore
// implementation.
type milestoneStatusEventStore struct{ pool *pgxpool.Pool }

var _ MilestoneStatusEventStore = milestoneStatusEventStore{}

const milestoneStatusEventColumns = `id, scope_id, milestone_id, status, note, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func (s milestoneStatusEventStore) RecordTransition(ctx context.Context, scopeID, milestoneID uuid.UUID, status MilestoneStatus, note *string, acting, onBehalfOf Subject) (MilestoneStatusEvent, error) {
	return MilestoneStatusEvent{}, fmt.Errorf("RecordTransition: not implemented")
}

func (s milestoneStatusEventStore) CurrentStatus(ctx context.Context, milestoneID uuid.UUID) (MilestoneStatus, error) {
	return "", fmt.Errorf("CurrentStatus: not implemented")
}

func (s milestoneStatusEventStore) CurrentStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]MilestoneStatus, error) {
	return nil, fmt.Errorf("CurrentStatuses: not implemented")
}

func (s milestoneStatusEventStore) ListTransitions(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneStatusEvent, error) {
	return nil, fmt.Errorf("ListTransitions: not implemented")
}
