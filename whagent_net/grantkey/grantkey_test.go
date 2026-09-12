package grantkey

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestForScope_IsDeterministic proves ForScope(s) returns the exact same
// key across repeated calls -- issue #2424's Testing section, first bullet.
func TestForScope_IsDeterministic(t *testing.T) {
	first, err := ForScope("audience_score_system")
	require.NoError(t, err)

	second, err := ForScope("audience_score_system")
	require.NoError(t, err)

	assert.Equal(t, first, second)
}

// TestForScope_DifferentScopesNeverCollide proves two different scopes
// never derive the same grant key -- FR4/NFR2's "one grant key, one
// scope" guarantee holds at the derivation function itself. Uses the
// same two example scopes issue #2424's Testing section names.
func TestForScope_DifferentScopesNeverCollide(t *testing.T) {
	ass, err := ForScope("audience_score_system")
	require.NoError(t, err)

	manmanv2, err := ForScope("manmanv2")
	require.NoError(t, err)

	assert.NotEqual(t, ass, manmanv2)
}

// TestForScope_EmptyScope_ReturnsError proves an empty scope never
// silently produces a key -- a set-but-empty AgentDefinition.Scope must
// fail loudly rather than collapsing onto some default grant.
func TestForScope_EmptyScope_ReturnsError(t *testing.T) {
	key, err := ForScope("")
	require.Error(t, err)
	assert.Empty(t, key)
}

// TestForScope_WhitespaceOnlyScope_ReturnsError mirrors the empty-string
// case for a scope that is present but carries no real content.
func TestForScope_WhitespaceOnlyScope_ReturnsError(t *testing.T) {
	key, err := ForScope("   ")
	require.Error(t, err)
	assert.Empty(t, key)
}

// TestForScope_MalformedScope_ReturnsError proves a scope outside
// scopePattern's character set (here: internal whitespace and a
// disallowed symbol) is rejected rather than passed through as-is.
func TestForScope_MalformedScope_ReturnsError(t *testing.T) {
	cases := []string{
		"audience score system",
		"audience_score_system!",
		" audience_score_system",
		"audience_score_system ",
	}
	for _, scope := range cases {
		key, err := ForScope(scope)
		require.Errorf(t, err, "scope %q should be rejected as malformed", scope)
		assert.Empty(t, key)
	}
}

// TestForScope_ValidScope_ReturnsTheScopeItself pins ForScope's
// derivation as the identity mapping over a well-formed scope (this
// file's own doc comment on ForScope explains why) -- a future change to
// a non-identity derivation must update this test deliberately, not by
// accident.
func TestForScope_ValidScope_ReturnsTheScopeItself(t *testing.T) {
	key, err := ForScope("audience_score_system")
	require.NoError(t, err)
	assert.Equal(t, "audience_score_system", key)
}
