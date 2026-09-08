package tools

import (
	"context"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// renderedText concatenates every mcp.TextContent block in result -- used
// by tests that assert on send_turn's explicit "not completed yet" wording
// (issue #2120's Implementation section, "Async semantics preserved").
func renderedText(t *testing.T, result *mcp.CallToolResult) string {
	t.Helper()
	var out string
	for _, c := range result.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}

// TestSendTurn_ForwardsArgsAndReturnsPromptlyWithoutClaimingCompletion is
// FR1's own contract: send_turn returns once the turn is accepted and
// queued, not once it completes -- neither the structured State field nor
// the rendered text may claim the turn finished.
func TestSendTurn_ForwardsArgsAndReturnsPromptlyWithoutClaimingCompletion(t *testing.T) {
	fc := &fakeSessionServiceClient{
		sendTurnFunc: func(ctx context.Context, in *pb.SendTurnRequest) (*pb.SendTurnResponse, error) {
			assert.Equal(t, "sess-1", in.SessionId)
			assert.Equal(t, "what's next?", in.Input)
			return &pb.SendTurnResponse{Session: &pb.Session{
				SessionId: "sess-1",
				State:     pb.SessionState_SESSION_STATE_RUNNING,
			}}, nil
		},
	}
	tool := &sendTurnTool{client: fc}

	result, out, err := tool.call(context.Background(), nil, SendTurnInput{
		SessionID: "sess-1",
		Input:     "what's next?",
	})

	require.NoError(t, err)
	assert.Equal(t, "sess-1", out.SessionID)
	assert.Equal(t, "running", out.State)
	require.Len(t, fc.calls, 1)
	assert.Equal(t, "SendTurn", fc.calls[0].rpc)

	require.NotNil(t, result)
	require.Len(t, result.Content, 1)
	rendered := renderedText(t, result)
	assert.Contains(t, rendered, "accepted and queued")
	assert.Contains(t, rendered, "does NOT mean the turn has completed", "the result text must explicitly deny completion, not just omit mentioning it")
}
