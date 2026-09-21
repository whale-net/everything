// This file (issue #2725, FR8, C15) is the work-axis complete-with-a-
// verdict surface: an Agent holding a task's current claim reports
// `pass` or `fail`, and krill -- never the Agent -- decides where the
// task goes next. NextLane (below) is the pure routing function: it reads
// only the task's own lane_sequence/current_lane (FR1), never a global
// lane order and never a caller-supplied destination, so a request body
// naming a lane has nothing to land in (CompleteTaskParams carries no
// such field at all -- a compile-time guarantee, stronger than a runtime
// rejection). NextLane would read naturally as `work.NextLane`, mirroring
// this milestone's own suggestion, but Lane/CanonicalLaneOrder already
// live here in `store` (task.go), and //krill/work already imports
// `store` (payload.go) -- so a `store`-side CompleteTask calling into
// `work` would be an import cycle. It stays beside the lane vocabulary it
// routes over instead, exactly where CreateTask's own
// validateLaneSequence already lives.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Verdict is CompleteTask's two-value enum (FR8): the completing Agent
// reports an outcome, never a destination lane -- NextLane, not the
// caller, decides where the task goes.
type Verdict string

const (
	VerdictPass Verdict = "pass"
	VerdictFail Verdict = "fail"
)

// Valid reports whether v is one of Verdict's two fixed values.
func (v Verdict) Valid() bool {
	return v == VerdictPass || v == VerdictFail
}

// ErrUnknownVerdict is CompleteTask's rejection for a Verdict outside
// {VerdictPass, VerdictFail} -- checked before any DB round trip, the
// same "pure validation first" shape validateLaneSequence (task.go)
// already establishes for CreateTask.
var ErrUnknownVerdict = errors.New("krill/store: verdict must be \"pass\" or \"fail\"")

// ErrClaimNotCurrent (task_lease.go, issue #2723) is reused here: CompleteTask
// applies the same rule Heartbeat does to a stale or foreign claim id --
// nothing is written, no lane change, no attempt row.

// NextLane computes FR8's krill-decided lane transition, purely from
// sequence (the task's own lane_sequence) and current (its current_lane)
// -- no DB access, so every branch is exhaustively unit-testable without
// Postgres:
//
//   - pass: the next lane in sequence; Done if current is sequence's last
//     element.
//   - fail: the lane immediately preceding current in sequence; current
//     itself (staying put) if current is already sequence's first
//     element.
//
// current is always expected to be a member of sequence (CreateTask's
// own validateLaneSequence enforces that invariant at task creation, and
// no store method ever writes a current_lane outside lane_sequence
// afterward) -- if it somehow is not, NextLane returns current unchanged
// rather than guessing.
func NextLane(sequence []Lane, current Lane, verdict Verdict) Lane {
	idx := -1
	for i, l := range sequence {
		if l == current {
			idx = i
			break
		}
	}
	if idx < 0 {
		return current
	}

	switch verdict {
	case VerdictPass:
		if idx == len(sequence)-1 {
			return LaneDone
		}
		return sequence[idx+1]
	case VerdictFail:
		if idx == 0 {
			return current
		}
		return sequence[idx-1]
	default:
		return current
	}
}

// CompleteTaskParams is CompleteTask's input (FR8). Deliberately carries
// no lane/destination field of any kind -- the completing Agent names or
// chooses nothing about where the task goes next; NextLane alone decides
// that, from ScopeID/TaskID's own row.
//
// Summary is accepted here purely for API-shape completeness (the issue's
// own params list names it) but this milestone defines no column or
// note-write step for it -- migration 015 (this task's own "no new
// migration" constraint) gives task_attempt no free-text column, and the
// issue's numbered CompleteTask algorithm never writes one. CompleteTask
// therefore does not persist Summary anywhere; a future task that decides
// where free-text completion commentary belongs (a task_note via
// RecordNote, a new task_attempt column, etc.) is the place to wire it up,
// not an ad hoc write added here without that product decision.
type CompleteTaskParams struct {
	ScopeID    uuid.UUID
	TaskID     uuid.UUID
	ClaimID    uuid.UUID
	Verdict    Verdict
	Summary    *string
	Acting     Subject
	OnBehalfOf Subject
}

// TaskLaneResult is CompleteTask's return value (FR8): the lane
// transition krill computed for this completion. work.Assembler.Assemble
// (#2721) remains the authoritative, re-fetched source of the task's
// resulting state for any caller-facing response (FR4/NFR4) -- this
// value exists only so CompleteTask's own caller (the HTTP handler, the
// MCP tool) has the transition's before/after without a second read.
//
// State/EscalationReason are additive (issue #2870, FR2): State is
// TaskStateEscalated, with EscalationReason naming
// EscalationReasonThrashCap, exactly when this completion tripped FR1's
// thrash cap -- TaskStateActive with a nil EscalationReason otherwise.
// ToLane always names the lane the task is actually held at: for a
// thrash-cap trip that is FromLane itself (NextLane's own revert is never
// applied), never a lane the task was not in fact routed to.
type TaskLaneResult struct {
	TaskID   uuid.UUID
	ClaimID  uuid.UUID
	Verdict  Verdict
	FromLane Lane
	ToLane   Lane

	State            TaskState
	EscalationReason *EscalationReason
}

