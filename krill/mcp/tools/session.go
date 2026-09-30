// init_session and get_scope. init_session takes no arguments: the caller's
// identity is derived from the verified credential (krill/caller) and the
// scope is the deployment's sole scope (store.ScopeStore.GetSole, shared
// with the api's GET /scope); a multi-scope deployment refuses until a
// credential-to-scope mapping exists.
package tools

import (
	"context"
	"fmt"

	sdkauth "github.com/modelcontextprotocol/go-sdk/auth"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/caller"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// initSessionInput is empty by design: identity comes from the verified
// credential and scope from the deployment, never from the client. The
// generated schema rejects any supplied field.
type initSessionInput struct{}

// getScopeInput is get_scope's (empty) argument schema.
type getScopeInput struct{}

// RegisterInitSession registers init_session: it mints a krill_session_id
// attributed to the caller's verified identity (krill/caller) under the
// deployment's sole scope. whagent_session_id comes from the whagent claim.
func RegisterInitSession(reg *server.Registry, sessions store.SessionStore, scopes store.ScopeStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "init_session",
		Description: "Mint a krill_session_id (FR3): the session id every other write tool on this mount requires as " +
			"input. Takes no arguments -- your identity and scope are derived server-side from your credential. " +
			"Call this first -- every other write tool rejects a missing or unknown krill_session_id. " +
			"The response also carries scope_id, the value list_products, list_tasks, and the ops console tools take as input.",
	}, []server.Persona{server.PersonaSwarmOperator, server.PersonaAgent}, func(ctx context.Context, req *mcp.CallToolRequest, _ initSessionInput) (*mcp.CallToolResult, handlers.InitSessionResponse, error) {
		var zero handlers.InitSessionResponse

		who, err := callerFromRequest(req)
		if err != nil {
			return nil, zero, err
		}
		scope, err := scopes.GetSole(ctx)
		if err != nil {
			return nil, zero, fmt.Errorf("resolve scope: %w", err)
		}

		var wsid *string
		if who.WhagentSessionID != "" {
			wsid = &who.WhagentSessionID
		}
		id, err := sessions.InitSession(ctx, scope.ID, toStoreSubject(who.Acting), toStoreSubject(who.OnBehalfOf), wsid)
		if err != nil {
			return nil, zero, fmt.Errorf("init session: %w", err)
		}
		return nil, handlers.InitSessionResponse{SessionID: id.String(), ScopeID: scope.ID.String()}, nil
	})
}

// RegisterGetScope registers get_scope: a read-only way to learn scope_id
// without minting a session.
func RegisterGetScope(reg *server.Registry, scopes store.ScopeStore) {
	server.RegisterRead(reg, &mcp.Tool{
		Name:        "get_scope",
		Description: "Return this deployment's scope_id (the value list_products and list_tasks take) without minting a session.",
	}, func(ctx context.Context, _ *mcp.CallToolRequest, _ getScopeInput) (*mcp.CallToolResult, handlers.ScopeResponse, error) {
		scope, err := scopes.GetSole(ctx)
		if err != nil {
			return nil, handlers.ScopeResponse{}, fmt.Errorf("resolve scope: %w", err)
		}
		return nil, handlers.ScopeResponse{ScopeID: scope.ID.String()}, nil
	})
}

func callerFromRequest(req *mcp.CallToolRequest) (caller.Identity, error) {
	var info *sdkauth.TokenInfo
	if req != nil && req.Extra != nil {
		info = req.Extra.TokenInfo
	}
	return caller.FromTokenInfo(info)
}

func toStoreSubject(s caller.Subject) store.Subject {
	return store.Subject{Iss: s.Iss, Sub: s.Sub, Kind: store.SubjectKind(s.Kind)}
}
