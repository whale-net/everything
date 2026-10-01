// list_entity_notes: the read-back path for notes record_note attaches to a
// spec-axis entity, a thin wrapper over handlers.ListEntityNotes (the same
// function GET /{kind}/{id}/notes serves, LB7). Ungated like every other
// read tool -- no krillSessionInput, and scope comes from the entity itself.
package tools

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// listEntityNotesInput is list_entity_notes' argument schema: the same
// entity_kind/entity_id pair record_note accepts.
type listEntityNotesInput struct {
	EntityKind string `json:"entity_kind" jsonschema:"The spec-axis entity kind: one of product, feature_set, feature, requirement, load_bearing_decision."`
	EntityID   string `json:"entity_id" jsonschema:"The entity's surrogate id, as a UUID string."`
	Kind       string `json:"kind,omitempty" jsonschema:"Optional note kind filter (scope-note or comment)."`
	Status     string `json:"status,omitempty" jsonschema:"Optional lifecycle status filter (noted, carried-over, deferred, closed)."`
	BodyPrefix string `json:"body_prefix,omitempty" jsonschema:"Optional case-sensitive prefix the note body must start with."`
	Limit      int    `json:"limit,omitempty" jsonschema:"Optional maximum number of notes to return."`
}

// RegisterListEntityNotes registers list_entity_notes: every note recorded
// against one spec-axis entity, oldest first, regardless of lifecycle status.
func RegisterListEntityNotes(reg *server.Registry, scopes handlers.NoteEntityScopes, tasks store.TaskStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_entity_notes",
		Description: "List every note recorded (via record_note) against a spec-axis entity -- product, feature_set, feature, requirement, or load_bearing_decision -- oldest first (newest first when kind, status, body_prefix or limit is given), with each note's kind, body, and current lifecycle status (any status, not just noted). Use get_task for notes on a task.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listEntityNotesInput) (*mcp.CallToolResult, handlers.ListEntityNotesResponse, error) {
		var zero handlers.ListEntityNotesResponse

		entityID, err := uuid.Parse(in.EntityID)
		if err != nil {
			return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
		}

		kind := store.NoteEntityKind(in.EntityKind)
		resp, err := handlers.ListEntityNotes(ctx, scopes, tasks, kind, entityID, handlers.EntityNoteFilter{Kind: in.Kind, Status: in.Status, BodyPrefix: in.BodyPrefix, Limit: in.Limit})
		if errors.Is(err, store.ErrNotFound) {
			return nil, zero, fmt.Errorf("%s %s not found", kind, entityID, handlers.EntityNoteFilter{Kind: in.Kind, Status: in.Status, BodyPrefix: in.BodyPrefix, Limit: in.Limit})
		}
		if err != nil {
			return nil, zero, err
		}
		return nil, resp, nil
	})
}
