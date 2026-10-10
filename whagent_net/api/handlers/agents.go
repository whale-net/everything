package handlers

import (
	"context"

	"github.com/whale-net/everything/libs/go/grpcauth"
	pb "github.com/whale-net/everything/whagent_net/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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

// ListAgents returns every current agent definition ordered by agent_id.
func (s *SessionServer) ListAgents(ctx context.Context, _ *pb.ListAgentsRequest) (*pb.ListAgentsResponse, error) {
	return nil, status.Error(codes.Unimplemented, "ListAgents not implemented")
}

// GetAgent returns one agent's current definition; include_history requires
// the admin role.
func (s *SessionServer) GetAgent(ctx context.Context, req *pb.GetAgentRequest) (*pb.GetAgentResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetAgent not implemented")
}
