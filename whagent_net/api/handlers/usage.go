// GetSessionUsage (FR4/NFR5, issue #2238) is this file's read path:
// turns-used-of-turn-cap and cost-used-of-cost-cap, derived by summing
// committed `turn_usage` rows (session.UsageStore.Summary) against the
// caps on the session's pinned agent_definition -- the same summed-rows
// source cap evaluation already uses (whagent_net/worker/caps.go).
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// defaultMaxTurns and defaultMaxCostUSD mirror
// whagent_net/worker/caps.go's identically-named constants and
// ARCHITECTURE.md "Guardrails" ("defaults 100 turns / $1"). Duplicated
// (not imported) for the same `package main` reason session.go's
// sessionWorkflowTaskQueue doc comment gives for its own constants --
// caps.go lives in the worker binary's package main, which nothing
// outside it can import.
const (
	defaultMaxTurns   = 100
	defaultMaxCostUSD = 1.0
)

// GetSessionUsage is a read path (FR4/NFR5): resolves the session
// (NOT_FOUND if unknown), reads the caps from the session's current SCD2
// agent assignment (session.AgentDefinitionStore.CurrentAssignment ->
// GetVersion), applying the defaults above when a definition field is
// zero-valued, and combines that with session.UsageStore.Summary --
// every figure summed fresh from committed turn_usage rows, never a
// separately-mutated counter. Authorization follows the same rule as
// GetSession/ReadTranscript: any authenticated caller may read any
// session's usage, no ownership check.
func (s *SessionServer) GetSessionUsage(ctx context.Context, req *pb.GetSessionUsageRequest) (*pb.GetSessionUsageResponse, error) {
	id, err := uuid.Parse(req.GetSessionId())
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid session_id: %v", err)
	}

	sess, err := s.store.Sessions().GetByID(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get session: %v", err)
	}
	if sess == nil {
		return nil, status.Error(codes.NotFound, "session not found")
	}

	maxTurns := defaultMaxTurns
	maxCostUSD := defaultMaxCostUSD

	assignment, err := s.store.AgentDefinitions().CurrentAssignment(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get current agent assignment: %v", err)
	}
	if assignment != nil {
		def, err := s.store.AgentDefinitions().GetVersion(ctx, assignment.AgentID, assignment.AgentVersion)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "get agent definition: %v", err)
		}
		if def != nil {
			if def.MaxTurns != 0 {
				maxTurns = def.MaxTurns
			}
			if def.MaxCostUSD != 0 {
				maxCostUSD = def.MaxCostUSD
			}
		}
	}

	summary, err := s.store.Usage().Summary(ctx, id)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "summarize usage: %v", err)
	}

	return &pb.GetSessionUsageResponse{
		Usage: &pb.SessionUsage{
			TurnsUsed:     int32(summary.TurnsUsed),
			TurnCap:       int32(maxTurns),
			CostUsd:       summary.CostUSD,
			CostCapUsd:    maxCostUSD,
			CostEstimated: summary.CostEstimated,
		},
	}, nil
}
