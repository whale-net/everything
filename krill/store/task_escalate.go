// This file (issue #2872, root plan #2851's M5, FR9) is the work-axis
// manual-escalate surface: a Swarm Operator escalates a task at any time,
// recording the same reasoned escalation event FR2 (issue #2870)/FR3
// (issue #2871) record automatically, but with reason 'manual' and the
// operator as the acting subject -- giving a human who notices a problem
// before either automatic counter trips the same path into FR5's console
// and FR6/FR7's recovery verbs.
//
// Design choice, recorded here per this task's own issue body ("Close the
// open ambiguity #2851's design notes flag"): escalating an
// already-escalated task is rejected, not a silent no-op. It reuses
// ErrTaskEscalated -- recordEscalationTx (task_escalation.go) already
// names this exact rejection in its own doc comment ("recordEscalationTx's
// named rejection when a task that already has one active escalation is
// escalated again (FR9's one-event rule)") -- rather than a second,
// redundant sentinel for the identical condition ClaimTask's own
// claimability check already reports with this error. A no-op would let a
// caller believe their escalate call is what put the task in front of an
// operator, when in fact an earlier, possibly differently-reasoned event
// did; a loud rejection keeps "the one escalation event this task has" a
// question with exactly one accurate answer (see krill/ARCHITECTURE.md's
// entry for this task for the fuller rationale).
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// EscalateParams is EscalateTask's input (FR9): the task to escalate, an
// optional free-text rationale (task_intervention_event.reason, distinct
// from EscalationEvent.Reason's fixed vocabulary), and the LB4 subject
// pair -- the operator -- recorded on the resulting task_escalation_event,
// any force-closed-claim task_attempt row, and the task_intervention_event
// row this call appends.
type EscalateParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	Reason     *string
	Acting     Subject
	OnBehalfOf Subject
}

// EscalateResult is EscalateTask's return value: the manual escalation
// event recorded, whether an open claim was force-closed along the way
// (and, if so, whether that force-close brought attempt_count to
// DefaultAttemptCap or beyond -- reported for visibility only: per FR9's
// exception below, crossing the cap here never adds a second escalation
// event), and the intervention event this call appended.
type EscalateResult struct {
	TaskID          uuid.UUID
	EscalationEvent EscalationEvent

	ClaimForceClosed bool
	CapExhausted     bool

	InterventionEvent InterventionEvent
}

// EscalateTask is TaskStore.EscalateTask (FR9): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, the same lock ClaimTask/
// CompleteTask/AbandonClaim/ReleaseLease take):
//
//  1. Refuses a cancelled task (ErrTaskCancelled), nothing written.
//  2. recordEscalationTx (task_escalation.go) with reason='manual',
//     counter_value/cap_value NULL (a manual escalation has no triggering
//     counter), lane_at_escalation=task.current_lane, acting subject=the
//     operator. Refuses an already-escalated task with ErrTaskEscalated
//     (see this file's own package doc comment for why) -- nothing
//     written past this point either.
//  3. If the task has an open claim, forceCloseClaimTx
//     (release_reason='escalate') -- a manual escalation is a judgment
//     call that the task is no longer safely progressing, so it must not
//     leave a claimant free to keep heartbeating or completing against a
//     task the console now shows as escalated.
//  4. That force-close counts as an attempt against the same run-attempt
//     cap FR7/FR8 enforce: one `force-closed` task_attempt row,
//     attempt_count+1. Without this, an escalate-then-requeue round trip
//     would force-close a live claim and return the task to claimable at
//     the same lane -- FR8's exact end state -- without ever touching the
//     counter FR8 protects.
//  5. FR9's stated exception to FR8's "cap crossed => attempt-cap
//     escalation recorded in the same transaction" rule: even where this
//     force-close is itself the attempt that reaches DefaultAttemptCap,
//     no second escalation event is recorded here -- this call's own
//     'manual' event (step 2) already occupies task.current_escalation_id,
//     so calling recordEscalationTx a second time in this same
//     transaction would itself fail with ErrTaskEscalated; this method
//     simply never attempts it. A single task therefore never carries two
//     concurrently-active escalation events, keeping FR6's "reset the
//     counter whose cap triggered *the* escalation" unambiguous.
//  6. Appends one `task_intervention_event` row (action='escalate',
//     escalation_event_id=NULL -- the freshly-inserted
//     task_escalation_event from step 2 is this call's own result, not a
//     reference back to itself; only requeue names one).
func (s taskStore) EscalateTask(ctx context.Context, params EscalateParams) (EscalateResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return EscalateResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var cancelledAt *time.Time
	var currentClaimID *uuid.UUID
	var currentLane string
	err = tx.QueryRow(ctx, `
		SELECT cancelled_at, current_claim_id, current_lane
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&cancelledAt, &currentClaimID, &currentLane)
	if errors.Is(err, pgx.ErrNoRows) {
		return EscalateResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return EscalateResult{}, fmt.Errorf("lock task: %w", err)
	}
	if cancelledAt != nil {
		return EscalateResult{}, fmt.Errorf("%w: task id %s", ErrTaskCancelled, params.TaskID)
	}

	escalation, err := recordEscalationTx(ctx, tx, RecordEscalationParams{
		ScopeID:          params.ScopeID,
		TaskID:           params.TaskID,
		Reason:           EscalationReasonManual,
		CounterValue:     nil,
		CapValue:         nil,
		LaneAtEscalation: Lane(currentLane),
		Acting:           params.Acting,
		OnBehalfOf:       params.OnBehalfOf,
	})
	if err != nil {
		return EscalateResult{}, fmt.Errorf("record manual escalation: %w", err)
	}

	result := EscalateResult{
		TaskID:          params.TaskID,
		EscalationEvent: escalation,
	}

	if currentClaimID != nil {
		if err := forceCloseClaimTx(ctx, tx, params.TaskID, "escalate"); err != nil {
			return EscalateResult{}, fmt.Errorf("force close claim: %w", err)
		}
		result.ClaimForceClosed = true

		if _, err := tx.Exec(ctx, `
			INSERT INTO task_attempt (
				scope_id, task_id, claim_id, outcome,
				created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
				created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
			) VALUES ($1, $2, $3, 'force-closed', $4, $5, $6, $7, $8, $9)
		`, params.ScopeID, params.TaskID, *currentClaimID,
			params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
			params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
		); err != nil {
			return EscalateResult{}, fmt.Errorf("insert task_attempt: %w", err)
		}

		var newAttemptCount int
		if err := tx.QueryRow(ctx, `
			UPDATE task SET attempt_count = attempt_count + 1 WHERE id = $1 RETURNING attempt_count
		`, params.TaskID).Scan(&newAttemptCount); err != nil {
			return EscalateResult{}, fmt.Errorf("increment attempt_count: %w", err)
		}
		// FR9's exception: attempt_count may have reached DefaultAttemptCap
		// here, but no second escalation event is recorded -- see this
		// method's own doc comment, step 5.
		result.CapExhausted = newAttemptCount >= DefaultAttemptCap
	}

	event, err := scanInterventionEvent(tx.QueryRow(ctx, `
		INSERT INTO task_intervention_event (
			scope_id, task_id, action, escalation_event_id, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NULL, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+interventionEventColumns,
		params.ScopeID, params.TaskID, string(InterventionActionEscalate), params.Reason,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return EscalateResult{}, fmt.Errorf("insert task_intervention_event: %w", err)
	}
	result.InterventionEvent = event

	if err := tx.Commit(ctx); err != nil {
		return EscalateResult{}, fmt.Errorf("commit: %w", err)
	}

	return result, nil
}
