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
// (auth.go) wired to place the caller's bearer token or resolved identity
// onto every tool call's context -- either forwarded again, unchanged, to
// `api` (the manual-token path), or resolved into a working credential at
// tool-dispatch time via ../tools' DomainResolver/GrantSource seams (the
// browser-OAuth2 path, issue #2430's FR7/FR8). Holds no state of its own
// -- statelessness lives entirely in `api` and its store, never here.
func New() *mcp.Server {
	srv := mcp.NewServer(Implementation, nil)
	srv.AddReceivingMiddleware(AuthMiddleware())
	return srv
}
