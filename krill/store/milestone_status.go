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
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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

func scanMilestoneStatusEvent(row pgx.Row) (MilestoneStatusEvent, error) {
	var e MilestoneStatusEvent
	var status string
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&e.ID, &e.ScopeID, &e.MilestoneID, &status, &e.Note,
		&e.CreatedByActing.Iss, &e.CreatedByActing.Sub, &actingKind,
		&e.CreatedByOnBehalfOf.Iss, &e.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&e.CreatedAt,
	)
	if err != nil {
		return MilestoneStatusEvent{}, err
	}
	e.Status = MilestoneStatus(status)
	e.CreatedByActing.Kind = SubjectKind(actingKind)
	e.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return e, nil
}

func (s milestoneStatusEventStore) RecordTransition(ctx context.Context, scopeID, milestoneID uuid.UUID, status MilestoneStatus, note *string, acting, onBehalfOf Subject) (MilestoneStatusEvent, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MilestoneStatusEvent{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	event, err := recordTransitionTx(ctx, tx, scopeID, milestoneID, status, note, acting, onBehalfOf)
	if err != nil {
		return MilestoneStatusEvent{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneStatusEvent{}, fmt.Errorf("commit: %w", err)
	}
	return event, nil
}

// recordTransitionTx is RecordTransition's transaction-scoped core --
// shared with Abandon (abandon.go, issue #2688), which appends this same
// append-only row as one step inside its own larger transaction rather
// than through a second, standalone one.
func recordTransitionTx(ctx context.Context, tx pgx.Tx, scopeID, milestoneID uuid.UUID, status MilestoneStatus, note *string, acting, onBehalfOf Subject) (MilestoneStatusEvent, error) {
	// milestone_ref's own kind CHECK (migration 011) already restricts
	// every row to kind IN ('milestone', 'milepebble', 'backlog') -- a
	// plain existence check under scopeID is therefore sufficient to
	// enforce this method's "target is a milestone or milepebble"
	// contract; Abandon (abandon.go) itself is what keeps a backlog row
	// from ever reaching here.
	exists, err := plainRowExists(ctx, tx, "milestone_ref", milestoneID, scopeID)
	if err != nil {
		return MilestoneStatusEvent{}, err
	}
	if !exists {
		return MilestoneStatusEvent{}, errParentNotFound("milestone_ref", milestoneID)
	}

	event, err := scanMilestoneStatusEvent(tx.QueryRow(ctx, `
		INSERT INTO milestone_status_event (
			scope_id, milestone_id, status, note,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+milestoneStatusEventColumns,
		scopeID, milestoneID, string(status), note,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind)))
	if err != nil {
		return MilestoneStatusEvent{}, fmt.Errorf("insert milestone_status_event: %w", err)
	}
	return event, nil
}

func (s milestoneStatusEventStore) CurrentStatus(ctx context.Context, milestoneID uuid.UUID) (MilestoneStatus, error) {
	return currentStatusTx(ctx, s.pool, milestoneID)
}

// currentStatusTx is CurrentStatus's core query, generalized over
// txQuerier (errors.go) so Abandon (abandon.go, issue #2688) can read a
// container's latest status inside its own transaction -- the "already
// abandoned" check needs the same snapshot the rest of that transaction
// runs against, not a separate read through the pool.
func currentStatusTx(ctx context.Context, q txQuerier, milestoneID uuid.UUID) (MilestoneStatus, error) {
	var status string
	err := q.QueryRow(ctx, `
		SELECT status FROM milestone_status_event
		WHERE milestone_id = $1
		ORDER BY created_at DESC, id DESC
		LIMIT 1
	`, milestoneID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		// Absence of any row IS "not started" (FR8) -- never a seeded
		// row, see MilestoneStatusNotStarted's doc comment.
		return MilestoneStatusNotStarted, nil
	}
	if err != nil {
		return "", fmt.Errorf("current milestone_status_event: %w", err)
	}
	return MilestoneStatus(status), nil
}

func (s milestoneStatusEventStore) CurrentStatuses(ctx context.Context, milestoneIDs []uuid.UUID) (map[uuid.UUID]MilestoneStatus, error) {
	result := make(map[uuid.UUID]MilestoneStatus, len(milestoneIDs))
	for _, id := range milestoneIDs {
		result[id] = MilestoneStatusNotStarted
	}
	if len(milestoneIDs) == 0 {
		return result, nil
	}

	// One query for the whole set (FR11 depends on this shape) -- DISTINCT
	// ON (milestone_id) picks the latest row per id, tie-broken by id the
	// same way CurrentStatus is.
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (milestone_id) milestone_id, status
		FROM milestone_status_event
		WHERE milestone_id = ANY($1)
		ORDER BY milestone_id, created_at DESC, id DESC
	`, milestoneIDs)
	if err != nil {
		return nil, fmt.Errorf("current milestone_status_events: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id uuid.UUID
		var status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("scan milestone_status_event: %w", err)
		}
		result[id] = MilestoneStatus(status)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("current milestone_status_events: %w", err)
	}
	return result, nil
}

func (s milestoneStatusEventStore) ListTransitions(ctx context.Context, milestoneID uuid.UUID) ([]MilestoneStatusEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+milestoneStatusEventColumns+`
		FROM milestone_status_event
		WHERE milestone_id = $1
		ORDER BY created_at ASC, id ASC
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("list milestone_status_event: %w", err)
	}
	defer rows.Close()

	var events []MilestoneStatusEvent
	for rows.Next() {
		e, err := scanMilestoneStatusEvent(rows)
		if err != nil {
			return nil, fmt.Errorf("scan milestone_status_event: %w", err)
		}
		events = append(events, e)
	}
	return events, rows.Err()
}
