// This file (issue #2719, FR1, C14) is TaskStore -- the work-axis store
// surface migration 015 creates. It ships one method, CreateTask, and is
// the interface every later M4 task (#2720 dependency declaration,
// #2722-#2726 claim/lease/attempt, #2727 notes) widens with its own
// methods rather than introducing a sibling accessor -- see #2720's issue
// body: "Store API on store.TaskStore (or a sibling TaskDependencyStore
// reachable from the same entities accessor)" settles on the former. This
// mirrors MilestoneAuthoringStore's own incremental growth
// (milestone_authoring.go gained CreateMilepebble/AddMilepebbleDelivers/
// AddDiscoveredScope across several M3 tasks without ever splitting into
// a second interface).
//
// CreateTask resolves params.MilestoneID against `milestone_ref` inside
// the same transaction as its INSERT, mirroring
// MilestoneAuthoringStore.CreateMilepebble's own parent-kind check
// (milestone_authoring.go) -- see this file's errNotADeliveryTarget and
// errMilestoneHasMilepebbleCut for FR1's two rejection shapes.
package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Lane is one of task.lane_sequence's/task.current_lane's five fixed
// values (015_work_axis.up.sql's CHECK constraints) -- the same five-name
// vocabulary this project's own per-task swimlanes use (Scaffold,
// Implementation, Testing, Validation, Done), FR1's "ordered subset...
// lanes skippable" convention. Not an arbitrary string: every store method
// that writes `current_lane` or an element of `lane_sequence` does so
// through this type, never a bare string, so a typo can never reach the
// DB CHECK as anything other than a compile error.
type Lane string

const (
	LaneScaffold       Lane = "Scaffold"
	LaneImplementation Lane = "Implementation"
	LaneTesting        Lane = "Testing"
	LaneValidation     Lane = "Validation"
	LaneDone           Lane = "Done"
)

// CanonicalLaneOrder is the fixed order FR1's "ordered subset... lanes
// skippable" convention is defined against -- CreateTask's Implementation
// phase validates that a given LaneSequence is a subset of this slice
// preserving this same relative order, never a reordering of it.
var CanonicalLaneOrder = []Lane{LaneScaffold, LaneImplementation, LaneTesting, LaneValidation, LaneDone}

// canonicalLaneIndex returns lane's position in CanonicalLaneOrder, or -1
// if lane is not one of the five fixed values.
func canonicalLaneIndex(lane Lane) int {
	for i, l := range CanonicalLaneOrder {
		if l == lane {
			return i
		}
	}
	return -1
}

// ErrInvalidLaneSequence is CreateTask's rejection for a lane_sequence
// that is empty, contains a duplicate, contains a value outside
// CanonicalLaneOrder, or is not in that order's relative order (lanes may
// be skipped, never reordered -- FR1).
var ErrInvalidLaneSequence = errors.New("krill/store: lane_sequence must be a non-empty, duplicate-free, canonical-order subset of {Scaffold, Implementation, Testing, Validation, Done}")

// ErrStartingLaneNotInSequence is CreateTask's rejection when
// StartingLane is not itself a member of LaneSequence (FR1).
var ErrStartingLaneNotInSequence = errors.New("krill/store: starting_lane must be a member of lane_sequence")

// validateLaneSequence enforces FR1's "ordered subset... lanes skippable"
// convention: seq must be non-empty, every element must be one of
// CanonicalLaneOrder's five values, no element may repeat, and each
// element's canonical index must strictly increase across seq (an
// out-of-order sequence, e.g. [Testing, Implementation], is rejected the
// same way a duplicate is). starting must be a member of seq.
func validateLaneSequence(seq []Lane, starting Lane) error {
	if len(seq) == 0 {
		return fmt.Errorf("%w: empty", ErrInvalidLaneSequence)
	}
	seen := make(map[Lane]bool, len(seq))
	lastIdx := -1
	for _, lane := range seq {
		if seen[lane] {
			return fmt.Errorf("%w: duplicate lane %q", ErrInvalidLaneSequence, lane)
		}
		seen[lane] = true
		idx := canonicalLaneIndex(lane)
		if idx < 0 {
			return fmt.Errorf("%w: %q is not one of %v", ErrInvalidLaneSequence, lane, CanonicalLaneOrder)
		}
		if idx <= lastIdx {
			return fmt.Errorf("%w: %q is out of canonical order", ErrInvalidLaneSequence, lane)
		}
		lastIdx = idx
	}
	if !seen[starting] {
		return fmt.Errorf("%w: %q", ErrStartingLaneNotInSequence, starting)
	}
	return nil
}

