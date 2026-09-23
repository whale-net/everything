// This file (issue #2827) is the `init_session` MCP tool: the one gap that
// left an MCP-only caller -- no direct Postgres access, no shell into the
// cluster -- unable to use any krill write path at all. Every write tool
// registered by this package requires a krill_session_id (design.go's
// krillSessionInput), but the only way to mint one was api/handlers/
// session.go's InitSessionHandler, an HTTP endpoint with no MCP wrapper,
// which itself required a scope_id the caller had no way to discover.
//
// This tool closes both halves at once: it mirrors InitSessionHandler
// field-for-field (reusing its exported handlers.SubjectRequest/
// handlers.ParseSubject/handlers.InitSessionResponse rather than a second
// copy of the same shapes and validation, per LB7), except scope_id --
// this deployment's seeder (migrate/seed/seed.go) guarantees exactly one
// scope row exists, so this tool resolves it itself via
// store.ScopeStore.GetSole instead of asking the caller to supply or
// discover a scope_id.
package tools

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/mcp/server"
	"github.com/whale-net/everything/krill/store"
)

// initSessionInput is init_session's argument schema -- mirrors
// api/handlers/session.go's initSessionRequest, minus scope_id (see this
// file's package doc comment for why).
type initSessionInput struct {
	Acting           handlers.SubjectRequest `json:"acting" jsonschema:"Who is making this call."`
	OnBehalfOf       handlers.SubjectRequest `json:"on_behalf_of" jsonschema:"Who this session's writes are attributed to. Pass the same triple as acting when a caller is acting for itself -- never inferred."`
	WhagentSessionID *string                 `json:"whagent_session_id,omitempty" jsonschema:"The inbound whagent-net Claim.WhagentSessionID, if this call arrived through the whagent-net front door -- purely a correlation field, omit otherwise."`
}

// RegisterInitSession registers init_session (issue #2827): the one MCP
// entry point that mints a krill_session_id, which every other write tool
// on this mount requires as input. Backed by the same
// store.SessionStore.InitSession every other InitSession caller
// (api/handlers/session.go's InitSessionHandler, krill/importer) uses --
// this tool is a third caller, not a fourth code path.
//
// No allowedPersonas restriction: any resolved persona (mcpauth's
// PersonaSwarmOperator or whagent-net's PersonaAgent) may mint a session,
// exactly as InitSessionHandler accepts any caller today -- the MCP mount's
// own two front doors (../server's PersonaMiddleware/
// WhagentPersonaMiddleware) are the authentication this tool's HTTP twin
// still defers to a later milestone (session.go's package doc comment).
func RegisterInitSession(reg *server.Registry, sessions store.SessionStore, scopes store.ScopeStore) {
	server.RegisterWrite(reg, &mcp.Tool{
		Name: "init_session",
		Description: "Mint a krill_session_id (FR3): the session id every other write tool on this mount requires as " +
			"input. Call this first -- every other write tool rejects a missing or unknown krill_session_id. " +
			"The response also carries scope_id, the value list_products, list_tasks, and the ops console tools take as input.",
	}, nil, func(ctx context.Context, _ *mcp.CallToolRequest, in initSessionInput) (*mcp.CallToolResult, handlers.InitSessionResponse, error) {
		var zero handlers.InitSessionResponse

		scope, err := scopes.GetSole(ctx)
		if err != nil {
			return nil, zero, fmt.Errorf("resolve scope: %w", err)
		}

		acting, err := handlers.ParseSubject(in.Acting)
		if err != nil {
			return nil, zero, fmt.Errorf("acting: %w", err)
		}
		onBehalfOf, err := handlers.ParseSubject(in.OnBehalfOf)
		if err != nil {
			return nil, zero, fmt.Errorf("on_behalf_of: %w", err)
		}

		id, err := sessions.InitSession(ctx, scope.ID, acting, onBehalfOf, in.WhagentSessionID)
		if err != nil {
			return nil, zero, fmt.Errorf("init session: %w", err)
		}
		return nil, handlers.InitSessionResponse{SessionID: id.String(), ScopeID: scope.ID.String()}, nil
	})
}
