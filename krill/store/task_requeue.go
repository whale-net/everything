// This file (issue #2876, root plan #2851's M5, FR6) is the work-axis
// requeue surface: a Swarm Operator returns an escalated task to
// claimable, resetting exactly the counter whose cap triggered the
// escalation being resolved -- the "recover" half of the recover-or-
// terminate pair task_cancel.go's CancelTask is the other half of.
//
// Correctness here is defined per EscalationReason (task_escalation.go):
// a thrash-cap escalation resets task.thrash_count, an attempt-cap
// escalation resets task.attempt_count, and a manual escalation resets
// neither UNLESS EscalateTask's own force-close (task_escalate.go, FR9)
// left task.attempt_count at or past DefaultAttemptCap -- FR9's stated
// exception, so a manual escalation can never strand a task as
// requeued-but-unclaimable (NFR4, NFR5). The task_escalation_event row
// itself is never rewritten or deleted (NFR2, NFR5) -- resolution is
// recorded exclusively by the task_intervention_event this call appends,
// which names the escalation it resolved.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResetCounter names which of task's two independent counters (NFR4)
// RequeueTask reset while resolving an escalation -- ResetCounterNone
// when neither was (a manual escalation below DefaultAttemptCap, FR6's
// documented below-cap behaviour).
type ResetCounter string

const (
	ResetCounterThrash  ResetCounter = "thrash_count"
	ResetCounterAttempt ResetCounter = "attempt_count"
	ResetCounterNone    ResetCounter = "none"
)

// RequeueParams is RequeueTask's input (FR6): the task to requeue, an
// optional free-text rationale (task_intervention_event.reason, distinct
// from task_escalation_event.reason's fixed vocabulary), and the LB4
// subject pair -- always the operator -- recorded on the resulting
// task_intervention_event row.
type RequeueParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	Reason     *string
	Acting     Subject
	OnBehalfOf Subject
}

// RequeueResult is RequeueTask's return value: the escalation this call
// resolved, which counter (if any) it reset, the lane the task is now
// claimable at (task_escalation_event.lane_at_escalation, never re-derived
// from task.current_lane -- see this file's doc comment), and the
// intervention event this call appended.
type RequeueResult struct {
	TaskID uuid.UUID

	EscalationEventID uuid.UUID
	EscalationReason  EscalationReason
	CounterReset      ResetCounter
	ResultingLane     Lane

	InterventionEvent InterventionEvent
}

