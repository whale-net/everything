package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry wraps *mcp.Server so every tool registration on the spec
// surface (/mcp/spec), the design-session surface (/mcp/design), or the
// operator surface (/mcp/ops, issue #2867) goes through
// RegisterRead/RegisterWrite/RegisterOpsRead/RegisterOpsWrite rather than
// mcp.AddTool directly -- the choke point registry_tools_test.go's
// negative tests check against ("no write/mutation tool is registered on
// the spec endpoint" and "no tool is registered on both mounts").
// RegisterWrite (below) is new as of issue #2547: M1 shipped no write
// tools at all (see ../../ARCHITECTURE.md "The MCP spec surface" for that
// now-corrected sentence); M2's design-session surface is the first
// milestone that needs one. RegisterOpsRead/RegisterOpsWrite (also below)
// are new as of issue #2867: M5's operator surface is the first mount
// whose authorization boundary is the mount itself, not a per-tool
// allow-list a caller opts into.
type Registry struct {
	server *mcp.Server
}

// NewRegistry wraps srv for tool registration.
func NewRegistry(srv *mcp.Server) *Registry {
	return &Registry{server: srv}
}

// RegisterRead adds a read-only tool. Every call requires a Persona to have
// been resolved by PersonaMiddleware or WhagentPersonaMiddleware before h
// runs (NFR1: authorization is by persona, never individual identity) -- a
// call with no resolved Persona is rejected before h is ever entered.
// Every read tool this package backs (../../tools, both the FR5-FR8 spec
// surface and this task's design-session surface) is open to any resolved
// persona: there is no per-tool persona allow-list on the read side in M1
// or M2, only on the write side (RegisterWrite below) -- a later
// milestone's work-axis surface (M4) is where a persona-restricted READ
// tool first becomes necessary. Every call -- authorized or rejected -- is
// traced and logged by instrumentToolCall (observability.go).
func RegisterRead[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		return instrumentToolCall(ctx, tool.Name, func(ctx context.Context) (*mcp.CallToolResult, Out, error) {
			var zero Out
			if PersonaFromContext(ctx) == "" {
				return nil, zero, fmt.Errorf("unauthenticated: no caller persona resolved")
			}
			return h(ctx, req, in)
		})
	}
	mcp.AddTool(reg.server, tool, wrapped)
}

// RegisterWrite adds a write tool to whichever mount reg backs --
// originally only the design-session surface (/mcp/design, transport.go's
// designMountPath, M2's first MCP write path, issue #2547), now also the
// work-axis surface (/mcp/work, transport.go's workMountPath, M4). Exactly
// like RegisterRead, every call requires a Persona to have been resolved
// before h runs.
//
// allowedPersonas is the minimal per-tool allow-list this task's Scope
// section calls for: pass nil (or an empty slice) for a tool any resolved
// persona may call -- open_design_session, append_revision_event, and
// init_session, the write tools with no persona-sensitivity of their own --
// or a non-empty list to reject every persona not named in it. Every
// non-empty allow-list in this package includes PersonaSwarmOperator
// (issue #2926): an allow-list naming only PersonaAgent and/or
// PersonaRequirementContributor made the tool unreachable end-to-end from
// any mcpauth-authenticated caller, including every krill-design/krill-work
// subagent, which share the parent session's mcpauth connection and can
// never resolve PersonaAgent themselves (that persona is produced only by
// the whagent-net front door, whagent_auth.go). This is deliberately an
// allow-list per tool, not a policy engine -- nothing in this milestone
// needs more than that.
//
// This mirrors audience_score_system/mcp/server/registry.go's
// RegisterRead/RegisterWrite split in spirit -- persona gating stands in
// for that package's ChannelScoped authorization check, and this
// package's own instrumentToolCall (observability.go) mirrors that
// package's tracing/logging wrapper -- but the precedent draws one more
// distinction this function deliberately does NOT carry over:
//
//   - Idempotency (that package's WriteMutate/WriteRender split, guarded by
//     store.Idempotency/IdempotencyKeyed). No FR or NFR in this milestone
//     requires replay-safety for a write tool call, unlike
//     audience_score_system's NFR2/LB4 -- wiring an idempotency guard here
//     with nothing driving the requirement would be unused machinery, not
//     a genuine carry-over of the precedent's reasoning.
//
// Krill-session gating -- every write tool's HTTP twin requires a valid
// X-Krill-Session-Id (api/handlers/gate.go's RequireSession) -- is also
// deliberately NOT handled here. Persona is resolved once, from ctx, by
// middleware this package already owns; a krill session id is instead a
// per-call INPUT field each tool's In type carries, and validating it
// needs a store.SessionStore this package has never depended on and still
// does not (../../ARCHITECTURE.md's "the separate krill/mcp binary...
// never touches krill_session" -- corrected in place by this task to
// describe krill/mcp/tools instead, which now does). See that package's
// design.go for the one factored session check every write tool calls
// before h's mutation runs, mirroring krill/importer's own direct-to-store
// requireSession for a caller that cannot wrap itself in HTTP middleware
// either.
func RegisterWrite[In, Out any](reg *Registry, tool *mcp.Tool, allowedPersonas []Persona, h mcp.ToolHandlerFor[In, Out]) {
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		return instrumentToolCall(ctx, tool.Name, func(ctx context.Context) (*mcp.CallToolResult, Out, error) {
			var zero Out
			persona := PersonaFromContext(ctx)
			if persona == "" {
				return nil, zero, fmt.Errorf("unauthenticated: no caller persona resolved")
			}
			if len(allowedPersonas) > 0 && !personaAllowed(persona, allowedPersonas) {
				return nil, zero, fmt.Errorf("forbidden: persona %q may not call %s", persona, tool.Name)
			}
			return h(ctx, req, in)
		})
	}
	mcp.AddTool(reg.server, tool, wrapped)
}

