// Package mcpdomain is the Postgres-backed implementation of the
// DomainResolver-shaped interfaces `mcp` declares in its own packages
// (whagent_net/mcp/server.DomainResolver, whagent_net/mcp/tools.DomainResolver
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
// Domain field is the only source of domain resolution below. Parsing
// agent_id, required_role, or tool_set[].server_url to infer a domain is
// disallowed -- see whagent_net/session/agentdef.go's AgentDefinition doc
// comment and whagent_net/grantkey.ForDomain, which is FR4's one intended
// consumer of a resolved domain.
package mcpdomain

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/whagent_net/session"
)

// ErrNotFound is returned (wrapped, via fmt.Errorf's %w) by
// DomainForAgent/DomainForSession when the requested agent id or session
// id has no resolvable domain -- distinguishable from a transport/database
// error via errors.Is(err, ErrNotFound), so a caller can render a
// 4xx-shaped tool error rather than a 5xx (issue #2427's Implementation
// section).
var ErrNotFound = errors.New("mcpdomain: not found")

// Resolver implements whagent_net/mcp/server.DomainResolver and
// whagent_net/mcp/tools.DomainResolver against a
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

// DomainForAgent resolves agentID's current (highest-Version)
// AgentDefinition and returns its Domain. Returns an error wrapping
// ErrNotFound when agentID has no agent_definition row at all.
func (r *Resolver) DomainForAgent(ctx context.Context, agentID string) (string, error) {
	def, err := r.store.GetLatest(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("mcpdomain: resolve domain for agent %q: %w", agentID, err)
	}
	if def == nil {
		return "", fmt.Errorf("%w: agent id %q", ErrNotFound, agentID)
	}
	return def.Domain, nil
}

// DomainForSession resolves sessionID's already-recorded agent-definition
// assignment (the session_agent row session.AssignToSession wrote) and
// returns the Domain of the exact (AgentID, Version) it was assigned to --
// never the current/latest version of that AgentID, which may since have
// been upserted with a different Domain. Returns an error wrapping
// ErrNotFound when sessionID is not a valid session id, has no
// session_agent assignment, or (should it ever happen) is assigned to an
// agent_definition row that no longer exists.
func (r *Resolver) DomainForSession(ctx context.Context, sessionID string) (string, error) {
	sid, err := uuid.Parse(sessionID)
	if err != nil {
		return "", fmt.Errorf("%w: session id %q is not a valid uuid", ErrNotFound, sessionID)
	}

	assignment, err := r.store.CurrentAssignment(ctx, sid)
	if err != nil {
		return "", fmt.Errorf("mcpdomain: resolve current agent assignment for session %q: %w", sessionID, err)
	}
	if assignment == nil {
		return "", fmt.Errorf("%w: session id %q has no current agent assignment", ErrNotFound, sessionID)
	}

	def, err := r.store.GetVersion(ctx, assignment.AgentID, assignment.AgentVersion)
	if err != nil {
		return "", fmt.Errorf("mcpdomain: resolve domain for session %q's assigned agent %s@%d: %w",
			sessionID, assignment.AgentID, assignment.AgentVersion, err)
	}
	if def == nil {
		return "", fmt.Errorf("%w: session %q is assigned to agent %s@%d, which no longer exists",
			ErrNotFound, sessionID, assignment.AgentID, assignment.AgentVersion)
	}

	return def.Domain, nil
}
