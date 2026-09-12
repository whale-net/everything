package server

import (
	"context"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Registry wraps *mcp.Server so every tool registration on either the spec
// surface (/mcp/spec) or the design-session surface (/mcp/design, this
// task) goes through RegisterRead/RegisterWrite rather than mcp.AddTool
// directly -- the choke point registry_tools_test.go's negative tests
// check against ("no write/mutation tool is registered on the spec
// endpoint" and, this task, "no tool is registered on both mounts").
// RegisterWrite (below) is new as of issue #2547: M1 shipped no write
// tools at all (see ../../ARCHITECTURE.md "The MCP spec surface" for that
// now-corrected sentence); M2's design-session surface is the first
// milestone that needs one.
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
// tool first becomes necessary.
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

// RegisterWrite adds a write tool to the design-session surface
// (/mcp/design, transport.go's designMountPath) -- M2's first MCP write
// path (issue #2547). Exactly like RegisterRead, every call requires a
// Persona to have been resolved before h runs.
//
// allowedPersonas is the minimal per-tool allow-list this task's Scope
// section calls for: pass nil (or an empty slice) for a tool any resolved
// persona may call -- open_design_session and append_revision_event, the
// two write tools with no persona-sensitivity of their own -- or a
// non-empty list to reject every persona not named in it.
// tools.RegisterProposeEntities is the one caller that passes a non-empty
// list ([]Persona{PersonaAgent}): FR9/FR10's mediated write requires a
// producer-role Agent to be the caller, since FR10's "acting must differ
// from on-behalf-of" can never be satisfied by a human acting for itself.
// This is deliberately an allow-list per tool, not a policy engine --
// nothing in this milestone needs more than that.
//
// This mirrors audience_score_system/mcp/server/registry.go's
// RegisterRead/RegisterWrite split in spirit -- persona gating stands in
// for that package's ChannelScoped authorization check -- but the
// precedent draws two more distinctions this function deliberately does
// NOT carry over:
//
//   - Idempotency (that package's WriteMutate/WriteRender split, guarded by
//     store.Idempotency/IdempotencyKeyed). No FR or NFR in this milestone
//     requires replay-safety for a write tool call, unlike
//     audience_score_system's NFR2/LB4 -- wiring an idempotency guard here
//     with nothing driving the requirement would be unused machinery, not
//     a genuine carry-over of the precedent's reasoning.
//   - Per-call observability (that package's instrumentToolCall tracing/
//     logging wrapper). This package has no equivalent middleware for
//     either RegisterRead or RegisterWrite yet; adding one only to
//     RegisterWrite would make the two registration paths inconsistent
//     with each other for a reason unrelated to this task's scope.
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
		var zero Out
		persona := PersonaFromContext(ctx)
		if persona == "" {
			return nil, zero, fmt.Errorf("unauthenticated: no caller persona resolved")
		}
		if len(allowedPersonas) > 0 && !personaAllowed(persona, allowedPersonas) {
			return nil, zero, fmt.Errorf("forbidden: persona %q may not call %s", persona, tool.Name)
		}
		return h(ctx, req, in)
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
