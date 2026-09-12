// Package grantkey derives the `grant` half of libs/go/grpcauth's
// (subject, grant) delegated-grant key from AgentDefinition.Scope (issue
// #2424, plan #2421's FR1/FR4/NFR2).
//
// FR4: "The identifier used to key a persisted grant ... is derived
// directly from AgentDefinition.Scope ... never by parsing agent_id or
// server_url by convention, and never by an access-control check layered
// on top of a shared or ambiguous key." ForScope is that derivation, and
// it is the *only* one -- deriving a grant key from agent_id,
// required_role, or tool_set[].server_url is forbidden by FR1/FR4 and must
// be rejected in review.
//
// AgentDefinition.Scope is nullable: an agent definition with no scope
// carries no delegated-grant scoping at all. Callers must check for that
// nil case themselves and skip calling ForScope entirely when Scope is
// unset -- ForScope's own contract is only about the non-nil case: given a
// scope string, it is never given an empty one and told to cope.
//
// This package is deliberately pure and dependency-free: no import of
// whagent_net/session, pgx, or whagent_net/config. That keeps it safe for
// both whagent_net/mcp/server and whagent_net/mcp/tools to depend on
// without tripping their deps_test.go's TestBUILD_NoStoreOrTemporalDependency
// (issue #2120's "mcp never talks to Postgres or Temporal directly"), and
// for whagent_net/ui to depend on it too, without pulling in anything
// I/O-touching.
package grantkey

import (
	"fmt"
	"regexp"
	"strings"
)

// scopePattern is the character set a valid AgentDefinition.Scope value
// may use: lowercase letters, digits, underscore, hyphen -- matching every
// scope value that exists today (config/agents.yaml's "audience_score_system",
// AGENTS.md's Domains table entries like "manmanv2"/"app-registry"). A
// scope outside this set is rejected as malformed rather than silently
// accepted, so a typo'd or copy-pasted scope (e.g. one carrying internal
// whitespace or punctuation) fails loudly here instead of quietly minting
// a grant key nothing else will ever match.
var scopePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ForScope derives the grpcauth grant key for scope. It is deterministic
// (the same scope always derives the same key) and total over every
// well-formed scope value -- it never falls back to an empty or
// placeholder key.
//
// The derivation is intentionally literal: the grant key for scope s is s
// itself, once validated. "Derived directly from AgentDefinition.Scope"
// (FR4) is satisfied most simply, and most auditably, by not transforming
// it at all -- a grant key that hashed or obscured the scope would make
// FR14/FR16's revoke-page grouping and any future operator-facing debug
// output (e.g. a raw grant_key column read during an incident) harder to
// read for no isolation benefit: two different scope strings already can
// never collide under an identity mapping.
//
// ForScope returns an error -- never a silent fallback or empty string --
// for a scope that is empty, whitespace-only, or contains any character
// outside scopePattern. This is deliberate: a malformed scope must never
// collapse two distinct scopes onto the same grant key (or onto no key at
// all), because that key is the sole mechanism NFR2 relies on to keep one
// scope's consent from being usable against another's agent. A caller with
// no scope at all (AgentDefinition.Scope == nil) must not call ForScope in
// the first place -- see this package's doc comment.
func ForScope(scope string) (string, error) {
	if strings.TrimSpace(scope) == "" {
		return "", fmt.Errorf("grantkey: scope is empty or whitespace-only")
	}
	if !scopePattern.MatchString(scope) {
		return "", fmt.Errorf("grantkey: scope %q is malformed (must match %s)", scope, scopePattern.String())
	}
	return scope, nil
}
