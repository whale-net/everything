package tools

import "context"

// ScopeResolver is this package's own copy of
// ../server/scope.go's identical interface literal: the scope-
// resolution seam dispatch.go's resolveGrantTokenForAgent/
// resolveGrantTokenForSession use (issue #2430, FR7) to resolve the
// scope an agent id or session id belongs to (whagent_net/session.
// AgentDefinition.Scope, issue #2424's FR1), without this package ever
// importing whagent_net/session or talking to Postgres directly
// (deps_test.go's TestBUILD_NoStoreOrTemporalDependency, pinned by issue
// #2120).
//
// Duplicated here rather than imported from ../server (or a shared third
// package) because Go interfaces are structural: whagent_net/mcpscope.Resolver
// satisfies both this type and ../server.ScopeResolver simply by having
// the right method set, with neither package importing the other or a
// shared interface-only package. This keeps both deps_test.go files
// trivially green with zero import surface added for an interface this
// small.
//
// fake_scope_resolver_test.go is the in-memory double dispatch.go's own
// tests (and every tool's) drive this interface against -- never a real
// Postgres-backed whagent_net/mcpscope.Resolver, which is constructed
// exclusively at whagent_net/mcp/main.go's composition root.
type ScopeResolver interface {
	// ScopeForAgent resolves agentID's current AgentDefinition and
	// returns its Scope (nil if the agent carries no scope).
	ScopeForAgent(ctx context.Context, agentID string) (*string, error)
	// ScopeForSession resolves sessionID's already-recorded
	// agent-definition assignment and returns that assignment's Scope --
	// never a scope re-derived from a fresh agent_id parsed off the
	// request.
	ScopeForSession(ctx context.Context, sessionID string) (*string, error)
}
