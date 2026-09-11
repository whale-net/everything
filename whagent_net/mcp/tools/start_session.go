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
//
// domainResolver/grant are FR7/FR8's dispatch-time resolution seams
// (domain.go, grant.go), injected here by issue #2430's Scaffold phase.
// call does not use them yet -- resolving in.AgentID's domain and
// acquiring a token via grant.TokenSource before forwarding is this same
// issue's Implementation phase.
type startSessionTool struct {
	client         pb.SessionServiceClient
	domainResolver DomainResolver
	grant          GrantSource
}

// RegisterStartSession registers the start_session tool on srv.
func RegisterStartSession(srv *mcp.Server, client pb.SessionServiceClient, domainResolver DomainResolver, grant GrantSource) {
	t := &startSessionTool{client: client, domainResolver: domainResolver, grant: grant}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "start_session",
		Description: "Start a new whagent-net agent session, optionally sending its first turn. Returns once the session has started -- see send_turn for how a later turn's completion is observed.",
	}, t.call)
}

// call wires start_session to t.client.StartSession, then, only when
// in.FirstTurn is non-empty, t.client.SendTurn on the session just
// started (issue #2120's Implementation section, "Design question:
// first_turn" -- StartSessionRequest carries no first-turn field of its
// own, so this is two RPC calls under the hood, not one; see
// ../../ARCHITECTURE.md "Open items" for the recorded decision). The
// caller's bearer token travels on ctx exactly as
// ../server/auth.go's AuthMiddleware placed it there -- grpcauth's user
// token dial option (main.go) reads it back off ctx for both calls, so
// both reach api as the same operator identity. If SendTurn fails after
// StartSession already succeeded, the error says so explicitly (the
// session was created, but its first turn was not queued) rather than
// looking like start_session failed outright -- an operator seeing this
// should retry with send_turn against the returned session_id, not
// start_session again.
func (t *startSessionTool) call(ctx context.Context, req *mcp.CallToolRequest, in StartSessionInput) (*mcp.CallToolResult, StartSessionOutput, error) {
	startReq := &pb.StartSessionRequest{AgentId: in.AgentID}
	if in.ModelOverride != "" {
		startReq.ModelOverride = &in.ModelOverride
	}

	startResp, err := t.client.StartSession(ctx, startReq)
	if err != nil {
		return nil, StartSessionOutput{}, toolError("StartSession", err)
	}
	sess := startResp.GetSession()

	if in.FirstTurn == "" {
		return nil, StartSessionOutput{
			SessionID: sess.GetSessionId(),
			State:     sessionStateString(sess.GetState()),
		}, nil
	}

	turnResp, err := t.client.SendTurn(ctx, &pb.SendTurnRequest{
		SessionId: sess.GetSessionId(),
		Input:     in.FirstTurn,
	})
	if err != nil {
		return nil, StartSessionOutput{}, fmt.Errorf(
			"start_session: session %s was started but its first turn was not queued: %w",
			sess.GetSessionId(), toolError("SendTurn", err),
		)
	}

	return nil, StartSessionOutput{
		SessionID: sess.GetSessionId(),
		State:     sessionStateString(turnResp.GetSession().GetState()),
	}, nil
}