// RequeueTask is TaskStore.RequeueTask (FR6): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, the same lock ClaimTask/
// CompleteTask/CancelTask/EscalateTask take):
//
//  1. Refuses a cancelled task (ErrTaskCancelled) -- FR7's dead-letter
//     state is explicitly one requeue cannot reopen -- and a task with no
//     active escalation (ErrTaskNotEscalated), nothing written in either
//     case.
//  2. Reads the task's active escalation (task.current_escalation_id) --
//     by construction there is exactly one (FR9's one-event rule,
//     recordEscalationTx's own doc comment), so "the counter whose cap
//     triggered the escalation" is unambiguous.
//  3. Resets exactly the counter named by that escalation's reason
//     (NFR4): thrash-cap -> thrash_count=0; attempt-cap -> attempt_count=
//     0; manual -> attempt_count=0 only when it is already at or past
//     DefaultAttemptCap (FR9's exception), otherwise neither counter is
//     touched.
//  4. Clears task.current_escalation_id in the same UPDATE, so the task
//     is claimable again under 016_escalation_axis.up.sql's task_
//     claimable_idx predicate. The task_escalation_event row itself is
//     never rewritten or deleted (NFR2, NFR5).
//  5. Appends one task_intervention_event row (action='requeue',
//     escalation_event_id naming the resolved escalation).
//
// Does NOT append a task_attempt row and does NOT increment
// attempt_count -- requeuing is explicitly not an attempt (FR6, #2851
// Assumption 7). Does NOT touch task.current_lane -- every escalation
// producer (FR2, FR3, FR9) leaves current_lane untouched, so it already
// equals the escalation's own lane_at_escalation; the task returns to
// claimable at that lane, not reverted, not advanced. Nothing in the
// task's attempt/verdict/note history is rewritten (NFR5).
func (s taskStore) RequeueTask(ctx context.Context, params RequeueParams) (RequeueResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return RequeueResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cancelledAt *time.Time
	var currentEscalationID *uuid.UUID
	var attemptCount int
	err = tx.QueryRow(ctx, `
		SELECT cancelled_at, current_escalation_id, attempt_count
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&cancelledAt, &currentEscalationID, &attemptCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return RequeueResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return RequeueResult{}, fmt.Errorf("lock task: %w", err)
	}
	if cancelledAt != nil {
		return RequeueResult{}, fmt.Errorf("%w: task id %s", ErrTaskCancelled, params.TaskID)
	}
	if currentEscalationID == nil {
		return RequeueResult{}, fmt.Errorf("%w: task id %s", ErrTaskNotEscalated, params.TaskID)
	}
	escalationID := *currentEscalationID

	var reasonStr string
	var laneAtEscalation string
	err = tx.QueryRow(ctx, `
		SELECT reason, lane_at_escalation FROM task_escalation_event WHERE id = $1
	`, escalationID).Scan(&reasonStr, &laneAtEscalation)
	if err != nil {
		return RequeueResult{}, fmt.Errorf("read task_escalation_event: %w", err)
	}
	reason := EscalationReason(reasonStr)

	var counterReset ResetCounter
	switch reason {
	case EscalationReasonThrashCap:
		if _, err := tx.Exec(ctx, `
			UPDATE task SET thrash_count = 0, current_escalation_id = NULL WHERE id = $1
		`, params.TaskID); err != nil {
			return RequeueResult{}, fmt.Errorf("reset thrash_count: %w", err)
		}
		counterReset = ResetCounterThrash
	case EscalationReasonAttemptCap:
		if _, err := tx.Exec(ctx, `
			UPDATE task SET attempt_count = 0, current_escalation_id = NULL WHERE id = $1
		`, params.TaskID); err != nil {
			return RequeueResult{}, fmt.Errorf("reset attempt_count: %w", err)
		}
		counterReset = ResetCounterAttempt
	case EscalationReasonManual:
		if attemptCount >= DefaultAttemptCap {
			// FR9's exception: EscalateTask's own force-close reached the
			// run-attempt cap, so this requeue must also reset
			// attempt_count -- otherwise the task would return to
			// claimable but ClaimTask's own ErrAttemptCapExhausted check
			// (task_claim.go) would immediately refuse it, stranding it.
			if _, err := tx.Exec(ctx, `
				UPDATE task SET attempt_count = 0, current_escalation_id = NULL WHERE id = $1
			`, params.TaskID); err != nil {
				return RequeueResult{}, fmt.Errorf("reset attempt_count (manual, at cap): %w", err)
			}
			counterReset = ResetCounterAttempt
		} else {
			if _, err := tx.Exec(ctx, `
				UPDATE task SET current_escalation_id = NULL WHERE id = $1
			`, params.TaskID); err != nil {
				return RequeueResult{}, fmt.Errorf("clear current_escalation_id: %w", err)
			}
			counterReset = ResetCounterNone
		}
	default:
		return RequeueResult{}, fmt.Errorf("%w: %q on task_escalation_event id %s", ErrUnknownEscalationReason, reasonStr, escalationID)
	}

	event, err := scanInterventionEvent(tx.QueryRow(ctx, `
		INSERT INTO task_intervention_event (
			scope_id, task_id, action, escalation_event_id, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
		RETURNING `+interventionEventColumns,
		params.ScopeID, params.TaskID, string(InterventionActionRequeue), escalationID, params.Reason,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return RequeueResult{}, fmt.Errorf("insert task_intervention_event: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return RequeueResult{}, fmt.Errorf("commit: %w", err)
	}

	return RequeueResult{
		TaskID:            params.TaskID,
		EscalationEventID: escalationID,
		EscalationReason:  reason,
		CounterReset:      counterReset,
		ResultingLane:     Lane(laneAtEscalation),
		InterventionEvent: event,
	}, nil
}
