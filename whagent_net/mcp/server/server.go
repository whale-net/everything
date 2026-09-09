// Package server is `mcp`'s bootstrap: building the *mcp.Server every
// tool (../tools) plugs into, wiring the caller-identity middleware
// (auth.go), and exposing it over streamable HTTP (transport.go). See
// ../../ARCHITECTURE.md's "Open items" ("`mcp` from Claude Code is the
// v1 answer") and issue #2120's Scaffold phase.
//
// mcp is a pure facade over `api`'s SessionService (issue #2113,
// ARCHITECTURE.md "Service boundary vs. package boundary"): it holds no
// store, no Temporal client, and mints nothing of its own -- every tool
// call is a pass-through gRPC call to `api`, authenticated as the
// operator who made it (auth.go), never a shared service account.
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation identifies this MCP server to connecting clients (e.g.
// Claude Code).
var Implementation = &mcp.Implementation{
	Name:    "whagent-net-mcp",
	Version: "0.1.0",
}

// New builds the mcp.Server every whagent-net MCP tool
// (../tools/RegisterStartSession et al.) plugs into, with AuthMiddleware
// (auth.go) wired to forward the caller's bearer token onto every tool
// call's context so it can be forwarded again, unchanged, to `api`. Holds
// no state of its own -- statelessness lives entirely in `api` and its
// store, never here.
//
// exchanger backs AuthMiddleware's OAuth2/token-exchange branch (FR9,
// issue #2249) -- main.go always constructs one (server.NewKeycloakExchanger),
// even when TokenExchangeConfig.Enabled() is false, so this parameter is
// never nil in production; a disabled exchanger simply errors if
// AuthMiddleware ever tries to use it (which it never does unless
// NewVerifier's credentials dependency is also configured -- see
// NewHTTPHandler).
func New(exchanger Exchanger) *mcp.Server {
	srv := mcp.NewServer(Implementation, nil)
	srv.AddReceivingMiddleware(AuthMiddleware(exchanger))
	return srv
}