// Task is one row of `task` (migration 015, issue #2719, FR1) -- the one
// append-only-plus-claimed table this milestone ships (NFR2, LB3; see
// 015_work_axis.up.sql's own LB3 note on this table). MilestoneID is the
// one delivery-axis reference this struct carries (NFR7, LB6): a
// MilestoneRef row of either Kind, never a Feature.ID or Requirement.ID.
type Task struct {
	ID           uuid.UUID
	ScopeID      uuid.UUID
	MilestoneID  uuid.UUID
	Title        string
	Body         *string
	LaneSequence []Lane
	CurrentLane  Lane

	// AttemptCount/CurrentClaimID/LeaseExpiresAt are the claim/lease state
	// CreateTask never sets directly (AttemptCount starts at 0,
	// CurrentClaimID/LeaseExpiresAt start nil) -- later M4 tasks' claim/
	// heartbeat/reclaim methods are this struct's only other writers.
	AttemptCount   int
	CurrentClaimID *uuid.UUID
	LeaseExpiresAt *time.Time

	// ThrashCount/CurrentEscalationID/CancelledAt (016_escalation_axis.
	// up.sql, issue #2868) are M5's additive escalation-axis state --
	// CreateTask never sets any of them directly (ThrashCount starts at 0,
	// CurrentEscalationID/CancelledAt start nil). ThrashCount is FR1's
	// lane-thrash counter, fed and reset independently of AttemptCount
	// (NFR4). CurrentEscalationID names the task's one active
	// EscalationEvent (recordEscalationTx sets it, requeue/cancel clear
	// it -- task_escalation.go's own doc comment); ClaimTask refuses any
	// task with this set (ErrTaskEscalated), regardless of claim state.
	// CancelledAt is FR7's dead-letter terminal state, distinct from lane
	// Done; ClaimTask refuses any task with this set (ErrTaskCancelled).
	ThrashCount         int
	CurrentEscalationID *uuid.UUID
	CancelledAt         *time.Time

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR3,
	// LB4) -- CreateTask is the only write path onto this table, and it
	// always has a real caller session (NFR6's write gate).
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}

// CreateTaskParams is CreateTask's input (FR1). Exactly the fields
// 015_work_axis's issue body names for the Implementation phase: a single
// delivery-axis reference (MilestoneID, resolved to either a milepebble or
// a milestone with no milepebble cut), the lane sequence and starting
// lane, and the LB4 subject pair.
type CreateTaskParams struct {
	ScopeID      uuid.UUID
	MilestoneID  uuid.UUID
	Title        string
	Body         *string
	LaneSequence []Lane
	StartingLane Lane
	Acting       Subject
	OnBehalfOf   Subject
}

