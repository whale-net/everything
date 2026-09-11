package tools

import (
	"context"

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
//
// domainResolver/grant are FR7/FR8's dispatch-time resolution seams
// (domain.go, grant.go): call resolves in.SessionID's domain via
// DomainForSession and acquires a token via grant.TokenSource before
// forwarding (dispatch.go's resolveGrantTokenForSession), for the
// browser-OAuth2 path only -- see dispatch.go's own doc comment for the
// manual-token-path no-op case.
type stopSessionTool struct {
	client         pb.SessionServiceClient
	domainResolver DomainResolver
	grant          GrantSource
}

// RegisterStopSession registers the stop_session tool on srv.
func RegisterStopSession(srv *mcp.Server, client pb.SessionServiceClient, domainResolver DomainResolver, grant GrantSource) {
	t := &stopSessionTool{client: client, domainResolver: domainResolver, grant: grant}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "stop_session",
		Description: "Stop a running whagent-net session, ending it in the 'stopped' state.",
	}, t.call)
}

// call resolves in.SessionID's domain and acquires a token before
// forwarding to t.client.StopSession (dispatch.go's
// resolveGrantTokenForSession, FR7/FR8) -- otherwise a direct
// pass-through, no other business logic -- forwarding the caller's
// bearer token via ctx exactly as ../server/auth.go's AuthMiddleware
// placed it there (the manual-token path) or as
// resolveGrantTokenForSession acquired it (the browser-OAuth2 path).
func (t *stopSessionTool) call(ctx context.Context, req *mcp.CallToolRequest, in StopSessionInput) (*mcp.CallToolResult, StopSessionOutput, error) {
	ctx, err := resolveGrantTokenForSession(ctx, t.domainResolver, t.grant, in.SessionID)
	if err != nil {
		return nil, StopSessionOutput{}, err
	}

	resp, err := t.client.StopSession(ctx, &pb.StopSessionRequest{SessionId: in.SessionID})
	if err != nil {
		return nil, StopSessionOutput{}, toolError("StopSession", err)
	}

	return nil, StopSessionOutput{
		SessionID: in.SessionID,
		State:     sessionStateString(resp.GetSession().GetState()),
	}, nil
}
