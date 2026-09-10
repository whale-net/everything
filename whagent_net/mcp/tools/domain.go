package tools

import "context"

// DomainResolver is this package's own copy of
// ../server/domain.go's identical interface literal: the domain-
// resolution seam a tool handler will eventually need at dispatch time
// (issue #2427, FR7) to resolve the domain an agent id or session id
// belongs to (whagent_net/session.AgentDefinition.Domain, issue #2424's
// FR1), without this package ever importing whagent_net/session or
// talking to Postgres directly (deps_test.go's
// TestBUILD_NoStoreOrTemporalDependency, pinned by issue #2120).
//
// Duplicated here rather than imported from ../server (or a shared third
// package) because Go interfaces are structural: whagent_net/mcpdomain.Resolver
// satisfies both this type and ../server.DomainResolver simply by having
// the right method set, with neither package importing the other or a
// shared interface-only package. This keeps both deps_test.go files
// trivially green with zero import surface added for an interface this
// small.
//
// A fake in-memory implementation for dependent tasks' tests lives
// alongside fake_client_test.go once a task actually calls this interface
// -- issue #2427's Testing section reserves adding it for that point,
// since no tool handler calls this interface yet (see below).
//
// This task (#2427) is purely additive: DomainResolver is declared and
// whagent_net/mcpdomain.Resolver -- the Postgres-backed implementation --
// is constructed and held at whagent_net/mcp/main.go's composition root,
// but no tool handler in this package calls it yet. Wiring an actual call
// at tool-dispatch time is issue #2427's dependent "dispatch-time
// rewiring" task.
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
