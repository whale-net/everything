// This file (issue #2724, FR7, C14/C16) is the work-axis reclaim surface:
// TaskStore.ReclaimExpired closes out a lease-expired claim, counts the
// lapse as an attempt, and either makes the task claimable again or --
// once DefaultAttemptCap (task_claim.go, #2722) is reached -- refuses to
// re-serve it, mirroring task_claim.go's own transactional-cascade shape
// (a `task` row lock, not an application-level mutex) per candidate task
// rather than a sequence of separate store calls.
//
// This is the single place `task_claim.release_reason = 'reclaim'` is
// ever written for a lease that lapsed with no new claimant yet --
// ClaimTask's own expired-lease branch (task_claim.go) is the other entry
// point into the identical situation (a caller asking to claim a task
// whose lease has already lapsed), and it writes that exact same closure
// inline rather than calling this file's code, because it has already
// paid for the row lock and decided to mint the replacement claim in the
// same transaction -- see task_claim.go's own package doc comment:
// "Claiming an unclaimed task and reclaiming a lease-expired one take the
// exact same path... one attempt accounting shape, not two." The two call
// sites therefore diverge only in what happens *after* the claim closes
// (ClaimTask mints a replacement claim; ReclaimExpired below leaves the
// task unclaimed), never in how the closure itself is written.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ReclaimParams is ReclaimExpired's input (FR7): the scope to sweep (NFR1),
// the LB4 subject pair recorded on every lapsed task_attempt row this sweep
// writes, and an optional TaskID to sweep exactly one task instead of every
// lease-expired task in scope.
type ReclaimParams struct {
	ScopeID    uuid.UUID
	TaskID     *uuid.UUID
	Acting     Subject
	OnBehalfOf Subject
}

// ReclaimedTask is one task ReclaimExpired's sweep actually reclaimed.
// CapExhausted reports whether this lapse brought task.attempt_count to
// DefaultAttemptCap or beyond -- FR7's terminal state for this milestone:
// the task is left claimed by no one either way (current_claim_id is
// always cleared), but a CapExhausted task additionally fails any
// subsequent ClaimTask with ErrAttemptCapExhausted (task_claim.go's own
// attempt_count >= DefaultAttemptCap check), never a second flag column or
// escalation destination on this row.
type ReclaimedTask struct {
	TaskID       uuid.UUID
	CapExhausted bool
}

// ReclaimResult is ReclaimExpired's output: every task the sweep actually
// reclaimed. A task that did not match the candidate predicate (a live
// lease, or -- for a repeated sweep -- a task already reclaimed since the
// candidate list was read) is simply absent, never an error.
type ReclaimResult struct {
	Reclaimed []ReclaimedTask
}

// ReclaimExpired is TaskStore.ReclaimExpired (FR7) -- see this file's
// package doc comment for why this is the one place
// `task_claim.release_reason = 'reclaim'` is written for a lease that
// lapsed with no new claimant. It reads the candidate set once (every task
// in params.ScopeID with a live current_claim_id and a lapsed
// lease_expires_at, or -- if params.TaskID is set -- exactly that one task
// id, checked for existence up front so an unknown or cross-scope id is a
// loud ErrNotFound rather than a silent empty result), then reclaims each
// candidate in its own transaction:
//
//  1. Row-lock `task` (SELECT ... FOR UPDATE) and re-check the candidate
//     predicate under the lock -- a task already reclaimed by a concurrent
//     sweep, or heartbeated back to life, since the candidate list was
//     read is silently skipped, not an error: this is what makes a
//     repeated sweep idempotent (NFR2).
//  2. Mark the stale `task_claim` row released (released_at = NOW(),
//     release_reason = 'reclaim') -- the same closure a later heartbeat
//     from the zombie run checks against and rejects with
//     ErrClaimNotCurrent (task_lease.go's own FR6 rule).
//  3. Insert one `task_attempt` row with outcome='lapsed' (FR7: the lapse
//     counts as an attempt) and increment task.attempt_count.
//  4. Clear task.current_claim_id/lease_expires_at unconditionally --
//     whether the task is actually claimable again now turns entirely on
//     the attempt_count ClaimTask reads back under its own row lock
//     (below cap: claimable; at/over cap: ErrAttemptCapExhausted), never a
//     second piece of state written here.
//
// task.current_lane is never touched (a lapse is not a verdict).
func (s taskStore) ReclaimExpired(ctx context.Context, params ReclaimParams) (ReclaimResult, error) {
	taskIDs, err := s.reclaimCandidates(ctx, params.ScopeID, params.TaskID)
	if err != nil {
		return ReclaimResult{}, err
	}

	result := ReclaimResult{}
	for _, taskID := range taskIDs {
		reclaimed, ok, err := s.reclaimOneTask(ctx, params.ScopeID, taskID, params.Acting, params.OnBehalfOf)
		if err != nil {
			return ReclaimResult{}, err
		}
		if ok {
			result.Reclaimed = append(result.Reclaimed, reclaimed)
		}
	}
	return result, nil
}

