package handlers

import (
	"context"
	"encoding/json"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/session"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// isAgentAdmin reports whether the caller's token roles include the
// configured API admin role. An unset or empty role fails closed.
func (s *SessionServer) isAgentAdmin(ctx context.Context) bool {
	if s.agentAdminRole == "" {
		return false
	}
	claims, ok := grpcauth.ClaimsFromContext(ctx)
	if !ok {
		return false
	}
	return hasRole(claims.Roles, s.agentAdminRole)
}

func agentDefToProto(d *session.AgentDefinition) (*pb.AgentDefinition, error) {
	toolSet, err := json.Marshal(d.ToolSet)
	if err != nil {
		return nil, err
	}
	out := &pb.AgentDefinition{
		Id:                d.ID.String(),
		AgentId:           d.AgentID,
		ToolSet:           string(toolSet),
		MaxTurns:          int32(d.MaxTurns),
		MaxCostUsd:        d.MaxCostUSD,
		MaxToolIterations: int32(d.MaxToolIterations),
		ToolLoadingMode:   string(d.ToolLoadingMode),
		RequiredRole:      d.RequiredRole,
		ValidFrom:         timestamppb.New(d.ValidFrom),
	}
	if d.Scope != nil {
		out.Scope = *d.Scope
	}
	if d.Model != nil {
		out.Model = *d.Model
	}
	if d.ModelDefinitionID != nil {
		id := d.ModelDefinitionID.String()
		out.ModelDefinitionId = &id
	}
	if d.SystemPrompt != nil {
		out.SystemPrompt = *d.SystemPrompt
	}
	if d.ValidTo != nil {
		out.ValidTo = timestamppb.New(*d.ValidTo)
	}
	return out, nil
}

// ListAgents returns every current agent definition ordered by agent_id.
func (s *SessionServer) ListAgents(ctx context.Context, _ *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	defs, err := s.store.AgentDefinitions().ListCurrent(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list agents: %v", err)
	}
	resp := &pb.ListAgentsResponse{}
	for _, d := range defs {
		p, err := agentDefToProto(d)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "encode agent: %v", err)
		}
		resp.Agents = append(resp.Agents, p)
	}
	return resp, nil
}

// GetAgent returns one agent's current definition; include_history requires
// the admin role.
func (s *SessionServer) GetAgent(ctx context.Context, req *pb.GetAgentRequest) (*pb.GetAgentResponse, error) {
	if req.GetIncludeHistory() && !s.isAgentAdmin(ctx) {
		return nil, status.Error(codes.PermissionDenied, "definition history requires the agent admin role")
	}
	cur, err := s.store.AgentDefinitions().GetCurrent(ctx, req.GetAgentId())
	if err != nil {
		return nil, status.Errorf(codes.Internal, "get agent: %v", err)
	}
	if cur == nil {
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.GetAgentId())
	}
	resp := &pb.GetAgentResponse{}
	if resp.Current, err = agentDefToProto(cur); err != nil {
		return nil, status.Errorf(codes.Internal, "encode agent: %v", err)
	}
	if req.GetIncludeHistory() {
		hist, err := s.store.AgentDefinitions().History(ctx, req.GetAgentId())
		if err != nil {
			return nil, status.Errorf(codes.Internal, "agent history: %v", err)
		}
		for _, d := range hist {
			p, err := agentDefToProto(d)
			if err != nil {
				return nil, status.Errorf(codes.Internal, "encode agent: %v", err)
			}
			resp.History = append(resp.History, p)
		}
	}
	return resp, nil
}

// GetDashboardSummary returns today's (UTC) per-agent traffic and cost.
// Any authenticated caller may call it.
func (s *SessionServer) GetDashboardSummary(ctx context.Context, _ *pb.GetDashboardSummaryRequest) (*pb.GetDashboardSummaryResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetDashboardSummary not implemented")
}
