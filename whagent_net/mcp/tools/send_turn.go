package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// SendTurnInput is send_turn's argument schema (issue #2120, FR1).
type SendTurnInput struct {
	SessionID string `json:"session_id" jsonschema:"The session to send this turn to, as a UUID string"`
	Input     string `json:"input" jsonschema:"The turn's text"`
}

// SendTurnOutput is send_turn's structured result. FR1: send_turn returns
// once the turn is accepted and queued -- it does not block until the
// turn completes, so State here reflects acceptance, not completion; a
// caller reads completion via read_transcript/get_session.
type SendTurnOutput struct {
	SessionID string `json:"session_id" jsonschema:"The session id this turn was sent to"`
	State     string `json:"state" jsonschema:"The session's state immediately after the turn was accepted -- not necessarily the state once the turn finishes"`
}

// sendTurnTool holds the SessionService client this tool is a
// pass-through to.
type sendTurnTool struct {
	client pb.SessionServiceClient
}

// RegisterSendTurn registers the send_turn tool on srv.
func RegisterSendTurn(srv *mcp.Server, client pb.SessionServiceClient) {
	t := &sendTurnTool{client: client}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send_turn",
		Description: "Send a turn to a running whagent-net session. Returns once the turn is accepted and queued -- it does NOT wait for the turn to complete. Use read_transcript or get_session to observe the result.",
	}, t.call)
}

// call is a scaffold stub: issue #2120's Implementation phase wires this
// to t.client.SendTurn, a direct pass-through with no business logic,
// forwarding the caller's bearer token via ctx.
func (t *sendTurnTool) call(ctx context.Context, req *mcp.CallToolRequest, in SendTurnInput) (*mcp.CallToolResult, SendTurnOutput, error) {
	return nil, SendTurnOutput{}, fmt.Errorf("send_turn: not implemented yet (issue #2120 scaffold phase)")
}
