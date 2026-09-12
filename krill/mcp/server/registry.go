package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry wraps *mcp.Server so every tool registration on the spec
// surface goes through RegisterRead rather than mcp.AddTool directly --
// the choke point a later negative test (issue #2494's Testing section:
// "no write/mutation tool is registered on the spec endpoint") checks
// against. There is deliberately no RegisterWrite in this package: M1
// ships no write tools on this endpoint at all (see
// ../../ARCHITECTURE.md "The MCP spec surface").
type Registry struct {
	server *mcp.Server
}

// NewRegistry wraps srv for tool registration.
func NewRegistry(srv *mcp.Server) *Registry {
	return &Registry{server: srv}
}

// RegisterRead adds a read-only tool to the spec surface. Every call
// requires a Persona to have been resolved by PersonaMiddleware or
// WhagentPersonaMiddleware before h runs (NFR1: authorization is by
// persona, never individual identity) -- a call with no resolved Persona
// is rejected before h is ever entered. Every granularity this milestone
// exposes (FR5-FR8) is open to all three personas once authenticated:
// there is no per-tool persona allow-list in M1, since none of the four
// tools this package backs (../../tools) touches anything
// persona-sensitive -- a later milestone's work-axis surface (M4) is
// where a per-tool allow-list first becomes necessary.
func RegisterRead[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		var zero Out
		if PersonaFromContext(ctx) == "" {
			return nil, zero, fmt.Errorf("unauthenticated: no caller persona resolved")
		}
		return h(ctx, req, in)
	}
	mcp.AddTool(reg.server, tool, wrapped)
}
