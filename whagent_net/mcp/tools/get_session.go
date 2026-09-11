package tools

import (
	"context"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	pb "github.com/whale-net/everything/whagent_net/protos"
)

// GetSessionInput is get_session's argument schema (issue #2120, FR3).
type GetSessionInput struct {
	SessionID string `json:"session_id" jsonschema:"The session to look up, as a UUID string"`
}

// GetSessionOutput is get_session's structured result (FR3): the
// session's current state, and -- for an ended session -- why: which of
// done/stopped/failed/capped, which cap for capped, category + detail
// for failed. CapKind/ErrorCategory/ErrorDetail are left empty (never a
// zero-value placeholder) when the session hasn't ended for that reason
// -- mirrors pb.Session's has_cap_kind/has_error_category optionality
// (session.proto's "Session" message doc).
type GetSessionOutput struct {
	SessionID     string `json:"session_id" jsonschema:"The session's id"`
	State         string `json:"state" jsonschema:"One of the six SessionState values (running, awaiting_input, done, stopped, failed, capped)"`
	AgentID       string `json:"agent_id" jsonschema:"The agent definition this session runs"`
	Model         string `json:"model" jsonschema:"The model this session is running (after any FR5 override resolution)"`
	CapKind       string `json:"cap_kind,omitempty" jsonschema:"Set only when state is capped: which guardrail ended the session (turns or cost)"`
	ErrorCategory string `json:"error_category,omitempty" jsonschema:"Set only when state is failed: retryable or non_retryable"`
	ErrorDetail   string `json:"error_detail,omitempty" jsonschema:"Set only when state is failed: human-readable detail of what failed"`
}

// getSessionTool holds the SessionService client this tool is a
// pass-through to.
//
// domainResolver/grant are FR7/FR8's dispatch-time resolution seams
// (domain.go, grant.go): call resolves in.SessionID's domain via
// DomainForSession and acquires a token via grant.TokenSource before
// forwarding (dispatch.go's resolveGrantTokenForSession), for the
// browser-OAuth2 path only -- see dispatch.go's own doc comment for the
// manual-token-path no-op case.
type getSessionTool struct {
	client         pb.SessionServiceClient
	domainResolver DomainResolver
	grant          GrantSource
}

// RegisterGetSession registers the get_session tool on srv.
func RegisterGetSession(srv *mcp.Server, client pb.SessionServiceClient, domainResolver DomainResolver, grant GrantSource) {
	t := &getSessionTool{client: client, domainResolver: domainResolver, grant: grant}
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_session",
		Description: "Get a whagent-net session's current state, and -- for an ended session -- why it ended (FR3).",
	}, t.call)
}

// call resolves in.SessionID's domain and acquires a token (dispatch.go's
// resolveGrantTokenForSession, FR7/FR8) before wiring get_session to
// t.client.GetSession, mapping pb.GetSessionResponse's optional
// cap_kind/error_category/error_detail fields onto GetSessionOutput --
// present only when the underlying pointer on the *pb.Session itself is
// non-nil (proto3 `optional` presence), never a zero-value substitute --
// and forwarding the caller's bearer token via ctx exactly as
// ../server/auth.go's AuthMiddleware placed it there (the manual-token
// path) or as resolveGrantTokenForSession acquired it (the browser-OAuth2
// path).
func (t *getSessionTool) call(ctx context.Context, req *mcp.CallToolRequest, in GetSessionInput) (*mcp.CallToolResult, GetSessionOutput, error) {
	ctx, err := resolveGrantTokenForSession(ctx, t.domainResolver, t.grant, in.SessionID)
	if err != nil {
		return nil, GetSessionOutput{}, err
	}

	resp, err := t.client.GetSession(ctx, &pb.GetSessionRequest{SessionId: in.SessionID})
	if err != nil {
		return nil, GetSessionOutput{}, toolError("GetSession", err)
	}
	sess := resp.GetSession()

	out := GetSessionOutput{
		SessionID: sess.GetSessionId(),
		State:     sessionStateString(sess.GetState()),
		AgentID:   sess.GetAgentId(),
		Model:     sess.GetModel(),
	}
	if sess.CapKind != nil {
		out.CapKind = capKindString(*sess.CapKind)
	}
	if sess.ErrorCategory != nil {
		out.ErrorCategory = errorCategoryString(*sess.ErrorCategory)
	}
	if sess.ErrorDetail != nil {
		out.ErrorDetail = *sess.ErrorDetail
	}
	return nil, out, nil
}
