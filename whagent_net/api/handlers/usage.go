// GetSessionUsage (FR4/NFR5, issue #2238) is this file's read path:
// turns-used-of-turn-cap and cost-used-of-cost-cap, derived by summing
// committed `turn_usage` rows (session.UsageStore.Summary) against the
// caps on the session's pinned agent_definition -- the same summed-rows
// source cap evaluation already uses (whagent_net/worker/caps.go). This
// is a Scaffold-phase skeleton: it returns UNIMPLEMENTED until the
// Implementation phase wires the real session/caps lookups in.
package handlers

import (
	"context"

	pb "github.com/whale-net/everything/whagent_net/protos"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetSessionUsage is issue #2238's Scaffold-phase skeleton -- UNIMPLEMENTED
// until the Implementation phase resolves the session, reads its current
// SCD2 agent assignment for caps, and combines that with
// session.UsageStore.Summary.
func (s *SessionServer) GetSessionUsage(ctx context.Context, req *pb.GetSessionUsageRequest) (*pb.GetSessionUsageResponse, error) {
	return nil, status.Error(codes.Unimplemented, "GetSessionUsage: not implemented")
}
