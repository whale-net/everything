// This file (issue #2723, FR6, C14) is the work-axis heartbeat surface:
// TaskStore.Heartbeat extends a live claim's lease and appends one
// append-only `task_lease_event` row, mirroring task_claim.go's own
// transactional-cascade shape (a `task` row lock, not an application-level
// mutex) rather than a sequence of separate store calls.
//
// Heartbeat's anti-zombie rule (FR6): a caller's ClaimID must be the
// task's current_claim_id, and that claim row must not already be
// released. A run that was reclaimed out from under it (its lease lapsed
// and a new claimant took over) or whose claim was closed by complete/
// abandon gets ErrClaimNotCurrent, with nothing written -- it must not
// resume writing to a task it no longer owns.
//
// Semantic choice, documented here so #2724's reclaim-on-lapse sweep and
// this path cannot disagree: a heartbeat against a lease that has already
// expired, but has NOT yet been re-served (current_claim_id still names
// the caller's own claim), still extends the lease. "Current claim" here
// means "the claim task.current_claim_id still points at", never "the
// claim is unexpired" -- expiry only matters at the moment a *different*
// caller tries to claim or reclaim the task (task_claim.go's own
// claimable check). Until that reclaim actually happens, the original
// claimant is still free to resume heartbeating it back to life.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrClaimNotCurrent is Heartbeat's named rejection (FR6) when
// params.ClaimID is not the task's current_claim_id, or names a claim row
// that has already been released (closed by complete/abandon/reclaim) --
// the caller's claim is gone, and it must not write to this task again.
var ErrClaimNotCurrent = errors.New("krill/store: claim id is not the task's current, live claim")

// HeartbeatParams is Heartbeat's input (FR6): the task and claim a caller
// is extending the lease on, plus the LB4 subject pair the resulting
// task_lease_event row records.
type HeartbeatParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	ClaimID    uuid.UUID
	Acting     Subject
	OnBehalfOf Subject
}

// LeaseState is Heartbeat's result: the task/claim heartbeated and the
// new lease expiry it was extended to -- never a Claim or Task struct,
// since a heartbeat mutates neither's other fields.
type LeaseState struct {
	TaskID     uuid.UUID
	ClaimID    uuid.UUID
	ExtendedTo time.Time
}

// Heartbeat is TaskStore.Heartbeat (FR6) -- see this file's package doc
// comment for the anti-zombie rule and the expired-but-not-yet-reclaimed
// semantic it implements. Every step below runs inside the one
// transaction opened at the top:
//
//  1. Row-lock `task` (SELECT ... FOR UPDATE) and read current_claim_id --
//     the same lock task_claim.go's ClaimTask takes, so a heartbeat and a
//     concurrent claim/reclaim/complete/abandon on the same task can never
//     interleave.
//  2. Reject with ErrClaimNotCurrent, nothing written, if current_claim_id
//     is NULL or does not equal params.ClaimID, or if the named claim row
//     is already released.
//  3. Insert one `task_lease_event` row (append-only, NFR2) with
//     extended_to = NOW() + DefaultLeaseDuration and both subject pairs
//     (NFR3).
//  4. Update task.lease_expires_at in place to the same new value -- the
//     one permitted claimed-state mutation (this migration's own LB3
//     note on `task`).
//
// A heartbeat records no attempt (task_attempt is unchanged) -- it is
// progress on an existing attempt, not a new outcome.
func (s taskStore) Heartbeat(ctx context.Context, params HeartbeatParams) (LeaseState, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LeaseState{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentClaimID *uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT current_claim_id
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&currentClaimID)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeaseState{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return LeaseState{}, fmt.Errorf("lock task: %w", err)
	}

	if currentClaimID == nil || *currentClaimID != params.ClaimID {
		return LeaseState{}, fmt.Errorf("%w: task id %s", ErrClaimNotCurrent, params.TaskID)
	}

	var releasedAt *time.Time
	err = tx.QueryRow(ctx, `
		SELECT released_at FROM task_claim WHERE id = $1
	`, params.ClaimID).Scan(&releasedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return LeaseState{}, fmt.Errorf("%w: claim id %s", ErrClaimNotCurrent, params.ClaimID)
	}
	if err != nil {
		return LeaseState{}, fmt.Errorf("read task_claim: %w", err)
	}
	if releasedAt != nil {
		return LeaseState{}, fmt.Errorf("%w: claim id %s has already been released", ErrClaimNotCurrent, params.ClaimID)
	}

	// extended_to is computed by Postgres (NOW() + ..., mirroring
	// ClaimTask's own INSERT), and read back via RETURNING rather than
	// recomputed in Go -- both callers below (the LeaseState result and
	// task.lease_expires_at) must agree on the exact, microsecond-
	// truncated value Postgres actually stored, not a nanosecond-precision
	// Go value that would never compare equal to it.
	var extendedTo time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO task_lease_event (
			scope_id, task_id, claim_id, extended_to,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NOW() + ($4 * INTERVAL '1 second'), $5, $6, $7, $8, $9, $10)
		RETURNING extended_to
	`, params.ScopeID, params.TaskID, params.ClaimID, DefaultLeaseDuration.Seconds(),
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	).Scan(&extendedTo); err != nil {
		return LeaseState{}, fmt.Errorf("insert task_lease_event: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task SET lease_expires_at = $1 WHERE id = $2
	`, extendedTo, params.TaskID); err != nil {
		return LeaseState{}, fmt.Errorf("update task lease state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return LeaseState{}, fmt.Errorf("commit: %w", err)
	}

	return LeaseState{TaskID: params.TaskID, ClaimID: params.ClaimID, ExtendedTo: extendedTo}, nil
}
