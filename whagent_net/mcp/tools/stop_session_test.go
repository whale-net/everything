package tools

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// TestStopSession_ForwardsSessionIDAndRendersState proves stop_session is
// a direct pass-through: the session_id it was given is exactly what
// reaches StopSessionRequest, and the response's state is rendered
// verbatim.
func TestStopSession_ForwardsSessionIDAndRendersState(t *testing.T) {
	fc := &fakeSessionServiceClient{
		stopSessionFunc: func(ctx context.Context, in *pb.StopSessionRequest) (*pb.StopSessionResponse, error) {
			assert.Equal(t, "sess-42", in.SessionId)
			return &pb.StopSessionResponse{Session: &pb.Session{
				SessionId: "sess-42",
				State:     pb.SessionState_SESSION_STATE_STOPPED,
			}}, nil
		},
	}
	tool := &stopSessionTool{client: fc}

	_, out, err := tool.call(context.Background(), nil, StopSessionInput{SessionID: "sess-42"})

	require.NoError(t, err)
	assert.Equal(t, "sess-42", out.SessionID)
	assert.Equal(t, "stopped", out.State)
	require.Len(t, fc.calls, 1)
	assert.Equal(t, "StopSession", fc.calls[0].rpc)
}
