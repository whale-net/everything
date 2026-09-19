// This file (issue #2688, FR6) is krill's abandon MCP tool: a thin
// wrapper over store.AbandonStore.Abandon -- abandon_milestone (write,
// Requirement Contributor persona) -- mirroring
// krill/api/handlers/abandon.go's HTTP surface for the same capability
// (LB7), via the same handlers.AbandonResponse/handlers.ToAbandonResponse
// shapes this package's other write tools reuse. Registered from
// RegisterAbandonAll (below) onto the design mount (../main.go's
// designReg), same as milestone_status.go's and recut.go's tools:
// abandon_milestone attributes the LB4 subject pair from a resolved
// krill session (krillSessionInput, requireKrillSession -- design.go),
// never from a caller-supplied field. Registered for both
// PersonaRequirementContributor (FR6's named persona -- "Lets a
// Requirement Contributor mark...") and PersonaAgent, mirroring every
// other write tool in this package: PersonaRequirementContributor has no
// real front door yet (server/auth.go's own note), so PersonaAgent is
// what makes this tool reachable at all until C12 lands.
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

// abandonMilestoneInput is abandon_milestone's argument schema (FR6): the
// milestone or milepebble to abandon, and an optional note.
type abandonMilestoneInput struct {
	krillSessionInput
	MilestoneID string  `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id to abandon, as a UUID string."`
	Note        *string `json:"note" jsonschema:"Optional free-text note attached to the abandoned status transition."`
}

// RegisterAbandonMilestone registers abandon_milestone (FR6): appends an
// `abandoned` status transition and sweeps the container's not-yet-shipped
// scope into its product's backlog bucket, in one transaction, via
// store.AbandonStore.Abandon. Abandoning a milestone also abandons every
// live milepebble cut from it (store.AbandonStore's own cascade doc
// comment); abandoning an already-abandoned container, or the backlog
// bucket itself, is rejected loudly and writes nothing.
//
// Irreversible: there is no un-abandon tool. Shipped scope is left exactly
// where it is (NFR3) -- reviving a commitment means re-cutting its backlog
// scope into a new container via move_delivery_scope (recut.go).
func RegisterAbandonMilestone(reg *server.Registry, sessions store.SessionStore, abandons store.AbandonStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "abandon_milestone",
		Description: "Abandon a stalled milestone or milepebble (FR6): appends an 'abandoned' status transition and sweeps its not-yet-shipped scope into the backlog bucket, atomically. Irreversible -- there is no un-abandon tool -- and never alters what already shipped (NFR3).",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent}, func(ctx context.Context, _ *mcp.CallToolRequest, in abandonMilestoneInput) (*mcp.CallToolResult, handlers.AbandonResponse, error) {
		var zero handlers.AbandonResponse

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}

		milestoneID, err := uuid.Parse(in.MilestoneID)
		if err != nil {
			return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
		}

		result, err := abandons.Abandon(ctx, sess.ScopeID, milestoneID, in.Note, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.ToAbandonResponse(result), nil
	})
}

// RegisterAbandonAll registers every abandon tool this milestone exposes
// against reg -- abandon_milestone (write).
func RegisterAbandonAll(reg *server.Registry, sessions store.SessionStore, abandons store.AbandonStore) {
	RegisterAbandonMilestone(reg, sessions, abandons)
}
