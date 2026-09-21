// This file (issue #2726, FR9, C15) is the work-axis abandon surface: an
// Agent holding a task's current claim releases it without reporting a
// verdict -- unlike CompleteTask (task_complete.go, issue #2725),
// AbandonClaim never touches task.current_lane, and unlike ReclaimExpired
// (task_reclaim.go, issue #2724) it is the claimant itself asking to stop,
// not a sweep discovering a lapsed lease. Both of those paths, and this
// one, count against the exact same DefaultAttemptCap (task_claim.go,
// issue #2722) via the identical attempt_count column and the identical
// ClaimTask read-back check -- this file adds no second cap check of its
// own, only the same "insert one task_attempt row, increment
// attempt_count" shape #2724's reclaim sweep already established.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// AbandonParams is AbandonClaim's input (FR9): the claim the caller holds,
// an optional free-text reason, and the LB4 subject pair the resulting
// `abandoned` task_attempt row records. Deliberately carries no verdict
// field of any kind -- abandoning reports no outcome, unlike CompleteTask's
// pass/fail (FR9: "no verdict field exists on the abandon request").
type AbandonParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	ClaimID    uuid.UUID
	Reason     *string
	Acting     Subject
	OnBehalfOf Subject
}

// AbandonClaimResult is AbandonClaim's return value: the claim released and
// whether this abandon brought task.attempt_count to DefaultAttemptCap or
// beyond -- mirroring ReclaimedTask's own CapExhausted field
// (task_reclaim.go) for the identical purpose. The task is left claimed by
// no one either way; a CapExhausted task additionally fails any subsequent
// ClaimTask with ErrTaskEscalated once EscalationID below is set.
// EscalationID/EscalationReason are additive: nil unless CapExhausted, in
// which case they name the exact task_escalation_event this same
// transaction wrote (FR3, issue #2871).
type AbandonClaimResult struct {
	TaskID       uuid.UUID
	ClaimID      uuid.UUID
	CapExhausted bool

	EscalationID     *uuid.UUID
	EscalationReason *EscalationReason
}

// AbandonClaim is TaskStore.AbandonClaim (FR9): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, the same lock ClaimTask/
// CompleteTask/Heartbeat take), verifies params.ClaimID is that row's
// current, unreleased claim (ErrClaimNotCurrent otherwise -- the same rule
// #2723's Heartbeat and #2725's CompleteTask apply), marks the claim
// released (release_reason='abandon'), records one `abandoned` task_attempt
// row, increments task.attempt_count, and clears
// task.current_claim_id/lease_expires_at. If that increment brings
// attempt_count to DefaultAttemptCap or beyond, this same transaction also
// calls recordEscalationTx (task_escalation.go, FR3, issue #2871) -- never
// deferred to a later call. task.current_lane is never touched (FR9:
// abandoning is not a verdict).
func (s taskStore) AbandonClaim(ctx context.Context, params AbandonParams) (AbandonClaimResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return AbandonClaimResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentClaimID *uuid.UUID
	var currentLane string
	err = tx.QueryRow(ctx, `
		SELECT current_claim_id, current_lane
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&currentClaimID, &currentLane)
	if errors.Is(err, pgx.ErrNoRows) {
		return AbandonClaimResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return AbandonClaimResult{}, fmt.Errorf("lock task: %w", err)
	}

	if currentClaimID == nil || *currentClaimID != params.ClaimID {
		return AbandonClaimResult{}, fmt.Errorf("%w: task id %s", ErrClaimNotCurrent, params.TaskID)
	}

	var releasedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT released_at FROM task_claim WHERE id = $1
	`, params.ClaimID).Scan(&releasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AbandonClaimResult{}, fmt.Errorf("%w: claim id %s", ErrClaimNotCurrent, params.ClaimID)
	}
	if err != nil {
		return AbandonClaimResult{}, fmt.Errorf("read task_claim: %w", err)
	}
	if releasedAt != nil {
		return AbandonClaimResult{}, fmt.Errorf("%w: claim id %s has already been released", ErrClaimNotCurrent, params.ClaimID)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task_claim SET released_at = NOW(), release_reason = 'abandon'
		WHERE id = $1 AND released_at IS NULL
	`, params.ClaimID); err != nil {
		return AbandonClaimResult{}, fmt.Errorf("release claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_attempt (
			scope_id, task_id, claim_id, outcome,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'abandoned', $4, $5, $6, $7, $8, $9)
	`, params.ScopeID, params.TaskID, params.ClaimID,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	); err != nil {
		return AbandonClaimResult{}, fmt.Errorf("insert task_attempt: %w", err)
	}

	var newAttemptCount int
	if err := tx.QueryRow(ctx, `
		UPDATE task SET current_claim_id = NULL, lease_expires_at = NULL, attempt_count = attempt_count + 1
		WHERE id = $1
		RETURNING attempt_count
	`, params.TaskID).Scan(&newAttemptCount); err != nil {
		return AbandonClaimResult{}, fmt.Errorf("update task abandon state: %w", err)
	}

	result := AbandonClaimResult{
		TaskID:       params.TaskID,
		ClaimID:      params.ClaimID,
		CapExhausted: newAttemptCount >= DefaultAttemptCap,
	}
	if result.CapExhausted {
		// Recorded inside this same transaction, at the exact moment
		// CapExhausted is computed true (FR3, issue #2871) -- never
		// deferred to a later ClaimTask, which has no reason to be called
		// against a task already known to be exhausted.
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
			return AbandonClaimResult{}, fmt.Errorf("record attempt-cap escalation: %w", err)
		}
		result.EscalationID = &event.ID
		reason := event.Reason
		result.EscalationReason = &reason
	}

	if err := tx.Commit(ctx); err != nil {
		return AbandonClaimResult{}, fmt.Errorf("commit: %w", err)
	}

	return result, nil
}
