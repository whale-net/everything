// Package work is M4's work-axis task payload document (FR4, FR10, issue
// #2721): the one typed, self-describing Payload every later M4 task
// extends additively -- #2722 adds claim/lease state, #2727 adds the note
// list. Assembler.Assemble builds it by ENRICHING M1's C3 scoped-slice
// document (//krill/slice), never independently re-deriving spec-axis
// content: this package must never issue its own SELECT against `feature`,
// `requirement`, `load_bearing_decision`, or any other spec-axis table --
// the slice always arrives through slice.Querier, the same FR5-FR9 path
// GET /products/{id}/slice uses (NFR4, LB7).
//
// There is exactly one payload type in this repo. GET /tasks/{id}
// (krill/api/handlers/task_payload.go) and the MCP tool get_task
// (krill/mcp/tools/task_payload.go) both return this exact Payload; #2722's
// claim verb will too. A second, per-verb response shape is the drift
// LB7/NFR4 exist to prevent.
package work

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// SchemaVersion versions only the work-axis wrapper this package adds
// around slice.Document -- NOT a second envelope. slice.Document's own
// SchemaVersion (krill/slice/document.go) is the spec-axis envelope and is
// passed through untouched inside Payload.Slice. Bump this constant only
// when an existing field of Payload/TaskView is removed, renamed, or
// repurposed; purely additive fields (#2722's claim/lease state, #2727's
// note list) do not require a bump, exactly as slice.SchemaVersion
// documents for its own constant.
const SchemaVersion = "1"

// TaskDep is one entry of TaskView's declared dependency list (FR2, issue
// #2720): DependsOnTaskID is the id of a task this task depends on, in
// store.TaskStore.ListDependencies' declaration order. Named
// DependsOnTaskID rather than a bare TaskID -- which store.TaskDependency
// already uses for the *owning* task -- so a Dependencies entry is never
// misread as the payload's own Task.ID.
type TaskDep struct {
	DependsOnTaskID uuid.UUID `json:"depends_on_task_id"`
}

// TaskView is Payload's work-axis half: the task's own fields plus its
// declared dependency list, alongside the spec slice Payload.Slice embeds.
// Lease/claim state is added by #2722; Notes is added by #2727 -- both
// additive, per this package's own doc comment.
type TaskView struct {
	ID            uuid.UUID `json:"id"`
	MilestoneID   uuid.UUID `json:"milestone_id"`
	Title         string    `json:"title"`
	Body          string    `json:"body"`
	CurrentLane   string    `json:"current_lane"`
	LaneSequence  []string  `json:"lane_sequence"`
	Dependencies  []TaskDep `json:"dependencies"`
	AttemptNumber int       `json:"attempt_number"`
}

// Payload is the one typed, self-describing task payload document (FR4,
// FR10, LB7) -- see this package's own doc comment.
type Payload struct {
	// Slice is M1's C3 document verbatim -- same schema_version envelope,
	// same as-of revision fields (slice.EntityRef). Embedded, never copied
	// field-by-field into a parallel shape (NFR4/LB7).
	Slice slice.Document `json:"slice"`

	Task TaskView `json:"task"`
}

// Assembler builds Payload values from krill/store and krill/slice --
// mirrors slice.Querier's own shape (a thin struct wrapping the store it
// reads from) so the later MCP tool and HTTP handler (LB7) have one call
// to wrap rather than a bespoke join each.
type Assembler struct {
	tasks   store.TaskStore
	querier *slice.Querier
}

// NewAssembler returns an Assembler backed by tasks and querier.
func NewAssembler(tasks store.TaskStore, querier *slice.Querier) *Assembler {
	return &Assembler{tasks: tasks, querier: querier}
}

// Assemble builds taskID's Payload (FR4, FR10):
//  1. Load the `task` row (scope-qualified) -- store.TaskStore.
//     GetTaskByID takes no scope argument (task ids are globally unique
//     surrogates), so a scopeID mismatch is checked explicitly below and
//     rejected identically to an unknown taskID (NFR1): a cross-scope
//     task id must never leak another scope's payload.
//  2. Resolve the task's delivery reference (milestone_id) to the spec
//     slice it implies, by calling slice.Querier.GetMilestoneDeliversSlice
//     -- never by issuing its own SELECTs against any spec-axis table
//     (NFR4). Works identically whether milestone_id names a milestone or
//     a milepebble (both are `milestone_ref` rows).
//  3. Attach the work-axis fields: task id, current lane, lane sequence,
//     dependency list (store.TaskStore.ListDependencies), attempt number.
func (a *Assembler) Assemble(ctx context.Context, scopeID, taskID uuid.UUID) (Payload, error) {
	task, err := a.tasks.GetTaskByID(ctx, taskID)
	if err != nil {
		return Payload{}, fmt.Errorf("get task: %w", err)
	}
	if task.ScopeID != scopeID {
		// A cross-scope taskID is rejected identically to an unknown one
		// (NFR1) -- never leaks whether taskID belongs to another scope,
		// mirroring krill/api/handlers/pointer.go's own cross-scope check.
		return Payload{}, fmt.Errorf("%w: task id %s", store.ErrNotFound, taskID)
	}

	sliceDoc, err := a.querier.GetMilestoneDeliversSlice(ctx, task.MilestoneID)
	if err != nil {
		return Payload{}, fmt.Errorf("get milestone delivers slice: %w", err)
	}

	deps, err := a.tasks.ListDependencies(ctx, scopeID, taskID)
	if err != nil {
		return Payload{}, fmt.Errorf("list task dependencies: %w", err)
	}
	// Always a non-nil slice, even when deps is empty -- FR4's dependency
	// list must marshal as `[]`, never `null` (Dependencies carries no
	// `omitempty`).
	taskDeps := make([]TaskDep, len(deps))
	for i, d := range deps {
		taskDeps[i] = TaskDep{DependsOnTaskID: d.DependsOnTaskID}
	}

	laneSequence := make([]string, len(task.LaneSequence))
	for i, lane := range task.LaneSequence {
		laneSequence[i] = string(lane)
	}

	var body string
	if task.Body != nil {
		body = *task.Body
	}

	return Payload{
		Slice: sliceDoc,
		Task: TaskView{
			ID:            task.ID,
			MilestoneID:   task.MilestoneID,
			Title:         task.Title,
			Body:          body,
			CurrentLane:   string(task.CurrentLane),
			LaneSequence:  laneSequence,
			Dependencies:  taskDeps,
			AttemptNumber: task.AttemptCount,
		},
	}, nil
}