// reclaimCandidates resolves the set of task ids ReclaimExpired will
// attempt to reclaim: exactly taskID if given (after confirming it exists
// in scopeID, so an unknown/cross-scope single-task sweep is a loud
// ErrNotFound), otherwise every task in scopeID whose lease has already
// lapsed. The predicate here is a plain, unlocked read -- reclaimOneTask
// re-checks it under a row lock before writing anything, so a race between
// this read and a concurrent claim/heartbeat/sweep can only ever cause a
// candidate to be (harmlessly) skipped, never double-reclaimed.
func (s taskStore) reclaimCandidates(ctx context.Context, scopeID uuid.UUID, taskID *uuid.UUID) ([]uuid.UUID, error) {
	if taskID != nil {
		var exists bool
		if err := s.pool.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM task WHERE id = $1 AND scope_id = $2)
		`, *taskID, scopeID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check task exists: %w", err)
		}
		if !exists {
			return nil, errParentNotFound("task", *taskID)
		}
		return []uuid.UUID{*taskID}, nil
	}

	rows, err := s.pool.Query(ctx, `
		SELECT id FROM task
		WHERE scope_id = $1 AND current_claim_id IS NOT NULL AND lease_expires_at < NOW()
	`, scopeID)
	if err != nil {
		return nil, fmt.Errorf("list expired candidates: %w", err)
	}
	defer rows.Close()

	var taskIDs []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan expired candidate: %w", err)
		}
		taskIDs = append(taskIDs, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list expired candidates: %w", err)
	}
	return taskIDs, nil
}

// reclaimOneTask reclaims exactly one task id inside its own transaction --
// see ReclaimExpired's own doc comment for the four numbered steps. Returns
// ok=false (no error) when taskID no longer matches the candidate predicate
// under the row lock: either it names no row in scopeID (a stale id from a
// caller's earlier read), or its lease is not (or no longer) both claimed
// and expired.
func (s taskStore) reclaimOneTask(ctx context.Context, scopeID, taskID uuid.UUID, acting, onBehalfOf Subject) (ReclaimedTask, bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentClaimID *uuid.UUID
	var reclaimable bool
	err = tx.QueryRow(ctx, `
		SELECT current_claim_id, (current_claim_id IS NOT NULL AND lease_expires_at < NOW())
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, taskID, scopeID).Scan(&currentClaimID, &reclaimable)
	if errors.Is(err, pgx.ErrNoRows) {
		return ReclaimedTask{}, false, nil
	}
	if err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("lock task: %w", err)
	}

	if !reclaimable {
		// Already reclaimed by a concurrent sweep, or heartbeated back to
		// life, since the candidate list was read -- idempotent no-op, not
		// an error (NFR2).
		return ReclaimedTask{}, false, nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task_claim SET released_at = NOW(), release_reason = 'reclaim'
		WHERE id = $1 AND released_at IS NULL
	`, *currentClaimID); err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("release expired claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_attempt (
			scope_id, task_id, claim_id, outcome,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'lapsed', $4, $5, $6, $7, $8, $9)
	`, scopeID, taskID, *currentClaimID,
		acting.Iss, acting.Sub, string(acting.Kind),
		onBehalfOf.Iss, onBehalfOf.Sub, string(onBehalfOf.Kind),
	); err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("insert task_attempt: %w", err)
	}

	var newAttemptCount int
	if err := tx.QueryRow(ctx, `
		UPDATE task SET current_claim_id = NULL, lease_expires_at = NULL, attempt_count = attempt_count + 1
		WHERE id = $1
		RETURNING attempt_count
	`, taskID).Scan(&newAttemptCount); err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("update task reclaim state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ReclaimedTask{}, false, fmt.Errorf("commit: %w", err)
	}

	return ReclaimedTask{TaskID: taskID, CapExhausted: newAttemptCount >= DefaultAttemptCap}, true, nil
}
