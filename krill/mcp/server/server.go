// Package server is `mcp`'s bootstrap for krill's FR10/NFR1 spec surface:
// building the *mcp.Server every slice-query tool plugs into (../tools),
// wiring the caller-persona middleware (auth.go/whagent_auth.go), and
// exposing it over streamable HTTP (transport.go) at its own pre-filtered
// mount point (`/mcp/spec`), following the two-front-door pattern already
// shipped in `audience_score_system/mcp` and `whagent_net/mcp` (see
// ../../ARCHITECTURE.md "The MCP spec surface (FR10/NFR1, issue #2494)").
//
// M1 ships only this spec-scoped mount: the work-axis surface (M4) has no
// endpoint yet and is out of scope here (root plan issue #2485's roadmap).
package server

import (
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Implementation identifies this MCP server to connecting clients.
var Implementation = &mcp.Implementation{
	Name:    "krill-mcp",
	Version: "0.1.0",
}

// New builds the mcp.Server every krill spec-slice tool plugs into, with
// PersonaMiddleware (auth.go) wired to resolve every caller -- whichever
// front door authenticated it -- to a Persona before any tool handler
// runs. main.go additionally mounts WhagentPersonaMiddleware
// (whagent_auth.go) OUTSIDE this call (i.e. so it executes BEFORE
// PersonaMiddleware) when the agent front door is configured -- see that
// middleware's doc comment for the coexistence contract, mirroring
// audience_score_system/mcp/server.New's own construction order exactly.
//
// Holds no state of its own: every tool handler this server dispatches to
// receives a *slice.Querier directly (../tools' RegisterXxx functions),
// never a package-level or server-held reference -- there is nothing here
// that varies per request/session (statelessness mirrors
// audience_score_system/mcp/server.New's own doc comment).
func New() *mcp.Server {
	srv := mcp.NewServer(Implementation, nil)
	srv.AddReceivingMiddleware(PersonaMiddleware())
	return srv
}
