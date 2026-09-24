// This file (issue #2872, root plan #2851's M5, FR8) is the work-axis
// release surface: a Swarm Operator force-closes the active lease on a
// claimed task directly, independent of lease expiry (distinct from
// #2724's ReclaimExpired, which only ever acts on a lapsed lease). Release
// shares forceCloseClaimTx (task_escalation.go, issue #2868) with cancel
// (task_cancel.go) and escalate (task_escalate.go) -- the same "original
// claimant's subsequent heartbeat/complete/abandon is rejected exactly the
// way a reclaim already rejects one" guarantee holds here too.
//
// Assumption 2 (#2851): a manual release counts as an attempt against the
// exact same DefaultAttemptCap (task_claim.go) ClaimTask/ReclaimExpired/
// AbandonClaim enforce -- one `task_attempt` row (outcome='released',
// 016_escalation_axis.up.sql), attempt_count+1, and -- when that increment
// reaches the cap -- the attempt-cap escalation (FR3, issue #2871) recorded
// in this same transaction, never deferred to a later claim. Otherwise the
// operator could dodge the cap by releasing instead of letting the lease
// lapse.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrTaskNotClaimed is ReleaseLease's named rejection when the task has no
// open claim to release -- release force-closes a live lease (FR8); it is
// not a no-op over an already-unclaimed task, unlike forceCloseClaimTx's
// own no-op posture for cancel/escalate, which apply whether or not a
// claim happens to be open.
var ErrTaskNotClaimed = errors.New("krill/store: task has no open claim to release")

// ReleaseParams is ReleaseLease's input (FR8): the task whose active lease
// is being force-closed, an optional free-text rationale
// (task_intervention_event.reason), and the LB4 subject pair -- always the
// operator, never the original claimant -- recorded on both the resulting
// task_attempt and task_intervention_event rows.
type ReleaseParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	Reason     *string
	Acting     Subject
	OnBehalfOf Subject
}

// ReleaseResult is ReleaseLease's return value: the claim force-closed,
// whether this release brought task.attempt_count to DefaultAttemptCap or
// beyond (mirroring AbandonClaimResult/ReclaimedTask's own CapExhausted
// field), and the intervention event this call appended. EscalationID/
// EscalationReason are additive: nil unless CapExhausted, in which case
// they name the exact task_escalation_event this same transaction wrote.
type ReleaseResult struct {
	TaskID       uuid.UUID
	ClaimID      uuid.UUID
	CapExhausted bool

	EscalationID     *uuid.UUID
	EscalationReason *EscalationReason

	InterventionEvent InterventionEvent
}

// ReleaseLease is TaskStore.ReleaseLease (FR8): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, the same lock ClaimTask/
// CompleteTask/AbandonClaim take):
//
//  1. Refuses a cancelled task (ErrTaskCancelled) and an unclaimed task
//     (ErrTaskNotClaimed), nothing written in either case.
//  2. forceCloseClaimTx (task_escalation.go, release_reason='release') --
//     the original claimant's subsequent heartbeat/complete/abandon is then
//     rejected exactly the way a reclaim already rejects one.
//  3. Appends one `released` task_attempt row and increments
//     attempt_count (Assumption 2: release counts as an attempt against
//     the same DefaultAttemptCap ClaimTask/ReclaimExpired/AbandonClaim
//     enforce).
//  4. If that increment reaches DefaultAttemptCap, records the
//     attempt-cap escalation (recordEscalationTx, FR3, issue #2871) in
//     this same transaction, at the exact moment the cap is known
//     reached -- never deferred to a later claim.
//  5. Appends one `task_intervention_event` row (action='release',
//     escalation_event_id=NULL -- only requeue names one).
//
// Otherwise (below cap) the task is claimable again immediately (M4 FR3).
// current_lane is never touched -- release is not a verdict.
func (s taskStore) ReleaseLease(ctx context.Context, params ReleaseParams) (ReleaseResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("begin tx: %w", err)
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
		return ReleaseResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("lock task: %w", err)
	}
	if cancelledAt != nil {
		return ReleaseResult{}, fmt.Errorf("%w: task id %s", ErrTaskCancelled, params.TaskID)
	}
	if currentClaimID == nil {
		return ReleaseResult{}, fmt.Errorf("%w: task id %s", ErrTaskNotClaimed, params.TaskID)
	}
	claimID := *currentClaimID

	if err := forceCloseClaimTx(ctx, tx, params.TaskID, "release"); err != nil {
		return ReleaseResult{}, fmt.Errorf("force close claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_attempt (
			scope_id, task_id, claim_id, outcome,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'released', $4, $5, $6, $7, $8, $9)
	`, params.ScopeID, params.TaskID, claimID,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	); err != nil {
		return ReleaseResult{}, fmt.Errorf("insert task_attempt: %w", err)
	}

	var newAttemptCount int
	if err := tx.QueryRow(ctx, `
		UPDATE task SET attempt_count = attempt_count + 1 WHERE id = $1 RETURNING attempt_count
	`, params.TaskID).Scan(&newAttemptCount); err != nil {
		return ReleaseResult{}, fmt.Errorf("increment attempt_count: %w", err)
	}

	result := ReleaseResult{
		TaskID:       params.TaskID,
		ClaimID:      claimID,
		CapExhausted: newAttemptCount >= DefaultAttemptCap,
	}
	if result.CapExhausted {
		counterValue := newAttemptCount
		capValue := DefaultAttemptCap
		event, err := recordEscalationTx(ctx, tx, RecordEscalationParams{
			ScopeID:          params.ScopeID,
			TaskID:           params.TaskID,
			Reason:           EscalationReasonAttemptCap,
			CounterValue:     &counterValue,
			CapValue:         &capValue,
			LaneAtEscalation: Lane(currentLane),
			Acting:           params.Acting,
			OnBehalfOf:       params.OnBehalfOf,
		})
		if err != nil {
			return ReleaseResult{}, fmt.Errorf("record attempt-cap escalation: %w", err)
		}
		result.EscalationID = &event.ID
		reason := event.Reason
		result.EscalationReason = &reason
	}

	event, err := scanInterventionEvent(tx.QueryRow(ctx, `
		INSERT INTO task_intervention_event (
			scope_id, task_id, action, escalation_event_id, reason,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NULL, $4, $5, $6, $7, $8, $9, $10)
		RETURNING `+interventionEventColumns,
		params.ScopeID, params.TaskID, string(InterventionActionRelease), params.Reason,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return ReleaseResult{}, fmt.Errorf("insert task_intervention_event: %w", err)
	}
	result.InterventionEvent = event

	if err := tx.Commit(ctx); err != nil {
		return ReleaseResult{}, fmt.Errorf("commit: %w", err)
	}

	return result, nil
}