// TaskStore is the work-axis store surface (migration 015, issue #2719).
// See this file's package doc comment for why later M4 tasks widen this
// same interface rather than introducing a sibling.
type TaskStore interface {
	// CreateTask inserts a new `task` row scoped to exactly one
	// milepebble, or to a milestone directly when that milestone has no
	// milepebble cut (FR1, NFR7/LB6). Validates that params.LaneSequence
	// is a non-empty ordered subset of CanonicalLaneOrder with no
	// duplicates, and that params.StartingLane is a member of it -- never
	// derived from a branch name or other external reference (NFR5).
	CreateTask(ctx context.Context, params CreateTaskParams) (Task, error)

	// DeclareDependency records that params.TaskID depends on each id in
	// params.DependsOnTaskIDs, one append-only `task_dependency` row per
	// edge, all in one transaction (task_dependency.go, issue #2720,
	// FR2). Rejects (never silently skips) a TaskID or DependsOnTaskIDs
	// entry that names no `task` row in params.ScopeID (NFR1), a
	// self-edge (ErrSelfDependency), and a dependency cycle
	// (ErrDependencyCycle). Re-declaring an existing edge is idempotent --
	// absorbed by the unique index, not an error.
	DeclareDependency(ctx context.Context, params DeclareDependencyParams) error

	// ListDependencies returns taskID's declared dependencies, in
	// declaration order (task_dependency.go, issue #2720, FR2) -- the
	// ordered list the claim payload (#2721) carries.
	ListDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]TaskDependency, error)

	// UnsatisfiedDependencies is FR3's claimability predicate
	// (task_dependency.go, issue #2720): every dependency of taskID whose
	// own task has not reached its own terminal `Done` lane
	// (task.current_lane), evaluated in one scope-qualified SQL query
	// over the task_dependency/task join, never a per-dependency round
	// trip. A lane sequence that never contains `Done` is rejected at
	// CreateTask (above), so this predicate is always decidable.
	UnsatisfiedDependencies(ctx context.Context, scopeID, taskID uuid.UUID) ([]uuid.UUID, error)

	// GetTaskByID returns the Task row for id. Task ids are globally
	// unique surrogates (like every other entity id in this package), so
	// this takes no scope argument -- mirrors ProductStore.GetCurrentByID
	// and MilestoneStatusEventStore.CurrentStatus's own id-only read
	// shape. The one caller today is the ungated
	// ListTaskDependenciesHandler (task_dependency.go, issue #2720),
	// resolving a task's own ScopeID to scope-qualify ListDependencies/
	// UnsatisfiedDependencies when mounted with no session to source a
	// scope from -- the same "resolve scope from the entity itself"
	// posture GetProductDeliveryHandler already established
	// (milestone.go's own doc comment).
	GetTaskByID(ctx context.Context, id uuid.UUID) (Task, error)

	// ClaimTask is FR3/FR5's race-safe claim (task_claim.go, issue #2722):
	// a single transaction that row-locks the `task` (SELECT ... FOR
	// UPDATE), checks claimability (unclaimed or lease-expired,
	// UnsatisfiedDependencies empty, attempt cap not exceeded), inserts
	// one `task_claim` row and one `task_attempt` row, and updates
	// task.current_claim_id/lease_expires_at in place. Returns one of the
	// named errors below on any claimability failure -- never a generic
	// error -- with nothing written in that case.
	ClaimTask(ctx context.Context, params ClaimTaskParams) (Claim, error)

	// GetClaimByID returns the Claim row for id -- claim ids are globally
	// unique surrogates, mirroring GetTaskByID's own id-only shape
	// (task_claim.go, issue #2722). The one caller today is
	// work.Assembler.Assemble (krill/work/payload.go), resolving a task's
	// current claim/lease state for the by-id payload (FR10).
	GetClaimByID(ctx context.Context, id uuid.UUID) (Claim, error)

	// Heartbeat is FR6's lease extension (task_lease.go, issue #2723): in
	// one transaction that row-locks the same `task` row ClaimTask does,
	// verifies params.ClaimID is the task's current, live claim (rejecting
	// with ErrClaimNotCurrent and writing nothing otherwise -- the
	// anti-zombie rule), appends one append-only `task_lease_event` row,
	// and updates task.lease_expires_at in place. Records no attempt --
	// see task_lease.go's own doc comment for the full semantic,
	// including the expired-but-not-yet-reclaimed lease's chosen
	// behavior.
	Heartbeat(ctx context.Context, params HeartbeatParams) (LeaseState, error)

	// CompleteTask is FR8's krill-decided lane advance/revert
	// (task_complete.go, issue #2725): a single transaction that verifies
	// params.ClaimID is the task's current claim (ErrClaimNotCurrent
	// otherwise), computes the destination lane with the package-level
	// NextLane against the task's own lane_sequence/current_lane (never a
	// caller-supplied lane -- CompleteTaskParams carries no such field),
	// releases the claim (release_reason='complete'), records one
	// `completed` task_attempt row, and updates
	// task.current_lane/current_claim_id/lease_expires_at in place. A
	// VerdictFail also increments task.thrash_count (FR1, issue #2870);
	// the moment that reaches DefaultThrashCap, NextLane's revert is
	// skipped -- the task is held at its current lane instead, and one
	// 'thrash-cap' escalation is recorded against it (FR2).
	CompleteTask(ctx context.Context, params CompleteTaskParams) (TaskLaneResult, error)

	// ReclaimExpired is FR7's lease-expiry sweep (task_reclaim.go, issue
	// #2724): per candidate task (every lease-expired task in
	// params.ScopeID, or exactly params.TaskID when set), in its own
	// transaction, closes the stale claim (release_reason='reclaim'),
	// records one 'lapsed' task_attempt, increments attempt_count, and
	// clears current_claim_id/lease_expires_at -- making the task claimable
	// again below DefaultAttemptCap, or refusing to re-serve it (via
	// ClaimTask's own attempt-cap check) at/over it. current_lane is never
	// touched. A repeated sweep over an already-reclaimed task is a
	// no-op, not an error.
	ReclaimExpired(ctx context.Context, params ReclaimParams) (ReclaimResult, error)

	// AbandonClaim is FR9's claimant-initiated release (task_abandon.go,
	// issue #2726): a single transaction that verifies params.ClaimID is
	// the task's current, unreleased claim (ErrClaimNotCurrent otherwise,
	// the same rule Heartbeat/CompleteTask apply), releases it
	// (release_reason='abandon'), records one `abandoned` task_attempt
	// row, and increments attempt_count -- the exact same
	// DefaultAttemptCap ClaimTask/ReclaimExpired enforce, never a second
	// cap check. current_lane is never touched (abandoning is not a
	// verdict).
	AbandonClaim(ctx context.Context, params AbandonParams) (AbandonClaimResult, error)

	// RecordNote appends one `task_note` row (task_note.go, issue #2727,
	// FR11/FR12): a flat, immutable note against exactly one target --
	// params.TaskID, or params.EntityKind+params.EntityID naming a
	// spec-axis entity. Deliberately does NOT check current_claim_id --
	// any Agent may note, claimant or not (FR11); the only gate is NFR6's
	// session requirement, enforced by the caller's HTTP/MCP layer, not
	// here.
	RecordNote(ctx context.Context, params RecordNoteParams) (Note, error)

	// ListNotesForTask returns every note recorded against taskID, in
	// taskID's own scope, ordered by CreatedAt (task_note.go, issue
	// #2727) -- the list work.Assemble (#2721) populates TaskView.Notes
	// from.
	ListNotesForTask(ctx context.Context, scopeID, taskID uuid.UUID) ([]Note, error)

	// ListNotesForEntity returns every note recorded against the
	// spec-axis entity (kind, entityID), in scopeID's own scope, ordered
	// by CreatedAt (task_note.go, issue #2727).
	ListNotesForEntity(ctx context.Context, scopeID uuid.UUID, kind NoteEntityKind, entityID uuid.UUID) ([]Note, error)

	// ListClaimedTasks returns every currently-claimed task in
	// params.ScopeID (task_console.go, issue #2869, FR4), bounded and
	// continuable per params.Page (NFR6) -- M5's console query surface's
	// first query, whose paging machinery (paging.go) and
	// identifying-context join FR5's (#2875) and FR10's (#2873) queries
	// both reuse.
	ListClaimedTasks(ctx context.Context, params ListClaimedTasksParams) (Page[ClaimedTaskRow], error)

	// GetEscalationEventByID returns the EscalationEvent row for id
	// (task_escalation.go, issue #2868) -- the one caller today is
	// work.Assembler.Assemble, resolving the reason behind a task's own
	// current_escalation_id for the payload document (FR2, issue #2870).
	GetEscalationEventByID(ctx context.Context, id uuid.UUID) (EscalationEvent, error)

	// CancelTask is FR7's dead-letter terminal state (task_cancel.go,
	// issue #2873): a single transaction that row-locks the `task`,
	// refuses an already-cancelled task (ErrTaskAlreadyCancelled, nothing
	// written), force-closes any open claim (forceCloseClaimTx,
	// release_reason='cancel'), sets task.cancelled_at, and appends one
	// `task_intervention_event` row (action='cancel'). Works identically
	// on an escalated task -- cancel is the "terminate" half of the
	// recover-or-terminate pair FR6's requeue is the other half of.
	// current_lane and current_escalation_id are never touched (NFR5):
	// nothing already written is rewritten.
	CancelTask(ctx context.Context, params CancelTaskParams) (CancelResult, error)

	// ListCancelledTasks returns every cancelled task in params.ScopeID
	// (task_console.go, issue #2873, FR10), bounded and continuable per
	// params.Page (NFR6) -- reuses ListClaimedTasks' paging machinery
	// (paging.go) and identifying-context join shape rather than rolling
	// its own.
	ListCancelledTasks(ctx context.Context, params ListCancelledTasksParams) (Page[CancelledTaskRow], error)

	// TransitionNoteLifecycle appends one task_note_lifecycle_event row and
	// mirrors its Status onto task_note.current_status, in one transaction
	// (task_note_lifecycle.go, issue #2874, FR11). Open to any persona --
	// no claim or ownership check, mirroring RecordNote's own "any Agent,
	// claimant or not" posture. Never touches task_note.body/kind/task_id/
	// entity_kind/entity_id -- see Note's own doc comment (task_note.go).
	TransitionNoteLifecycle(ctx context.Context, params TransitionNoteLifecycleParams) (NoteLifecycleEvent, error)

	// ListOpenNotes returns every note in params.ScopeID whose
	// current_status is NoteLifecycleStatusNoted (task_note_console.go,
	// issue #2874, FR12), bounded and continuable per params.Page (NFR6) --
	// reuses ListClaimedTasks' own paging machinery and
	// identifying-context join style rather than forking a copy.
	ListOpenNotes(ctx context.Context, params ListOpenNotesParams) (Page[OpenNoteRow], error)

	// ReleaseLease is FR8's operator-initiated force-close
	// (task_release.go, issue #2872): a single transaction that refuses a
	// cancelled task (ErrTaskCancelled) and an unclaimed one
	// (ErrTaskNotClaimed), force-closes the open claim (forceCloseClaimTx,
	// release_reason='release'), appends one `released` task_attempt row,
	// increments attempt_count -- counting as an attempt against the same
	// DefaultAttemptCap ClaimTask/ReclaimExpired/AbandonClaim enforce
	// (#2851 Assumption 2) -- and, if that increment reaches the cap,
	// records the attempt-cap escalation (FR3, issue #2871) in this same
	// transaction. Appends one task_intervention_event row
	// (action='release'). current_lane is never touched.
	ReleaseLease(ctx context.Context, params ReleaseParams) (ReleaseResult, error)

	// EscalateTask is FR9's operator-initiated manual escalation
	// (task_escalate.go, issue #2872): a single transaction that refuses a
	// cancelled task (ErrTaskCancelled), records one 'manual'
	// task_escalation_event with NULL counter/cap (recordEscalationTx,
	// refusing an already-escalated task with ErrTaskEscalated -- see
	// task_escalate.go's own doc comment for why), force-closes any open
	// claim (forceCloseClaimTx, release_reason='escalate') and counts that
	// force-close as an attempt (one `force-closed` task_attempt row,
	// attempt_count+1) -- but never records a second escalation event even
	// when that increment reaches DefaultAttemptCap (FR9's stated
	// exception to FR8's "cap crossed => attempt-cap escalation" rule).
	// Appends one task_intervention_event row (action='escalate').
	EscalateTask(ctx context.Context, params EscalateParams) (EscalateResult, error)
}

