// This file (issue #2722, FR3/FR5, C14) is the work-axis claim surface:
// TaskStore.ClaimTask races callers for one `task` row safely (a Postgres
// row lock, not an application-level mutex), mints a lease, and records
// one `task_attempt` row -- all in one transaction, mirroring
// abandon.go's/recut.go's own transactional-cascade shape rather than a
// sequence of separate store calls. GetClaimByID is this file's read
// counterpart, the one work.Assembler.Assemble (krill/work/payload.go)
// calls to surface a task's current claim/lease state on the by-id
// payload (FR10).
//
// Claiming an unclaimed task and reclaiming a lease-expired one take the
// exact same path here on purpose: #2724's later sweep (the reclaim-on-
// lapse background job) reclaims a lapsed lease the same way a caller who
// simply asks to claim it next does -- one attempt accounting shape, not
// two.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// DefaultAttemptCap is FR7's attempt-cap value, provisional for this
// milestone: M5's C26 owns the real cap value and its escalation
// destination once a task exhausts it. Every path that counts an attempt
// against this cap (ClaimTask here, #2724's reclaim sweep, #2726's
// abandon path) reads this one constant -- never a per-task cap column, a
// config knob, or an escalation queue in this milestone.
const DefaultAttemptCap = 3

// DefaultLeaseDuration is the fixed lease length ClaimTask mints (FR3),
// likewise provisional and config-free in this milestone -- no
// environment variable or per-scope override exists yet.
const DefaultLeaseDuration = 15 * time.Minute

// ErrTaskAlreadyClaimed is ClaimTask's named rejection when the task is
// currently claimed by a live (non-expired) lease -- the race-loser's
// outcome (FR3): at most one concurrent caller ever sees success.
var ErrTaskAlreadyClaimed = errors.New("krill/store: task is already claimed and its lease has not expired")

// ErrDependenciesUnsatisfied is ClaimTask's named rejection when
// UnsatisfiedDependencies (task_dependency.go) returns a non-empty set --
// the error names every blocking task id, never just "blocked".
var ErrDependenciesUnsatisfied = errors.New("krill/store: task has unsatisfied dependencies")

// ErrAttemptCapExhausted is ClaimTask's named rejection when
// task.attempt_count has already reached DefaultAttemptCap -- FR7's
// terminal state for this milestone: the task simply stays claimed by no
// one, since escalation is M5's C26, out of scope here.
var ErrAttemptCapExhausted = errors.New("krill/store: task has exhausted its attempt cap")

// Claim is one row of `task_claim` (migration 015) -- append-only claim
// events with one narrow in-place exception (ReleasedAt/ReleaseReason,
// set exactly once when the claim ends: FR7 reclaim, FR8 complete, FR9
// abandon -- only FR7's reclaim is written by this file; FR8/FR9 are
// later M4 tasks). Every other field is written once at INSERT and never
// touched again (this migration's own LB3 note on task_claim).
type Claim struct {
	ID                    uuid.UUID
	ScopeID               uuid.UUID
	TaskID                uuid.UUID
	SessionID             SessionID
	ClaimedAt             time.Time
	InitialLeaseExpiresAt time.Time
	ReleasedAt            *time.Time
	ReleaseReason         *string

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- ClaimTask is the only write path onto this table, and it
	// always has a real caller session (NFR6's write gate).
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// ClaimTaskParams is ClaimTask's input (FR3, FR5): the task to claim, the
// claiming session, and the LB4 subject pair both task_claim and
// task_attempt rows record.
type ClaimTaskParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	SessionID  SessionID
	Acting     Subject
	OnBehalfOf Subject
}

