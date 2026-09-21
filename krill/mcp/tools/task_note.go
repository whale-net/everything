// This file (issue #2727, FR11, FR12, C25) is krill's work-axis note-
// recording MCP tool: record_note, a thin wrapper over
// store.TaskStore.RecordNote (krill/store/task_note.go), mirroring
// krill/api/handlers/task_note.go's HTTP surface for the same capability
// (LB7). Restricted to PersonaAgent -- root plan issue #2717's Personas
// section states the Agent "can record a note against a task or spec
// entity regardless of whether it holds the claim" (FR11); this milestone's
// Swarm Operator persona is not named against FR11 at all.
//
// transition_note_lifecycle (issue #2874, root plan #2851, FR11) is the
// sibling write tool over store.TaskStore.TransitionNoteLifecycle,
// mirroring krill/api/handlers/task_note_lifecycle.go's HTTP surface.
// Deliberately open to any resolved persona (nil allowedPersonas) -- FR11
// says "any persona" may transition a note's lifecycle, unlike record_note
// above's PersonaAgent-only restriction.
package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// recordNoteInput is record_note's argument schema (FR11): exactly one of
// TaskID or EntityKind+EntityID must be set, Kind must be one of
// store.NoteKind's fixed enumeration ("scope-note" or "comment"), and Body
// is required. Mirrors recordNoteRequest (api/handlers/task_note.go).
type recordNoteInput struct {
	krillSessionInput
	TaskID     *string `json:"task_id,omitempty" jsonschema:"The task this note targets, as a UUID string -- set this OR entity_kind+entity_id, never both, never neither."`
	EntityKind *string `json:"entity_kind,omitempty" jsonschema:"The spec-axis entity kind this note targets: one of product, feature_set, feature, requirement, load_bearing_decision. Set together with entity_id, never with task_id."`
	EntityID   *string `json:"entity_id,omitempty" jsonschema:"The spec-axis entity's surrogate id, as a UUID string. Set together with entity_kind, never with task_id."`
	Kind       string  `json:"kind" jsonschema:"The note's kind -- one of the fixed enumeration: scope-note (models GitHub's source:scope-note convention) or comment."`
	Body       string  `json:"body" jsonschema:"The note's free-form text body."`
}

// RegisterRecordNote registers record_note (FR11): appends a flat,
// immutable `task_note` row via store.TaskStore.RecordNote, mirroring
// krill/api/handlers/task_note.go's RecordNoteHandler for the same
// capability (LB7). Never checks whether the caller's session holds the
// target task's current claim -- any Agent may note, claimant or not.
func RegisterRecordNote(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "record_note",
		Description: "Record a flat, immutable note against a task or a spec-axis entity (FR11). Any Agent may call this, whether or not it holds the target task's current claim -- the only gate is an active krill session.",
	}, []server.Persona{server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in recordNoteInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		params := store.RecordNoteParams{
			ScopeID:    sess.ScopeID,
			Kind:       store.NoteKind(in.Kind),
			Body:       in.Body,
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		}

		if in.TaskID != nil {
			taskID, err := uuid.Parse(*in.TaskID)
			if err != nil {
				return nil, zero, fmt.Errorf("task_id: invalid or missing UUID")
			}
			params.TaskID = &taskID
		}
		if in.EntityKind != nil {
			kind := store.NoteEntityKind(*in.EntityKind)
			params.EntityKind = &kind
		}
		if in.EntityID != nil {
			entityID, err := uuid.Parse(*in.EntityID)
			if err != nil {
				return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
			}
			params.EntityID = &entityID
		}

		note, err := tasks.RecordNote(ctx, params)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: note.ID.String()}, nil
	})
}

// transitionNoteLifecycleInput is transition_note_lifecycle's argument
// schema (FR11): the target note id and the destination Status.
type transitionNoteLifecycleInput struct {
	krillSessionInput
	NoteID string `json:"note_id" jsonschema:"The note this transitions, as a UUID string."`
	Status string `json:"status" jsonschema:"The destination lifecycle status -- one of the fixed enumeration: noted, carried-over, deferred, closed."`
}

// RegisterTransitionNoteLifecycle registers transition_note_lifecycle
// (FR11): appends one task_note_lifecycle_event row and mirrors it onto
// the note's current_status via store.TaskStore.TransitionNoteLifecycle,
// mirroring krill/api/handlers/task_note_lifecycle.go's
// TransitionNoteLifecycleHandler for the same capability. Open to any
// resolved persona (nil allowedPersonas) -- see this file's own doc
// comment.
func RegisterTransitionNoteLifecycle(reg *server.Registry, sessions store.SessionStore, tasks store.TaskStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "transition_note_lifecycle",
		Description: "Transition a note's lifecycle status (FR11): noted -> carried-over/deferred/closed, or any other value in that fixed enumeration. Any persona may call this -- the only gate is an active krill session. The note's body and kind are never touched.",
	}, nil, func(ctx context.Context, _ *mcp.CallToolRequest, in transitionNoteLifecycleInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		noteID, err := uuid.Parse(in.NoteID)
		if err != nil {
			return nil, zero, fmt.Errorf("note_id: invalid or missing UUID")
		}

		event, err := tasks.TransitionNoteLifecycle(ctx, store.TransitionNoteLifecycleParams{
			ScopeID:    sess.ScopeID,
			NoteID:     noteID,
			Status:     store.NoteLifecycleStatus(in.Status),
			Acting:     sess.Acting,
			OnBehalfOf: sess.OnBehalfOf,
		})
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: event.ID.String()}, nil
	})
}