// ErrMilestoneHasMilepebbleCut is CreateTask's named, loud rejection
// (FR1) for a MilestoneID that names a MilestoneKindMilestone row which
// already has one or more milepebbles cut from it -- the caller must
// scope the task to one of those milepebbles instead, never to the
// milestone directly, once a cut exists.
var ErrMilestoneHasMilepebbleCut = errors.New("krill/store: milestone has a milepebble cut; scope the task to a milepebble instead")

// errNotADeliveryTarget reports that MilestoneID names a milestone_ref
// row that is neither a milepebble nor an uncut milestone -- today this
// is only MilestoneKindBacklog (NFR7/LB6: a task's one delivery-axis
// reference is always a milestone or milepebble, never the backlog
// bucket, and never a Feature/Requirement id -- a Feature/Requirement id
// is rejected earlier, as an ErrNotFound, since it names no milestone_ref
// row at all).
func errNotADeliveryTarget(id uuid.UUID, kind string) error {
	return fmt.Errorf("%w: milestone_ref id %s has kind %q, not a milepebble or an uncut milestone", ErrNotFound, id, kind)
}

// taskStore is the pgx-backed TaskStore implementation.
type taskStore struct{ pool *pgxpool.Pool }

var _ TaskStore = taskStore{}

// taskColumns mirrors milestoneRefColumns' role for `task` -- the two-
// subject columns here are NOT NULL (NFR3, LB4; unlike milestone_ref's
// nullable pair), so scanTask reads them straight into Task's Subject
// fields, mirroring scanMilestoneDeferral's shape rather than
// scanMilestoneRef's sql.NullString one.
const taskColumns = `id, scope_id, milestone_id, title, body, lane_sequence, current_lane, ` +
	`attempt_count, current_claim_id, lease_expires_at, ` +
	`thrash_count, current_escalation_id, cancelled_at, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at`

