// Package grantkey derives the `grant` half of libs/go/grpcauth's
// (subject, grant) delegated-grant key from AgentDefinition.Domain (issue
// #2424, plan #2421's FR1/FR4/NFR2).
//
// FR4: "The identifier used to key a persisted grant ... is derived
// directly from AgentDefinition.Domain ... never by parsing agent_id or
// server_url by convention, and never by an access-control check layered
// on top of a shared or ambiguous key." ForDomain is that derivation, and
// it is the *only* one -- deriving a grant key from agent_id,
// required_role, or tool_set[].server_url is forbidden by FR1/FR4 and must
// be rejected in review.
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

// domainPattern is the character set a valid AgentDefinition.Domain value
// may use: lowercase letters, digits, underscore, hyphen -- matching every
// domain value that exists today (config/agents.yaml's "audience_score_system",
// AGENTS.md's Domains table entries like "manmanv2"/"app-registry"). A
// domain outside this set is rejected as malformed rather than silently
// accepted, so a typo'd or copy-pasted domain (e.g. one carrying internal
// whitespace or punctuation) fails loudly here instead of quietly minting
// a grant key nothing else will ever match.
var domainPattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// ForDomain derives the grpcauth grant key for domain. It is deterministic
// (the same domain always derives the same key) and total over every
// well-formed domain value -- it never falls back to an empty or
// placeholder key.
//
// The derivation is intentionally literal: the grant key for domain d is d
// itself, once validated. "Derived directly from AgentDefinition.Domain"
// (FR4) is satisfied most simply, and most auditably, by not transforming
// it at all -- a grant key that hashed or obscured the domain would make
// FR14/FR16's revoke-page grouping and any future operator-facing debug
// output (e.g. a raw grant_key column read during an incident) harder to
// read for no isolation benefit: two different domain strings already can
// never collide under an identity mapping.
//
// ForDomain returns an error -- never a silent fallback or empty string --
// for a domain that is empty, whitespace-only, or contains any character
// outside domainPattern. This is deliberate: an unset or malformed domain
// must never collapse two distinct domains onto the same grant key (or
// onto no key at all), because that key is the sole mechanism NFR2 relies
// on to keep one domain's consent from being usable against another's
// agent.
func ForDomain(domain string) (string, error) {
	if strings.TrimSpace(domain) == "" {
		return "", fmt.Errorf("grantkey: domain is empty or whitespace-only")
	}
	if !domainPattern.MatchString(domain) {
		return "", fmt.Errorf("grantkey: domain %q is malformed (must match %s)", domain, domainPattern.String())
	}
	return domain, nil
}