// CompleteTask is TaskStore.CompleteTask (FR8): a single transaction that
// row-locks the `task` (SELECT ... FOR UPDATE, mirroring ClaimTask's own
// race-safety shape), verifies params.ClaimID is that row's current
// claim, computes the destination lane with NextLane against the task's
// own lane_sequence/current_lane, marks the claim released
// (release_reason='complete'), records one `completed` task_attempt row,
// and updates task.current_lane/current_claim_id/lease_expires_at in
// place.
//
// A completed attempt does NOT increment task.attempt_count: that column
// (this migration's own LB3 note, task.go) counts only attempts that
// closed badly -- a lapsed lease (#2724's reclaim sweep) or an abandon
// (#2726) -- never a claim by itself and never a completion. Of the three
// attempt-writing paths (ClaimTask's `claimed`, #2726's `abandoned`, this
// file's `completed`), only `abandoned` and #2724's `lapsed` ever
// increment attempt_count; the cap itself only ever refuses re-service
// for a lapsed or abandoned attempt (FR7), never for a completed one --
// so a task can walk its own lane_sequence end to end via any number of
// clean claim/complete cycles without ever moving this column, no matter
// how many lane transitions that walk takes.
//
// FR1/FR2/NFR4 (issue #2870): a VerdictFail also increments
// task.thrash_count by exactly 1, in this same transaction -- every
// failing verdict the task ever receives, total not consecutive, and
// never task.attempt_count (NFR4's independent-bookkeeping rule: a
// completion, pass or fail, never touches attempt_count at all, per the
// paragraph above). The moment that increment reaches DefaultThrashCap
// (task_claim.go), this call still succeeds exactly as a normal complete
// does -- the claim still closes with release_reason='complete' -- but
// NextLane's own revert is never applied: the task is held at fromLane,
// and recordEscalationTx (task_escalation.go, #2868) records one
// 'thrash-cap' escalation against it instead, in the same transaction, so
// an escalation insert failure rolls back the whole complete (no counter
// increment without its event).
func (s taskStore) CompleteTask(ctx context.Context, params CompleteTaskParams) (TaskLaneResult, error) {
	if !params.Verdict.Valid() {
		return TaskLaneResult{}, fmt.Errorf("%w: got %q", ErrUnknownVerdict, params.Verdict)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return TaskLaneResult{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	var currentClaimID *uuid.UUID
	var currentLane string
	var laneSeq []string
	var thrashCount int
	err = tx.QueryRow(ctx, `
		SELECT current_claim_id, current_lane, lane_sequence, thrash_count
		FROM task
		WHERE id = $1 AND scope_id = $2
		FOR UPDATE
	`, params.TaskID, params.ScopeID).Scan(&currentClaimID, &currentLane, &laneSeq, &thrashCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return TaskLaneResult{}, errParentNotFound("task", params.TaskID)
	}
	if err != nil {
		return TaskLaneResult{}, fmt.Errorf("lock task: %w", err)
	}

	if currentClaimID == nil || *currentClaimID != params.ClaimID {
		return TaskLaneResult{}, fmt.Errorf("%w: task id %s, claim id %s", ErrClaimNotCurrent, params.TaskID, params.ClaimID)
	}

	sequence := make([]Lane, len(laneSeq))
	for i, l := range laneSeq {
		sequence[i] = Lane(l)
	}
	fromLane := Lane(currentLane)
	toLane := NextLane(sequence, fromLane, params.Verdict)

	// FR1: total failing verdicts, never consecutive-only -- a pass never
	// moves this counter, and it is never reset by an intervening pass
	// (the roadmap's own alternating pass/fail/pass/fail sequence must
	// still trip the cap).
	newThrashCount := thrashCount
	if params.Verdict == VerdictFail {
		newThrashCount++
	}
	thrashCapped := newThrashCount >= DefaultThrashCap

	if thrashCapped {
		// FR2: held at the lane occupied when this call arrived --
		// NextLane's revert (computed above) is never applied.
		toLane = fromLane
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task_claim SET released_at = NOW(), release_reason = 'complete'
		WHERE id = $1 AND released_at IS NULL
	`, params.ClaimID); err != nil {
		return TaskLaneResult{}, fmt.Errorf("release claim: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO task_attempt (
			scope_id, task_id, claim_id, outcome,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, 'completed', $4, $5, $6, $7, $8, $9)
	`, params.ScopeID, params.TaskID, params.ClaimID,
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind),
	); err != nil {
		return TaskLaneResult{}, fmt.Errorf("insert task_attempt: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE task SET current_lane = $1, current_claim_id = NULL, lease_expires_at = NULL, thrash_count = $2
		WHERE id = $3
	`, string(toLane), newThrashCount, params.TaskID); err != nil {
		return TaskLaneResult{}, fmt.Errorf("update task lane state: %w", err)
	}

	state := TaskStateActive
	var escalationReason *EscalationReason
	if thrashCapped {
		capValue := DefaultThrashCap
		counterValue := newThrashCount
		if _, err := recordEscalationTx(ctx, tx, RecordEscalationParams{
			ScopeID:          params.ScopeID,
			TaskID:           params.TaskID,
			Reason:           EscalationReasonThrashCap,
			CounterValue:     &counterValue,
			CapValue:         &capValue,
			LaneAtEscalation: toLane,
			Acting:           params.Acting,
			OnBehalfOf:       params.OnBehalfOf,
		}); err != nil {
			return TaskLaneResult{}, fmt.Errorf("record thrash-cap escalation: %w", err)
		}
		state = TaskStateEscalated
		reason := EscalationReasonThrashCap
		escalationReason = &reason
	}

	if err := tx.Commit(ctx); err != nil {
		return TaskLaneResult{}, fmt.Errorf("commit: %w", err)
	}

	return TaskLaneResult{
		TaskID:           params.TaskID,
		ClaimID:          params.ClaimID,
		Verdict:          params.Verdict,
		FromLane:         fromLane,
		ToLane:           toLane,
		State:            state,
		EscalationReason: escalationReason,
	}, nil
}
