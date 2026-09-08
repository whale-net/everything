package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// StopSessionInput is stop_session's argument schema (issue #2120, FR1).
type StopSessionInput struct {
	SessionID string `json:"session_id" jsonschema:"The session to stop, as a UUID string"`
}

// StopSessionOutput is stop_session's structured result: the session's
// state after being signalled to stop (SESSION_STATE_STOPPED once the
// signal has been processed).
type StopSessionOutput struct {
	SessionID string `json:"session_id" jsonschema:"The stopped session's id"`
	State     string `json:"state" jsonschema:"The session's state after the stop signal"`
}

// stopSessionTool holds the SessionService client this tool is a
// pass-through to.
type stopSessionTool struct {
	client pb.SessionServiceClient
}

// RegisterStopSession registers the stop_session tool on srv.
func RegisterStopSession(srv *mcp.Server, client pb.SessionServiceClient) {
	t := &stopSessionTool{client: client}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "stop_session",
		Description: "Stop a running whagent-net session, ending it in the 'stopped' state.",
	}, t.call)
}

// call is a scaffold stub: issue #2120's Implementation phase wires this
// to t.client.StopSession, a direct pass-through with no business logic,
// forwarding the caller's bearer token via ctx.
func (t *stopSessionTool) call(ctx context.Context, req *mcp.CallToolRequest, in StopSessionInput) (*mcp.CallToolResult, StopSessionOutput, error) {
	return nil, StopSessionOutput{}, fmt.Errorf("stop_session: not implemented yet (issue #2120 scaffold phase)")
}
