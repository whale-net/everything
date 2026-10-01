// This file is krill's LB-protects-Feature tool group: add_lb_protects and
// withdraw_lb_protects (session-gated writes) and list_protecting_decisions
// (ungated read). A protects edge is an association between a load-bearing
// decision and a Feature -- never a parent link.
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

// lbProtectsInput is add_lb_protects' and withdraw_lb_protects' argument schema.
type lbProtectsInput struct {
	krillSessionInput
	DecisionID string `json:"decision_id" jsonschema:"The load-bearing decision's surrogate id, as a UUID string."`
	FeatureID  string `json:"feature_id" jsonschema:"The Feature (capability) the decision protects, as a UUID string."`
}

// addLBProtectsInput adds the optional rationale to the shared edge identity.
type addLBProtectsInput struct {
	lbProtectsInput
	Rationale string `json:"rationale,omitempty" jsonschema:"Optional free text: why the decision protects this Feature (rendered in the roadmap's Later coverage). Ignored when the edge is already active."`
}

func parseLBProtectsIDs(in lbProtectsInput) (decisionID, featureID uuid.UUID, err error) {
	if decisionID, err = uuid.Parse(in.DecisionID); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("decision_id: invalid or missing UUID")
	}
	if featureID, err = uuid.Parse(in.FeatureID); err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("feature_id: invalid or missing UUID")
	}
	return decisionID, featureID, nil
}

// RegisterAddLBProtects registers add_lb_protects: records that a decision
// protects a Feature. Idempotent for an already-active edge.
func RegisterAddLBProtects(reg *server.Registry, sessions store.SessionStore, edges store.LBProtectsStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "add_lb_protects",
		Description: "Record that a load-bearing decision protects a Feature (capability). An association only; the decision keeps its single FeatureSet parent. Optional rationale records why. Idempotent for an active edge (a different rationale is ignored; withdraw and re-add to change it); a previously withdrawn pair may be added again.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in addLBProtectsInput) (*mcp.CallToolResult, handlers.IDResponse, error) {
		var zero handlers.IDResponse
		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}
		decisionID, featureID, err := parseLBProtectsIDs(in.lbProtectsInput)
		if err != nil {
			return nil, zero, err
		}
		edge, err := edges.Add(ctx, sess.ScopeID, decisionID, featureID, in.Rationale)
		if err != nil {
			return nil, zero, err
		}
		return nil, handlers.IDResponse{ID: edge.ID.String()}, nil
	})
}

// WithdrawLBProtectsResponse is withdraw_lb_protects' response body.
type WithdrawLBProtectsResponse struct {
	Withdrawn bool `json:"withdrawn"`
}

// RegisterWithdrawLBProtects registers withdraw_lb_protects: retires the
// active edge, keeping its row and stamping the session's acting/on_behalf_of.
func RegisterWithdrawLBProtects(reg *server.Registry, sessions store.SessionStore, edges store.LBProtectsStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name:        "withdraw_lb_protects",
		Description: "Withdraw the active protects edge from a load-bearing decision to a Feature. The row is retained with withdrawn_at and the withdrawing subjects; it stops appearing in reads.",
	}, []server.Persona{server.PersonaRequirementContributor, server.PersonaAgent, server.PersonaSwarmOperator}, func(ctx context.Context, _ *mcp.CallToolRequest, in lbProtectsInput) (*mcp.CallToolResult, WithdrawLBProtectsResponse, error) {
		var zero WithdrawLBProtectsResponse
		sess, err := requireKrillSession(ctx, sessions, in.KrillSessionID)
		if err != nil {
			return nil, zero, err
		}
		decisionID, featureID, err := parseLBProtectsIDs(in)
		if err != nil {
			return nil, zero, err
		}
		if err := edges.Withdraw(ctx, sess.ScopeID, decisionID, featureID, sess.Acting, sess.OnBehalfOf); err != nil {
			return nil, zero, err
		}
		return nil, WithdrawLBProtectsResponse{Withdrawn: true}, nil
	})
}

type listProtectingDecisionsInput struct {
	FeatureID string `json:"feature_id" jsonschema:"The Feature (capability) to list active protecting decisions for, as a UUID string."`
}

// ProtectingDecision is one entry of ListProtectingDecisionsResponse.
type ProtectingDecision struct {
	ID            string `json:"id"`
	DisplayNumber int    `json:"display_number"`
	Name          string `json:"name"`
}

// ListProtectingDecisionsResponse is list_protecting_decisions' response body.
type ListProtectingDecisionsResponse struct {
	Decisions []ProtectingDecision `json:"decisions"`
}

// RegisterListProtectingDecisions registers list_protecting_decisions: the
// active decisions recorded as protecting a Feature (empty = uncovered).
func RegisterListProtectingDecisions(reg *server.Registry, edges store.LBProtectsStore, decisions store.LoadBearingDecisionStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "list_protecting_decisions",
		Description: "List the load-bearing decisions with an active protects edge to a Feature, from recorded edges only. An empty list means the capability is uncovered.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, in listProtectingDecisionsInput) (*mcp.CallToolResult, ListProtectingDecisionsResponse, error) {
		out := ListProtectingDecisionsResponse{Decisions: []ProtectingDecision{}}
		featureID, err := uuid.Parse(in.FeatureID)
		if err != nil {
			return nil, out, fmt.Errorf("feature_id: invalid or missing UUID")
		}
		list, err := edges.ListActiveByFeatures(ctx, []uuid.UUID{featureID})
		if err != nil {
			return nil, out, err
		}
		for _, e := range list {
			d, err := decisions.GetCurrentByID(ctx, e.DecisionID)
			if err != nil {
				continue // edge to a since-voided decision protects nothing
			}
			out.Decisions = append(out.Decisions, ProtectingDecision{ID: d.ID.String(), DisplayNumber: d.DisplayNumber, Name: d.Name})
		}
		return nil, out, nil
	})
}

// RegisterLBProtectsAll registers the LB-protects tool group on the design mount.
func RegisterLBProtectsAll(reg *server.Registry, sessions store.SessionStore, edges store.LBProtectsStore, decisions store.LoadBearingDecisionStore) {
	RegisterAddLBProtects(reg, sessions, edges)
	RegisterWithdrawLBProtects(reg, sessions, edges)
	RegisterListProtectingDecisions(reg, edges, decisions)
}
