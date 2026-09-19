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
	"errors"

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
// #2720): the id of a task this task depends on, in
// store.TaskStore.ListDependencies' declaration order.
type TaskDep struct {
	TaskID uuid.UUID `json:"task_id"`
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

// ErrNotImplemented is Assembler.Assemble's Scaffold-phase placeholder
// return (issue #2721) -- real assembly logic (resolving the task's
// milestone_id to its spec slice via slice.Querier, then attaching the
// work-axis fields) lands in this task's Implementation phase.
var ErrNotImplemented = errors.New("krill/work: Assemble not implemented yet")

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
//  1. Load the `task` row (scope-qualified).
//  2. Resolve the task's delivery reference (milestone_id) to the spec
//     slice it implies, and obtain that slice by calling slice.Querier --
//     never by issuing its own SELECTs against any spec-axis table.
//  3. Attach the work-axis fields: task id, current lane, lane sequence,
//     dependency list (store.TaskStore.ListDependencies), attempt number.
//
// Scaffold-phase stub: returns ErrNotImplemented unconditionally. This
// task's Implementation phase fills in the three steps above.
func (a *Assembler) Assemble(ctx context.Context, scopeID, taskID uuid.UUID) (Payload, error) {
	return Payload{}, ErrNotImplemented
}
