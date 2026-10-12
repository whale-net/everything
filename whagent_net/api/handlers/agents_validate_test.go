package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// validateUpdateAgent rejects max_turns <= 0 before touching the store, so the
// zero SessionServer is enough to exercise the rejection path.
func TestValidateUpdateAgent_MaxTurnsNonPositiveNamesValue(t *testing.T) {
	model := "m"
	for _, n := range []int32{0, -3} {
		srv := &SessionServer{}
		req := &pb.UpdateAgentRequest{AgentId: "ag", Model: model, MaxTurns: n,
			MaxCostUsd: 1, MaxToolIterations: 1, ToolLoadingMode: "bulk"}
		_, err := srv.validateUpdateAgent(t.Context(), req)
		assert.Equal(t, codes.InvalidArgument, status.Code(err), "max_turns=%d", n)
		assert.Contains(t, status.Convert(err).Message(), "max_turns must be >= 1: got ")
	}
}