// personaAllowed reports whether persona appears in allowed.
func personaAllowed(persona Persona, allowed []Persona) bool {
	for _, p := range allowed {
		if p == persona {
			return true
		}
	}
	return false
}

// registerOpsGated is the persona gate RegisterOpsRead and RegisterOpsWrite
// share: exactly PersonaSwarmOperator may reach h, fixed rather than taken
// as an allow-list parameter -- transport.go's opsMountPath doc comment
// makes that one persona the operator mount's entire reason to exist, so
// there is nothing for a caller-supplied allow-list to ever vary. Mirrors
// RegisterRead/RegisterWrite's own "no resolved persona -> unauthenticated"
// gate first.
func registerOpsGated[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	wrapped := func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		return instrumentToolCall(ctx, tool.Name, func(ctx context.Context) (*mcp.CallToolResult, Out, error) {
			var zero Out
			persona := PersonaFromContext(ctx)
			if persona == "" {
				return nil, zero, fmt.Errorf("unauthenticated: no caller persona resolved")
			}
			if persona != PersonaSwarmOperator {
				return nil, zero, fmt.Errorf("forbidden: persona %q may not call %s", persona, tool.Name)
			}
			return h(ctx, req, in)
		})
	}
	mcp.AddTool(reg.server, tool, wrapped)
}

// RegisterOpsRead adds a read-only tool to the operator surface
// (transport.go's opsMountPath, /mcp/ops) -- M5's console queries
// (FR4/FR5/FR10/FR12, issue #2867). Unlike RegisterRead (open to any
// resolved persona on the spec/design mounts), every tool registered here
// -- read or write -- requires PersonaSwarmOperator specifically: the ops
// mount's authorization boundary IS this persona restriction, enforced at
// the mount rather than layered onto individual tools elsewhere (see
// transport.go's opsMountPath doc comment).
func RegisterOpsRead[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	registerOpsGated(reg, tool, h)
}

// RegisterOpsWrite adds a write tool to the operator surface
// (transport.go's opsMountPath, /mcp/ops) -- M5's operator verbs
// (FR6-FR9, issue #2867). Gated identically to RegisterOpsRead; kept as
// its own function (rather than one shared entry point) so the read/write
// distinction RegisterRead/RegisterWrite already model on the other two
// mounts stays visible here too, per this package's doc comment.
func RegisterOpsWrite[In, Out any](reg *Registry, tool *mcp.Tool, h mcp.ToolHandlerFor[In, Out]) {
	registerOpsGated(reg, tool, h)
}
