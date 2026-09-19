// This file (issue #2719, FR1, C14) is TaskStore -- the work-axis store
// surface migration 015 creates. It ships one method this issue's
// Implementation phase fills in, CreateTask, and is the interface every
// later M4 task (#2720 dependency declaration, #2722-#2726 claim/lease/
// attempt, #2727 notes) widens with its own methods rather than
// introducing a sibling accessor -- see #2720's issue body: "Store API on
// store.TaskStore (or a sibling TaskDependencyStore reachable from the
// same entities accessor)" settles on the former. This mirrors
// MilestoneAuthoringStore's own incremental growth (milestone_authoring.go
// gained CreateMilepebble/AddMilepebbleDelivers/AddDiscoveredScope across
// several M3 tasks without ever splitting into a second interface).
//
// Method bodies are scaffold stubs; the Implementation phase of issue
// #2719 fills in CreateTask against this same interface.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
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
}

// taskStore is the pgx-backed TaskStore implementation.
type taskStore struct{ pool *pgxpool.Pool }

var _ TaskStore = taskStore{}

func (s taskStore) CreateTask(ctx context.Context, params CreateTaskParams) (Task, error) {
	return Task{}, fmt.Errorf("store: TaskStore.CreateTask not implemented -- see issue #2719's Implementation phase")
}
