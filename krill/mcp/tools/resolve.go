// This file is krill's resolve MCP tool group: resolve_non_goal, which
// settles a `deferred` Non-Goal, plus list_non_goal_promotions, its audit
// read. It mirrors the HTTP surface in krill/api/handlers/resolve.go (LB7).
//
// It is a tool of its own rather than a flag on amend_entity because amend
// forbids re-kinding (FR f0f6bc18) -- that guarantee is what lets a reader
// trust an amended row's `kind` to be the row's original kind, so widening
// amend to carry a new kind would trade one real guarantee for a
// convenience. Promote needs the re-kind, so promote needs a verb.
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

// resolveOutcomes is the closed set of outcome values resolve_non_goal
// accepts, in the order the tool description lists them. There is no
// default: "settle it" and "settle it how" are different questions, and
// guessing the second would silently pick between a row that survives and
// a row that is tombstoned.
var resolveOutcomes = []store.ResolveOutcome{store.ResolvePromote, store.ResolveRetire}

// resolveNonGoalInput is resolve_non_goal's argument schema. The krill
// session id travels as an ordinary input field (krillSessionInput) -- the
// LB4 subject pair and scope_id are resolved from it, never from a
// caller-supplied field.
type resolveNonGoalInput struct {
	krillSessionInput
	NonGoalID string `json:"non_goal_id" jsonschema:"The surrogate id (LB2) of the deferred Non-Goal to resolve, as a UUID string."`
	Outcome   string `json:"outcome" jsonschema:"How to settle it: 'promote' re-kinds it to 'permanent' and keeps it (close + successor, body and name preserved); 'retire' tombstones it (close with no successor, name freed). No other value is accepted."`
	Reason    *string `json:"reason,omitempty" jsonschema:"Optional free-text reason, recorded in the resolution's history for the audit reads."`
}

// listNonGoalPromotionsInput is list_non_goal_promotions's argument
// schema. Like list_products and list_void_events, it takes scope_id
// explicitly rather than deriving it from a session: it is a read, and
// every read tool on this mount is ungated.
type listNonGoalPromotionsInput struct {
	ScopeID   string `json:"scope_id" jsonschema:"The scope to list Non-Goal promotions for, as a UUID string. Use init_session's scope_id, or list_products to discover one."`
	ProductID string `json:"product_id,omitempty" jsonschema:"Narrow to one product. Omit for every product in the scope."`
}

// resolvePersonas matches amend's and void's: resolve is the Requirement
// Contributor's path for a decision that was deliberately held back at
// authoring time.
var resolvePersonas = []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}

// RegisterResolveNonGoal registers resolve_non_goal (FR d0021a0f): settles
// a `deferred` Non-Goal by exactly one of two operations, PROMOTE (close
// + successor, re-kinded to `permanent`, body and name preserved) or
// RETIRE (close with no successor, tombstoned, name freed). A `permanent`
// Non-Goal is refused by either -- it is the terminal state of both, so
// there is nothing left to settle.
//
// This verb is reachable only here and never through amend_entity, which is
// the point: the re-kind is the whole operation, and amend's contract is
// that it never performs one.
func RegisterResolveNonGoal(reg *server.Registry, sessions store.SessionStore, resolve store.ResolveStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "resolve_non_goal",
		Description: "Resolve a `deferred` Non-Goal -- one held back as 'explicitly not a non-goal' -- by exactly one of two operations. " +
			"'promote' closes the current row and opens a successor under the SAME id with kind 'permanent', body and name preserved, so a " +
			"citation already rendered for it still resolves. 'retire' closes the current row and opens NO successor: the row is tombstoned, " +
			"leaves every current read and the rendered brief, and its name is freed for a later create. " +
			"A `permanent` Non-Goal is refused by both -- it is already settled. " +
			"Do NOT reach for this to reword a Non-Goal: that is amend_entity, which deliberately cannot change an entity's kind. " +
			"Both outcomes are recorded with their actor and timestamp; read the promote side with list_non_goal_promotions and the " +
			"retire side with list_void_events.",
	}, resolvePersonas, func(ctx context.Context, _ *mcp.CallToolRequest, in resolveNonGoalInput) (*mcp.CallToolResult, any, error) {
		var zero any

		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}
		id, err := uuid.Parse(in.NonGoalID)
		if err != nil {
			return nil, zero, fmt.Errorf("non_goal_id: invalid or missing UUID")
		}
		outcome := store.ResolveOutcome(in.Outcome)
		if outcome != store.ResolvePromote && outcome != store.ResolveRetire {
			return nil, zero, fmt.Errorf("outcome: must be one of %v", resolveOutcomes)
		}

		resolved, err := resolve.ResolveNonGoal(ctx, sess.ScopeID, id, outcome, in.Reason, sess.Acting, sess.OnBehalfOf)
		if err != nil {
			return nil, zero, err
		}
		// A retire leaves no current row, so the id is the whole result.
		if outcome == store.ResolveRetire {
			return nil, handlers.IDResponse{ID: id.String()}, nil
		}
		return nil, handlers.NewNonGoalWire(resolved), nil
	})
}

// RegisterListNonGoalPromotions registers list_non_goal_promotions: the
// audit read (FR 19123858) for the PROMOTE outcome. A promote leaves a
// current row, so the SCD2 table records only that `kind` changed -- not
// that it changed by resolution rather than by a create that reused the id.
// This is how a caller recovers that fact. Ungated, like every other read
// on this mount.
func RegisterListNonGoalPromotions(reg *server.Registry, resolve store.ResolveStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name: "list_non_goal_promotions",
		Description: "Audit read for the promote outcome: every Non-Goal promoted from `deferred` to `permanent` in a scope, oldest first, " +
			"optionally narrowed to one product. Each entry carries the Non-Goal's ORIGINAL surrogate id, the kinds it moved between, and " +
			"its actor and timestamp. This is the only way to tell a resolution from a plain kind change, because the SCD2 row records only " +
			"that `kind` changed. The retire outcome is audited through list_void_events instead, narrowed to non_goal.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listNonGoalPromotionsInput) (*mcp.CallToolResult, handlers.ListNonGoalPromotionsResponse, error) {
		var zero handlers.ListNonGoalPromotionsResponse

		scopeID, err := uuid.Parse(in.ScopeID)
		if err != nil {
			return nil, zero, fmt.Errorf("scope_id: invalid or missing UUID")
		}
		var productID *uuid.UUID
		if in.ProductID != "" {
			parsed, err := uuid.Parse(in.ProductID)
			if err != nil {
				return nil, zero, fmt.Errorf("product_id: invalid or missing UUID")
			}
			productID = &parsed
		}

		promotions, err := resolve.ListNonGoalPromotions(ctx, scopeID, productID)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.NewListNonGoalPromotionsResponse(promotions), nil
	})
}

// RegisterResolveAll registers every resolve tool this milestone exposes.
func RegisterResolveAll(reg *server.Registry, sessions store.SessionStore, resolve store.ResolveStore) {
	RegisterResolveNonGoal(reg, sessions, resolve)
	RegisterListNonGoalPromotions(reg, resolve)
}
