package grantkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForDomain_IsDeterministic proves ForDomain(d) returns the exact same
// key across repeated calls -- issue #2424's Testing section, first bullet.
func TestForDomain_IsDeterministic(t *testing.T) {
	first, err := ForDomain("audience_score_system")
	require.NoError(t, err)

	second, err := ForDomain("audience_score_system")
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// TestForDomain_DifferentDomainsNeverCollide proves two different domains
// never derive the same grant key -- FR4/NFR2's "one grant key, one
// domain" guarantee holds at the derivation function itself. Uses the
// same two example domains issue #2424's Testing section names.
func TestForDomain_DifferentDomainsNeverCollide(t *testing.T) {
	ass, err := ForDomain("audience_score_system")
	require.NoError(t, err)

	manmanv2, err := ForDomain("manmanv2")
	require.NoError(t, err)

	assert.NotEqual(t, ass, manmanv2)
}

// TestForDomain_EmptyDomain_ReturnsError proves an empty domain never
// silently produces a key -- an unset AgentDefinition.Domain must fail
// loudly rather than collapsing onto some default grant.
func TestForDomain_EmptyDomain_ReturnsError(t *testing.T) {
	key, err := ForDomain("")
	require.Error(t, err)
	assert.Empty(t, key)
}

// TestForDomain_WhitespaceOnlyDomain_ReturnsError mirrors the empty-string
// case for a domain that is present but carries no real content.
func TestForDomain_WhitespaceOnlyDomain_ReturnsError(t *testing.T) {
	key, err := ForDomain("   ")
	require.Error(t, err)
	assert.Empty(t, key)
}

// TestForDomain_MalformedDomain_ReturnsError proves a domain outside
// domainPattern's character set (here: internal whitespace and a
// disallowed symbol) is rejected rather than passed through as-is.
func TestForDomain_MalformedDomain_ReturnsError(t *testing.T) {
	cases := []string{
		"audience score system",
		"audience_score_system!",
		" audience_score_system",
		"audience_score_system ",
	}
	for _, domain := range cases {
		key, err := ForDomain(domain)
		require.Errorf(t, err, "domain %q should be rejected as malformed", domain)
		assert.Empty(t, key)
	}
}

// TestForDomain_ValidDomain_ReturnsTheDomainItself pins ForDomain's
// derivation as the identity mapping over a well-formed domain (this
// file's own doc comment on ForDomain explains why) -- a future change to
// a non-identity derivation must update this test deliberately, not by
// accident.
func TestForDomain_ValidDomain_ReturnsTheDomainItself(t *testing.T) {
	key, err := ForDomain("audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, "audience_score_system", key)
}
