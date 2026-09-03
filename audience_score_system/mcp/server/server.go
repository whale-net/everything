// Package server is `mcp`'s bootstrap: building the *mcp.Server every
// C4-C7/C9/C10 tool plugs into (see ../../ARCHITECTURE.md "MCP server"),
// wiring the caller-auth middleware (auth.go), and exposing it over
// streamable HTTP (transport.go). Channel-scoping (channelscope.go) and
// idempotency (idempotency.go) are wired into tool registration
// (registry.go)'s RegisterRead/RegisterWrite, so every product tool gets
// both automatically.
//
// Statelessness (LB4): New returns a single *mcp.Server built only from a
// *store.Store -- no in-memory maps keyed by session/conversation, no
// assumption that consecutive calls hit the same process. The same
// *mcp.Server instance is reused across every request/session
// (transport.go's getServer callback always returns it), and Postgres
// (via st) is the only place any state -- caller identity, idempotency
// ledger, product data -- lives between calls.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/whale-net/everything/audience_score_system/store"
)

// Implementation identifies this MCP server to connecting clients.
var Implementation = &mcp.Implementation{
	Name:    "audience-score-system-mcp",
	Version: "0.1.0",
}

// New builds the mcp.Server every Audience Score System MCP tool plugs
// into, with PersonMiddleware (auth.go) wired to resolve the caller's
// credential to a store.Person on every request. Holds no state beyond
// st (LB4).
func New(st *store.Store) *mcp.Server {
	srv := mcp.NewServer(Implementation, nil)
	srv.AddReceivingMiddleware(PersonMiddleware(st.Persons()))
	return srv
}
