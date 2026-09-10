package server

import "context"

// DomainResolver is the domain-resolution seam `mcp` needs at
// tool-dispatch time (issue #2427, FR7): resolving the one domain
// (whagent_net/session.AgentDefinition.Domain, issue #2424's FR1) an
// agent id or an already-started session belongs to, without this
// package -- or ../tools, which declares an identical interface literal
// of its own, see tools/domain.go's doc comment for why it is duplicated
// rather than shared -- ever importing whagent_net/session or talking to
// Postgres directly (deps_test.go's TestBUILD_NoStoreOrTemporalDependency,
// pinned by issue #2120).
//
// Both methods return plain strings and error only -- no
// whagent_net/session type ever appears in this signature, by design.
// whagent_net/mcpdomain.Resolver is the only production implementation,
// constructed exclusively at whagent_net/mcp/main.go's composition root,
// exactly like this package's own mcpauth.CredentialStore usage (see
// auth.go's NewVerifier and main.go's initializeTokenExchange).
//
// A caller resolving "not found" (an unknown agent id or session id) gets
// a distinguishable error from a transport/database failure -- see
// whagent_net/mcpdomain.ErrNotFound -- so it can render a 4xx-shaped tool
// error rather than a 5xx.
//
// This task is purely additive: DomainResolver is declared and its
// Postgres-backed implementation is constructed and held at main.go's
// composition root, but no tool handler or middleware in this package
// calls it yet. Wiring an actual call at tool-dispatch time is issue
// #2427's dependent "dispatch-time rewiring" task.
type DomainResolver interface {
	// DomainForAgent resolves agentID's current AgentDefinition and
	// returns its Domain.
	DomainForAgent(ctx context.Context, agentID string) (string, error)
	// DomainForSession resolves sessionID's already-recorded
	// agent-definition assignment and returns that assignment's Domain --
	// never a domain re-derived from a fresh agent_id parsed off the
	// request.
	DomainForSession(ctx context.Context, sessionID string) (string, error)
}
