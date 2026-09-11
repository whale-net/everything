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
//
// domainResolver/grant are FR7/FR8's dispatch-time resolution seams
// (domain.go, grant.go): call resolves in.SessionID's domain via
// DomainForSession and acquires a token via grant.TokenSource before
// forwarding (dispatch.go's resolveGrantTokenForSession), for the
// browser-OAuth2 path only -- see dispatch.go's own doc comment for the
// manual-token-path no-op case.
type sendTurnTool struct {
	client         pb.SessionServiceClient
	domainResolver DomainResolver
	grant          GrantSource
}

// RegisterSendTurn registers the send_turn tool on srv.
func RegisterSendTurn(srv *mcp.Server, client pb.SessionServiceClient, domainResolver DomainResolver, grant GrantSource) {
	t := &sendTurnTool{client: client, domainResolver: domainResolver, grant: grant}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "send_turn",
		Description: "Send a turn to a running whagent-net session. Returns once the turn is accepted and queued -- it does NOT wait for the turn to complete. Use read_transcript or get_session to observe the result.",
	}, t.call)
}

// call resolves in.SessionID's domain and acquires a token before
// forwarding to t.client.SendTurn (dispatch.go's
// resolveGrantTokenForSession, FR7/FR8) -- otherwise a direct pass-through,
// no other business logic -- forwarding the caller's bearer token via ctx
// exactly as ../server/auth.go's AuthMiddleware placed it there (the
// manual-token path) or as resolveGrantTokenForSession acquired it (the
// browser-OAuth2 path). FR1: SendTurn returns as soon as api has accepted
// and queued the turn, not once it has completed, so this method returns
// immediately after that RPC resolves -- it never polls or waits. The
// result's Content is set explicitly (rather than left to the default
// JSON-only rendering) so that "not completed yet" reads as text, not
// just as an omission an operator could miss.
func (t *sendTurnTool) call(ctx context.Context, req *mcp.CallToolRequest, in SendTurnInput) (*mcp.CallToolResult, SendTurnOutput, error) {
	ctx, err := resolveGrantTokenForSession(ctx, t.domainResolver, t.grant, in.SessionID)
	if err != nil {
		return nil, SendTurnOutput{}, err
	}

	resp, err := t.client.SendTurn(ctx, &pb.SendTurnRequest{
		SessionId: in.SessionID,
		Input:     in.Input,
	})
	if err != nil {
		return nil, SendTurnOutput{}, toolError("SendTurn", err)
	}

	out := SendTurnOutput{
		SessionID: in.SessionID,
		State:     sessionStateString(resp.GetSession().GetState()),
	}
	result := &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{
			Text: fmt.Sprintf(
				"Turn accepted and queued (session state: %s). This does NOT mean the turn has completed -- use read_transcript or get_session to observe the result.",
				out.State,
			),
		}},
	}
	return result, out, nil
}
