package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// StartSessionInput is start_session's argument schema (issue #2120,
// FR1/FR5): an agent id, an optional first turn (sent as this session's
// first SendTurn once StartSession has returned -- see startSessionTool.
// call's doc comment for why this is two RPC calls, not one), and an
// optional model override checked against the provider catalogue by
// `api` (FR5) before any session row is written.
type StartSessionInput struct {
	AgentID       string `json:"agent_id" jsonschema:"The agent definition to start a session from"`
	FirstTurn     string `json:"first_turn,omitempty" jsonschema:"Optional first turn to send once the session has started; omit to start the session with no turn queued yet"`
	ModelOverride string `json:"model_override,omitempty" jsonschema:"Optional model id overriding the agent definition's default; rejected with FAILED_PRECONDITION if the provider catalogue does not serve it (FR5)"`
}

// StartSessionOutput is start_session's structured result: the started
// session's id and current state, mirroring pb.Session's caller-relevant
// fields.
type StartSessionOutput struct {
	SessionID string `json:"session_id" jsonschema:"The started session's id, as a UUID string"`
	State     string `json:"state" jsonschema:"The session's current state (see SessionState)"`
}

// startSessionTool holds the SessionService client this tool is a
// pass-through to (issue #2120's Implementation section, "Tools --
// one per gRPC RPC, no more").
type startSessionTool struct {
	client pb.SessionServiceClient
}

// RegisterStartSession registers the start_session tool on srv.
func RegisterStartSession(srv *mcp.Server, client pb.SessionServiceClient) {
	t := &startSessionTool{client: client}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "start_session",
		Description: "Start a new whagent-net agent session, optionally sending its first turn. Returns once the session has started -- see send_turn for how a later turn's completion is observed.",
	}, t.call)
}

// call is a scaffold stub: issue #2120's Implementation phase wires this
// to t.client.StartSession, then t.client.SendTurn when FirstTurn is set,
// forwarding the caller's bearer token via ctx exactly as
// ../server/auth.go's AuthMiddleware placed it there -- no business logic
// beyond that two-call sequence (StartSessionRequest carries no
// first-turn field of its own).
func (t *startSessionTool) call(ctx context.Context, req *mcp.CallToolRequest, in StartSessionInput) (*mcp.CallToolResult, StartSessionOutput, error) {
	return nil, StartSessionOutput{}, fmt.Errorf("start_session: not implemented yet (issue #2120 scaffold phase)")
}
