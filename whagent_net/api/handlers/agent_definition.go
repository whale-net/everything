package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ListAgentDefinitionScopes returns every distinct grant-scope configured
// across all agent_definition rows (session.AgentDefinitionStore.
// ListScopes) -- the set `ui`'s self-service /grants page (issue #2432)
// lists as available to consent to, so an operator can start a new
// delegated-grant consent by clicking a link there instead of hand-typing
// a /mcp/consent?scope=<s> URL. Any authenticated caller may call this --
// scope names carry no secret material, mirroring the read-any rule
// GetSession/ListSessions already establish (#2237).
func (s *SessionServer) ListAgentDefinitionScopes(ctx context.Context, _ *pb.ListAgentDefinitionScopesRequest) (*pb.ListAgentDefinitionScopesResponse, error) {
	scopes, err := s.store.AgentDefinitions().ListScopes(ctx)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "list agent definition scopes: %v", err)
	}
	return &pb.ListAgentDefinitionScopesResponse{Scopes: scopes}, nil
}
