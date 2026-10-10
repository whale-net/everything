package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/google/uuid"

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

// UpdateAgent supersedes an agent's current definition with the full
// desired definition in the request (admin only).
func (s *SessionServer) UpdateAgent(ctx context.Context, req *pb.UpdateAgentRequest) (*pb.UpdateAgentResponse, error) {
	if !s.isAgentAdmin(ctx) {
		return nil, status.Error(codes.PermissionDenied, "updating an agent requires the agent admin role")
	}
	def, err := s.validateUpdateAgent(ctx, req)
	if err != nil {
		return nil, err
	}
	cur, err := s.store.AgentDefinitions().Supersede(ctx, req.GetAgentId(), def)
	switch {
	case errors.Is(err, session.ErrAgentNotFound):
		return nil, status.Errorf(codes.NotFound, "agent %q not found", req.GetAgentId())
	case errors.Is(err, session.ErrSupersedeConflict):
		return nil, status.Errorf(codes.Aborted, "agent %q was modified concurrently; retry", req.GetAgentId())
	case err != nil:
		return nil, status.Errorf(codes.Internal, "update agent: %v", err)
	}
	p, err := agentDefToProto(cur)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "encode agent: %v", err)
	}
	return &pb.UpdateAgentResponse{Current: p}, nil
}

func (s *SessionServer) validateUpdateAgent(ctx context.Context, req *pb.UpdateAgentRequest) (*session.AgentDefinition, error) {
	bad := func(format string, a ...any) error { return status.Errorf(codes.InvalidArgument, format, a...) }
	if req.GetAgentId() == "" {
		return nil, bad("agent_id is required")
	}
	def := &session.AgentDefinition{
		AgentID:           req.GetAgentId(),
		MaxTurns:          int(req.GetMaxTurns()),
		MaxCostUSD:        req.GetMaxCostUsd(),
		MaxToolIterations: int(req.GetMaxToolIterations()),
		ToolLoadingMode:   session.ToolLoadingMode(req.GetToolLoadingMode()),
		RequiredRole:      req.RequiredRole,
	}
	hasModel, hasDef := req.GetModel() != "", req.ModelDefinitionId != nil
	if hasModel == hasDef {
		return nil, bad("exactly one of model or model_definition_id must be set")
	}
	if hasModel {
		m := req.GetModel()
		def.Model = &m
	} else {
		id, err := uuid.Parse(req.GetModelDefinitionId())
		if err != nil {
			return nil, bad("model_definition_id is not a valid UUID")
		}
		md, err := s.store.ModelDefinitions().GetByID(ctx, id)
		if err != nil {
			return nil, status.Errorf(codes.Internal, "look up model definition: %v", err)
		}
		if md == nil {
			return nil, bad("model_definition_id %s does not exist", id)
		}
		def.ModelDefinitionID = &id
	}
	if def.MaxTurns <= 0 || def.MaxToolIterations <= 0 || def.MaxCostUSD <= 0 {
		return nil, bad("max_turns, max_cost_usd and max_tool_iterations must be positive")
	}
	switch def.ToolLoadingMode {
	case session.ToolLoadingModeBulk, session.ToolLoadingModeSearch:
	default:
		return nil, bad("tool_loading_mode %q must be \"bulk\" or \"search\"", req.GetToolLoadingMode())
	}
	// Scope is optional; when set it must be non-blank (config.Validate rule).
	if req.GetScope() != "" {
		sc := req.GetScope()
		if strings.TrimSpace(sc) == "" {
			return nil, bad("scope, if set, must not be blank")
		}
		def.Scope = &sc
	}
	if req.GetToolSet() != "" {
		if err := json.Unmarshal([]byte(req.GetToolSet()), &def.ToolSet); err != nil {
			return nil, bad("tool_set must be a JSON array of tool server refs: %v", err)
		}
		for i, ref := range def.ToolSet {
			if ref.ServerURL == "" {
				return nil, bad("tool_set[%d]: server_url is required", i)
			}
		}
	}
	if req.GetSystemPrompt() != "" {
		sp := req.GetSystemPrompt()
		def.SystemPrompt = &sp
	}
	return def, nil
}
