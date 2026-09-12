// Package mcpscope is the Postgres-backed implementation of the
// ScopeResolver-shaped interfaces `mcp` declares in its own packages
// (whagent_net/mcp/server.ScopeResolver, whagent_net/mcp/tools.ScopeResolver
// -- deliberately duplicated interface literals, not this package's own
// type, so neither //whagent_net/mcp/server nor //whagent_net/mcp/tools
// ever has to import this package or anything it pulls in).
//
// It lives outside whagent_net/mcp/server and whagent_net/mcp/tools
// specifically so it may depend on whagent_net/session (and, transitively,
// Postgres/pgx) -- both of those packages' own deps_test.go
// (TestBUILD_NoStoreOrTemporalDependency) hard-fail their own BUILD.bazel
// on exactly those dependencies, pinning issue #2120's "mcp never talks to
// Postgres or Temporal directly; the gRPC API is the service boundary."
// The Postgres-backed Resolver below is constructed only at
// whagent_net/mcp/main.go's composition root, mirroring exactly how
// libs/go/mcpauth.CredentialStore's Postgres implementation is constructed
// there today (see main.go's initializeAuthDeps).
//
// FR1/FR4 constraint, restated here: whagent_net/session.AgentDefinition's
// Scope field is the only source of scope resolution below. Parsing
// agent_id, required_role, or tool_set[].server_url to infer a scope is
// disallowed -- see whagent_net/session/agentdef.go's AgentDefinition doc
// comment and whagent_net/grantkey.ForScope, which is FR4's one intended
// consumer of a resolved, non-nil scope.
package mcpscope

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/session"
)

// ErrNotFound is returned (wrapped, via fmt.Errorf's %w) by
// ScopeForAgent/ScopeForSession when the requested agent id or session
// id does not resolve to any agent_definition at all -- distinguishable
// from a transport/database error via errors.Is(err, ErrNotFound), so a
// caller can render a 4xx-shaped tool error rather than a 5xx (issue
// #2427's Implementation section). This is distinct from a successfully
// resolved agent definition that simply has a nil Scope -- that is not an
// error, it means the agent carries no delegated-grant scoping.
var ErrNotFound = errors.New("mcpscope: not found")

// Resolver implements whagent_net/mcp/server.ScopeResolver and
// whagent_net/mcp/tools.ScopeResolver against a
// whagent_net/session.AgentDefinitionStore. The zero value is not usable;
// construct with New.
type Resolver struct {
	store session.AgentDefinitionStore
}

// New returns a Resolver backed by store -- typically
// (whagent_net/session.Store).AgentDefinitions().
func New(store session.AgentDefinitionStore) *Resolver {
	return &Resolver{store: store}
}

// ScopeForAgent resolves agentID's current (highest-Version)
// AgentDefinition and returns its Scope (nil if the agent carries no
// scope). Returns an error wrapping ErrNotFound when agentID has no
// agent_definition row at all.
func (r *Resolver) ScopeForAgent(ctx context.Context, agentID string) (*string, error) {
	def, err := r.store.GetLatest(ctx, agentID)
	if err != nil {
		return nil, fmt.Errorf("mcpscope: resolve scope for agent %q: %w", agentID, err)
	}
	if def == nil {
		return nil, fmt.Errorf("%w: agent id %q", ErrNotFound, agentID)
	}
	return def.Scope, nil
}

// ScopeForSession resolves sessionID's already-recorded agent-definition
// assignment (the session_agent row session.AssignToSession wrote) and
// returns the Scope of the exact (AgentID, Version) it was assigned to --
// never the current/latest version of that AgentID, which may since have
// been upserted with a different Scope. Returns an error wrapping
// ErrNotFound when sessionID is not a valid session id, has no
// session_agent assignment, or (should it ever happen) is assigned to an
// agent_definition row that no longer exists.
func (r *Resolver) ScopeForSession(ctx context.Context, sessionID string) (*string, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return nil, fmt.Errorf("%w: session id %q is not a valid uuid", ErrNotFound, sessionID)
	}

	assignment, err := r.store.CurrentAssignment(ctx, sid)
	if err != nil {
		return nil, fmt.Errorf("mcpscope: resolve current agent assignment for session %q: %w", sessionID, err)
	}
	if assignment == nil {
		return nil, fmt.Errorf("%w: session id %q has no current agent assignment", ErrNotFound, sessionID)
	}

	def, err := r.store.GetVersion(ctx, assignment.AgentID, assignment.AgentVersion)
	if err != nil {
		return nil, fmt.Errorf("mcpscope: resolve scope for session %q's assigned agent %s@%d: %w",
			sessionID, assignment.AgentID, assignment.AgentVersion, err)
	}
	if def == nil {
		return nil, fmt.Errorf("%w: session %q is assigned to agent %s@%d, which no longer exists",
			ErrNotFound, sessionID, assignment.AgentID, assignment.AgentVersion)
	}

	return def.Scope, nil
}
