package server

import "context"

// ScopeResolver is the scope-resolution seam `mcp` needs at
// tool-dispatch time (issue #2427, FR7): resolving the one scope
// (whagent_net/session.AgentDefinition.Scope, issue #2424's FR1) an
// agent id or an already-started session belongs to, without this
// package -- or ../tools, which declares an identical interface literal
// of its own, see tools/scope.go's doc comment for why it is duplicated
// rather than shared -- ever importing whagent_net/session or talking to
// Postgres directly (deps_test.go's TestBUILD_NoStoreOrTemporalDependency,
// pinned by issue #2120).
//
// Both methods return a nullable string and error only -- no
// whagent_net/session type ever appears in this signature, by design. A
// nil scope means the agent carries no delegated-grant scoping at all.
// whagent_net/mcpscope.Resolver is the only production implementation,
// constructed exclusively at whagent_net/mcp/main.go's composition root,
// exactly like this package's own mcpauth.CredentialStore usage (see
// auth.go's NewVerifier and main.go's initializeAuthDeps).
//
// A caller resolving "not found" (an unknown agent id or session id) gets
// a distinguishable error from a transport/database failure -- see
// whagent_net/mcpscope.ErrNotFound -- so it can render a 4xx-shaped tool
// error rather than a 5xx.
//
// This task is purely additive: ScopeResolver is declared and its
// Postgres-backed implementation is constructed and held at main.go's
// composition root, but no tool handler or middleware in this package
// calls it yet. Wiring an actual call at tool-dispatch time is issue
// #2427's dependent "dispatch-time rewiring" task.
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