func scanTask(row pgx.Row) (Task, error) {
	var t Task
	var laneSeq []string
	var currentLane string
	var actingKind, onBehalfOfKind string
	err := row.Scan(
		&t.ID, &t.ScopeID, &t.MilestoneID, &t.Title, &t.Body, &laneSeq, &currentLane,
		&t.AttemptCount, &t.CurrentClaimID, &t.LeaseExpiresAt,
		&t.ThrashCount, &t.CurrentEscalationID, &t.CancelledAt,
		&t.CreatedByActing.Iss, &t.CreatedByActing.Sub, &actingKind,
		&t.CreatedByOnBehalfOf.Iss, &t.CreatedByOnBehalfOf.Sub, &onBehalfOfKind,
		&t.CreatedAt,
	)
	if err != nil {
		return Task{}, err
	}
	t.LaneSequence = make([]Lane, len(laneSeq))
	for i, l := range laneSeq {
		t.LaneSequence[i] = Lane(l)
	}
	t.CurrentLane = Lane(currentLane)
	t.CreatedByActing.Kind = SubjectKind(actingKind)
	t.CreatedByOnBehalfOf.Kind = SubjectKind(onBehalfOfKind)
	return t, nil
}

func (s taskStore) CreateTask(ctx context.Context, params CreateTaskParams) (Task, error) {
	// Pure input validation first (FR1's lane-sequence rule), never
	// derived from a branch name or other external ref (NFR5) -- no DB
	// round trip is needed to reject a malformed lane sequence.
	if err := validateLaneSequence(params.LaneSequence, params.StartingLane); err != nil {
		return Task{}, err
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Task{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	// milestone_ref is plain (not SCD2, LB3), so its parentage is checked
	// with a direct existence lookup rather than currentRowExists --
	// mirrors MilestoneAuthoringStore.CreateMilepebble's own check. A
	// Feature or Requirement id (NFR7) simply names no row in
	// milestone_ref at all, so it is rejected right here as ErrNotFound,
	// the same path a stale or cross-scope milestone_id takes.
	var kind string
	err = tx.QueryRow(ctx, `
		SELECT kind FROM milestone_ref WHERE id = $1 AND scope_id = $2
	`, params.MilestoneID, params.ScopeID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, errParentNotFound("milestone_ref", params.MilestoneID)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get milestone_ref: %w", err)
	}

	switch MilestoneKind(kind) {
	case MilestoneKindMilepebble:
		// A milepebble is always a valid task scope (FR1) -- no further
		// check needed.
	case MilestoneKindMilestone:
		var hasMilepebbleCut bool
		err = tx.QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1 FROM milestone_ref WHERE parent_milestone_id = $1 AND scope_id = $2
			)
		`, params.MilestoneID, params.ScopeID).Scan(&hasMilepebbleCut)
		if err != nil {
			return Task{}, fmt.Errorf("check milepebble cut: %w", err)
		}
		if hasMilepebbleCut {
			return Task{}, fmt.Errorf("%w: milestone_ref id %s", ErrMilestoneHasMilepebbleCut, params.MilestoneID)
		}
	default:
		return Task{}, errNotADeliveryTarget(params.MilestoneID, kind)
	}

	laneSeq := make([]string, len(params.LaneSequence))
	for i, l := range params.LaneSequence {
		laneSeq[i] = string(l)
	}

	task, err := scanTask(tx.QueryRow(ctx, `
		INSERT INTO task (
			scope_id, milestone_id, title, body, lane_sequence, current_lane,
			created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
			created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		RETURNING `+taskColumns,
		params.ScopeID, params.MilestoneID, params.Title, params.Body, laneSeq, string(params.StartingLane),
		params.Acting.Iss, params.Acting.Sub, string(params.Acting.Kind),
		params.OnBehalfOf.Iss, params.OnBehalfOf.Sub, string(params.OnBehalfOf.Kind)))
	if err != nil {
		return Task{}, fmt.Errorf("insert task: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Task{}, fmt.Errorf("commit: %w", err)
	}
	return task, nil
}

func (s taskStore) GetTaskByID(ctx context.Context, id uuid.UUID) (Task, error) {
	task, err := scanTask(s.pool.QueryRow(ctx, `
		SELECT `+taskColumns+`
		FROM task
		WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Task{}, errParentNotFound("task", id)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task: %w", err)
	}
	return task, nil
}
