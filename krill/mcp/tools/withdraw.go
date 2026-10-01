package tools

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// withdrawEdgeInput is the argument schema shared by both withdraw tools.
type withdrawEdgeInput struct {
	krillSessionInput
	MilestoneID string `json:"milestone_id" jsonschema:"The milestone or milepebble surrogate id the edge hangs off, as a UUID string."`
	EntityID    string `json:"entity_id" jsonschema:"The entity surrogate id the edge points at, as a UUID string."`
	Reason      string `json:"reason,omitempty" jsonschema:"Optional free-text reason, recorded with the withdrawal."`
}

// withdrawEdgeOutput echoes the withdrawn edge.
type withdrawEdgeOutput struct {
	MilestoneID string `json:"milestone_id"`
	EntityID    string `json:"entity_id"`
}

// RegisterWithdrawAll registers withdraw_delivers and withdraw_must_not_foreclose.
func RegisterWithdrawAll(reg *server.Registry, sessions store.SessionStore, w store.WithdrawalStore) {
	registerWithdraw(reg, sessions, "withdraw_must_not_foreclose",
		"Withdraw a wrong must-not-foreclose edge from a milestone. Errors, writing nothing, if no active edge exists. "+
			"To remove a mistaken entity itself, use void_entity instead.",
		w.WithdrawMustNotForeclose)
	registerWithdraw(reg, sessions, "withdraw_delivers",
		"Withdraw a wrong delivers edge from a milestone or milepebble. Refuses, writing nothing, if the item already shipped there, "+
			"if no active edge exists, or (milestone level) while an active milepebble edge for the entity exists under it. "+
			"The row is kept; re-adding creates a fresh active edge. To remove a mistaken entity itself, use void_entity instead.",
		w.WithdrawDelivers)
}

type withdrawFn func(ctx context.Context, scopeID, milestoneID, entityID uuid.UUID, reason string, acting, onBehalfOf store.Subject) error

func registerWithdraw(reg *server.Registry, sessions store.SessionStore, name, desc string, fn withdrawFn) {
	server.RegisterWrite(reg, &mcp.Tool{Name: name, Description: desc}, voidPersonas,
		func(ctx context.Context, _ *mcp.CallToolRequest, in withdrawEdgeInput) (*mcp.CallToolResult, withdrawEdgeOutput, error) {
			var zero withdrawEdgeOutput
			sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
			if err != nil {
				return nil, zero, err
			}
			mid, err := uuid.Parse(in.MilestoneID)
			if err != nil {
				return nil, zero, fmt.Errorf("milestone_id: invalid or missing UUID")
			}
			eid, err := uuid.Parse(in.EntityID)
			if err != nil {
				return nil, zero, fmt.Errorf("entity_id: invalid or missing UUID")
			}
			if err := fn(ctx, sess.ScopeID, mid, eid, in.Reason, sess.Acting, sess.OnBehalfOf); err != nil {
				return nil, zero, err
			}
			return nil, withdrawEdgeOutput{MilestoneID: in.MilestoneID, EntityID: in.EntityID}, nil
		})
}