const claimColumns = `id, scope_id, task_id, session_id, claimed_at, initial_lease_expires_at, released_at, release_reason, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanClaim(row pgx.Row) (Claim, error) {
	var c Claim
	var sessionID uuid.UUID
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&c.ID, &c.ScopeID, &c.TaskID, &sessionID, &c.ClaimedAt, &c.InitialLeaseExpiresAt, &c.ReleasedAt, &c.ReleaseReason,
		&c.CreatedByActing.Iss, &c.CreatedByActing.Sub, &actingKind,
		&c.CreatedByOnBehalfOf.Iss, &c.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&c.CreatedAt,
	)
	if err != nil {
		return Claim{}, err
	}
	c.SessionID = SessionID(sessionID)
	c.CreatedByActing.Kind = SubjectKind(actingKind)
	c.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return c, nil
}

// ClaimTask is TaskStore.ClaimTask (FR3, FR5) -- see task.go's interface
// doc comment for the shape summary. Every step below runs inside the one
// transaction opened at the top:
//
//  1. Row-lock `task` (SELECT ... FOR UPDATE) and read the state needed to
//     decide claimability in the same round trip -- this lock is what
//     makes two concurrent callers race safely: at most one proceeds past
//     it holding a "claimable" answer that stays true for the rest of this
//     transaction.
//  2. Reject with a named error, nothing written, on the first
//     claimability failure: ErrTaskAlreadyClaimed (a live lease already
//     held), ErrDependenciesUnsatisfied (naming every blocking task),
//     ErrAttemptCapExhausted (FR7's terminal state).
//  3. If a prior claim's lease had lapsed, mark it released with
//     release_reason='reclaim' -- the same accounting #2724's sweep
//     produces for the identical situation.
//  4. Insert one `task_claim` row (claimed_at/initial_lease_expires_at
//     both NOW()-derived, so they agree with the claimability check above:
//     NOW() is stable for the whole transaction) and one `task_attempt`
//     row with outcome='claimed'.
//  5. Update task.current_claim_id/lease_expires_at/attempt_count in
//     place -- the one permitted in-place mutation shape (NFR2).
func (s taskStore) ClaimTask(ctx context.Context, params ClaimTaskParams) (Claim, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Claim{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentClaimID *uuid.UUID
	var attemptCount int
	var claimable bool
	err = tx.QueryRow(ctx, `
		SELECT current_claim_id, attempt_count, (current_claim_id IS NULL OR lease_expires_at < NOW())
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&currentClaimID, &attemptCount, &claimable)
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return Claim{}, fmt.Errorf("lock task: %w", err)
	}

	if !claimable {
		return Claim{}, fmt.Errorf("%w: task id %s", ErrTaskAlreadyClaimed, params.TaskID)
	}

	unsatisfied, err := unsatisfiedDependencies(ctx, tx, params.ScopeID, params.TaskID)
	if err != nil {
		return Claim{}, err
	}
	if len(unsatisfied) > 0 {
		return Claim{}, fmt.Errorf("%w: task id %s blocked by %v", ErrDependenciesUnsatisfied, params.TaskID, unsatisfied)
	}

	if attemptCount >= DefaultAttemptCap {
		return Claim{}, fmt.Errorf("%w: task id %s has reached %d attempts", ErrAttemptCapExhausted, params.TaskID, DefaultAttemptCap)
	}

	// currentClaimID is non-nil here only in the reclaim-eligible case
	// (claimable was true and current_claim_id was not null implies its
	// lease had already lapsed) -- close it out before minting the new
	// claim, the same release_reason #2724's sweep uses for the identical
	// situation.
	if currentClaimID != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE task_claim SET released_at = NOW(), release_reason = 'reclaim'
			WHERE id = $1 AND released_at IS NULL
		`, *currentClaimID); err != nil {
			return Claim{}, fmt.Errorf("release expired claim: %w", err)
		}
	}

	claim, err := scanClaim(tx.QueryRow(ctx, `
		INSERT INTO task_claim (
			scope_id, task_id, session_id, initial_lease_expires_at,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, NOW() + ($4 * INTERVAL '1 second'), $5, $6, $7, $8, $9, $10)
		RETURNING `+claimColumns,
		params.ScopeID, params.TaskID, uuid.UUID(params.SessionID), DefaultLeaseDuration.Seconds(),
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	))
	if err != nil {
		return Claim{}, fmt.Errorf("insert task_claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_attempt (
			scope_id, task_id, claim_id, outcome,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'claimed', $4, $5, $6, $7, $8, $9)
	`, params.ScopeID, params.TaskID, claim.ID,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	); err != nil {
		return Claim{}, fmt.Errorf("insert task_attempt: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task SET current_claim_id = $1, lease_expires_at = $2, attempt_count = attempt_count + 1
		WHERE id = $3
	`, claim.ID, claim.InitialLeaseExpiresAt, params.TaskID); err != nil {
		return Claim{}, fmt.Errorf("update task claim state: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Claim{}, fmt.Errorf("commit: %w", err)
	}
	return claim, nil
}

// GetClaimByID returns the Claim row for id. See TaskStore.GetClaimByID's
// own doc comment (task.go) for its one caller today.
func (s taskStore) GetClaimByID(ctx context.Context, id uuid.UUID) (Claim, error) {
	claim, err := scanClaim(s.pool.QueryRow(ctx, `
		SELECT `+claimColumns+`
		FROM task_claim
		WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Claim{}, errParentNotFound("task_claim", id)
	}
	if err != nil {
		return Claim{}, fmt.Errorf("get task_claim: %w", err)
	}
	return claim, nil
}
