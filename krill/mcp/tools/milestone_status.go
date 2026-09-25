// This file (issue #2685, FR8, FR9, FR12) is krill's milestone/milepebble
// status MCP tool group: thin wrappers over store.MilestoneStatusEventStore
// -- set_milestone_status (write, Requirement Contributor and Agent
// personas) and get_milestone_status/get_milestone_status_history (reads)
// -- mirroring krill/api/handlers/milestone_status.go's HTTP surface for
// the same capability (LB7), via the same handlers.IDResponse/
// handlers.MilestoneStatusResponse/handlers.MilestoneStatusHistoryResponse/
// handlers.ToMilestoneStatusEventWire this package's other write/read
// tools reuse. Registered from RegisterMilestoneStatusAll (below) onto the
// design mount (../main.go's designReg), same as milestone.go's tools:
// set_milestone_status attributes the LB4 subject pair from a resolved
// krill session (krillSessionInput, requireKrillSession -- design.go),
// never from a caller-supplied field.
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

// setMilestoneStatusInput is set_milestone_status's argument schema (FR8,
// FR9). Note is optional.
type setMilestoneStatusInput struct {
	krillSessionInput
	MilestoneID string  `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id, as a UUID string."`
	Status      string  `json:"status" jsonschema:"One of: not started, in design, designed, planned, in progress, shipped, partially complete, abandoned (FR8). Only the transitions handlers' edge table allows are accepted; the error names the legal alternatives."`
	Note        *string `json:"note" jsonschema:"Optional free-text note attached to this transition."`
}

// RegisterSetMilestoneStatus registers set_milestone_status (FR8, FR9,
// issue #2963): appends a new status transition via
// handlers.ApplyMilestoneStatus -- the same write path the HTTP surface
// uses, so the transition edge table and the no-op-on-self-transition
// rule cannot be enforced here and skipped there (LB7). Re-affirming the
// status a container already holds is a no-op that writes no history row.
func RegisterSetMilestoneStatus(reg *server.Registry, sessions store.SessionStore, statuses store.MilestoneStatusEventStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "set_milestone_status",
		Description: "Record a new status transition for a milestone or milepebble (FR8, FR9) -- appended to history, never an overwrite (NFR2). Only transitions the status edge table allows are accepted; re-setting the status already held writes nothing.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in setMilestoneStatusInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		status := store.MilestoneStatus(in.Status)
		if _, ok := handlers.ValidMilestoneStatuses[status]; !ok {
			return nil, zero, fmt.Errorf("status: must be one of the fixed FR8 values, got %q", in.Status)
		}

		event, _, err := handlers.ApplyMilestoneStatus(ctx, statuses, sess.ScopeID, milestoneID, status, in.Note, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		if event.ID == uuid.Nil {
			return nil, handlers.IDResponse{ID: ""}, nil
		}
		return nil, handlers.IDResponse{ID: event.ID.String()}, nil
	})
}

// milestoneStatusIDInput is get_milestone_status's and
// get_milestone_status_history's shared argument schema: a single
// milestone or milepebble surrogate id.
type milestoneStatusIDInput struct {
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id, as a UUID string."`
}

// RegisterGetMilestoneStatus registers get_milestone_status (FR8, FR9):
// the current, derived status via
// store.MilestoneStatusEventStore.CurrentStatus -- mirrors
// GetMilestoneStatusHandler, via the same handlers.MilestoneStatusResponse
// shape (LB7).
func RegisterGetMilestoneStatus(reg *server.Registry, statuses store.MilestoneStatusEventStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_milestone_status",
		Description: "Return a milestone or milepebble's current status, derived from its transition history (FR8).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneStatusIDInput) (*mcp.CallToolResult, handlers.MilestoneStatusResponse, error) {
		var zero handlers.MilestoneStatusResponse

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		status, err := statuses.CurrentStatus(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.MilestoneStatusResponse{Status: string(status)}, nil
	})
}

// RegisterGetMilestoneStatusHistory registers get_milestone_status_history
// (FR12): the full transition history in chronological order via
// store.MilestoneStatusEventStore.ListTransitions -- mirrors
// GetMilestoneStatusHistoryHandler, via the same
// handlers.MilestoneStatusHistoryResponse/handlers.ToMilestoneStatusEventWire
// shapes (LB7).
func RegisterGetMilestoneStatusHistory(reg *server.Registry, statuses store.MilestoneStatusEventStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_milestone_status_history",
		Description: "Return a milestone or milepebble's full status transition history, oldest first, each entry with its actor and timestamp (FR12).",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in milestoneStatusIDInput) (*mcp.CallToolResult, handlers.MilestoneStatusHistoryResponse, error) {
		var zero handlers.MilestoneStatusHistoryResponse

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		events, err := statuses.ListTransitions(ctx, milestoneID)
		if err != nil {
			return nil, zero, err
		}

		wires := make([]handlers.MilestoneStatusEventWire, len(events))
		for i, e := range events {
			wires[i] = handlers.ToMilestoneStatusEventWire(e)
		}
		return nil, handlers.MilestoneStatusHistoryResponse{Transitions: wires}, nil
	})
}

// RegisterMilestoneStatusAll registers every milestone/milepebble status
// tool this milestone exposes against reg -- set_milestone_status (write)
// and get_milestone_status/get_milestone_status_history (read).
func RegisterMilestoneStatusAll(reg *server.Registry, sessions store.SessionStore, statuses store.MilestoneStatusEventStore) {
	RegisterSetMilestoneStatus(reg, sessions, statuses)
	RegisterGetMilestoneStatus(reg, statuses)
	RegisterGetMilestoneStatusHistory(reg, statuses)
}
