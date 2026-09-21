// This file (issue #2873, root plan #2851's M5, FR7, NFR5) is the
// work-axis cancel surface: a Swarm Operator moves any task -- escalated
// or not -- into a dead-lettered terminal state that ClaimTask never again
// returns (task_claimable_idx's own predicate, 016_escalation_axis.up.sql)
// and that a later requeue (FR6, #2876) cannot reopen. Distinct from lane
// Done (M4 FR8): a cancelled task's current_lane is left exactly where it
// was, never routed anywhere.
//
// CancelTask is the first production write path that ever sets
// task.cancelled_at -- task_escalation.go's ErrTaskCancelled and
// task_claim.go's ClaimTask check for it, and 016_escalation_axis.up.sql's
// task_claimable_idx already excludes it, but until this file lands no row
// ever has it set, so neither of those checks has ever actually fired
// against a real row.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrTaskAlreadyCancelled is CancelTask's named rejection when the task's
// cancelled_at is already set -- a clear error, not a silent no-op:
// cancelling an already-cancelled task writes nothing and reports why.
var ErrTaskAlreadyCancelled = errors.New("krill/store: task is already cancelled")

// CancelTaskParams is CancelTask's input (FR7): the task to cancel, an
// optional free-text rationale (task_intervention_event.reason, distinct
// from task_escalation_event.reason's fixed vocabulary), and the LB4
// subject pair the resulting task_intervention_event row records.
type CancelTaskParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	Reason     *string
	Acting     Subject
	OnBehalfOf Subject
}

// CancelResult is CancelTask's return value: the task cancelled, the
// moment it was marked terminal, the intervention event this call
// appended, and whether an open claim was force-closed along the way.
type CancelResult struct {
	TaskID            uuid.UUID
	CancelledAt       time.Time
	InterventionEvent InterventionEvent
	ClaimForceClosed  bool
}

// CancelTask is TaskStore.CancelTask (FR7): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, the same lock ClaimTask/
// CompleteTask/AbandonClaim take), refuses an already-cancelled task with
// ErrTaskAlreadyCancelled and nothing written, force-closes any open claim
// via forceCloseClaimTx (release_reason='cancel', task_escalation.go) so a
// still-heartbeating claimant is rejected exactly the way a reclaim
// already rejects one, sets task.cancelled_at, and appends one
// task_intervention_event row (action='cancel', escalation_event_id=nil --
// only a requeue names one, task_escalation.go's own InterventionEvent doc
// comment).
//
// current_lane is never touched (a cancelled task is not "in" any lane --
// 016_escalation_axis.up.sql's own comment on the cancelled_at column).
// current_escalation_id is left exactly as it was: cancelling an escalated
// task (FR7 is explicit this must succeed) leaves the escalation's own
// history as the record it always was; requeue (FR6, #2876) is the verb
// responsible for refusing a cancelled task regardless of that column's
// value. Nothing else is rewritten (NFR5): task_attempt, verdict, and
// task_note rows are untouched by this call.
func (s taskStore) CancelTask(ctx context.Context, params CancelTaskParams) (CancelResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return CancelResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cancelledAt *time.Time
	var currentClaimID *uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT cancelled_at, current_claim_id FROM task WHERE id = $1 AND scope_id = $2 FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&cancelledAt, &currentClaimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return CancelResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return CancelResult{}, fmt.Errorf("lock task: %w", err)
	}
	if cancelledAt != nil {
		return CancelResult{}, fmt.Errorf("%w: task id %s", ErrTaskAlreadyCancelled, params.TaskID)
	}

	// forceCloseClaimTx re-acquires the same row lock (a no-op on the same
	// connection, task_escalation.go's own doc comment) and is a no-op,
	// not an error, when the task has no open claim.
	if err := forceCloseClaimTx(ctx, tx, params.TaskID, "cancel"); err != nil {
		return CancelResult{}, fmt.Errorf("force close claim: %w", err)
	}

	var newCancelledAt time.Time
	if err := tx.QueryRow(ctx, `
		UPDATE task SET cancelled_at = NOW() WHERE id = $1 RETURNING cancelled_at
	`, params.TaskID).Scan(&newCancelledAt); err != nil {
		return CancelResult{}, fmt.Errorf("set task cancelled_at: %w", err)
	}

	event, err := scanInterventionEvent(tx.QueryRow(ctx, `
		INSERT INTO task_intervention_event (
			scope_id, task_id, action, escalation_event_id, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NULL, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+interventionEventColumns,
		params.ScopeID, params.TaskID, string(InterventionActionCancel), params.Reason,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return CancelResult{}, fmt.Errorf("insert task_intervention_event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return CancelResult{}, fmt.Errorf("commit: %w", err)
	}

	return CancelResult{
		TaskID:            params.TaskID,
		CancelledAt:       newCancelledAt,
		InterventionEvent: event,
		ClaimForceClosed:  currentClaimID != nil,
	}, nil
}
