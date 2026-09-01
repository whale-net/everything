package server

import "github.com/modelcontextprotocol/go-sdk/mcp"

// Registry wraps *mcp.Server so every tool registration goes through one
// of RegisterRead/RegisterWrite rather than mcp.AddTool directly -- the
// choke point this task's Validation criteria checks with `grep` to prove
// there is no path to register a write tool without idempotency.
type Registry struct {
	server *mcp.Server
}

// NewRegistry wraps srv for tool registration.
func NewRegistry(srv *mcp.Server) *Registry {
	return &Registry{server: srv}
}

// RegisterRead adds a read-only tool.
//
// Scaffold only -- Channel-scoping (store.CanRead, channelscope.go) is not
// yet applied to registered handlers. Issue #1575's Implementation phase
// adds it as a per-call check keyed off each tool's channel_id argument,
// enforced here so no read tool author can forget it.
func RegisterRead[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(reg.server, tool, h)
}

// RegisterWrite adds a write-back tool.
//
// Scaffold only -- neither Channel-scoping (store.CanWrite,
// channelscope.go) nor idempotency wrapping (store.Idempotency, NFR2/LB4,
// idempotency.go) is yet applied to registered handlers. Issue #1575's
// Implementation phase makes RegisterWrite apply both automatically, so a
// write tool author cannot forget either -- at that point this is the only
// place in the codebase mcp.AddTool is called for a write tool (see this
// task's Validation criteria).
func RegisterWrite[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	mcp.AddTool(reg.server, tool, h)
}
