package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsAllowed_NilOrEmptyAllowsEverything(t *testing.T) {
	assert.True(t, isAllowed("search", nil))
	assert.True(t, isAllowed("search", []string{}))
}

func TestIsAllowed_NonEmptyRestrictsToListedNames(t *testing.T) {
	allowed := []string{"search", "read_note"}

	assert.True(t, isAllowed("search", allowed))
	assert.True(t, isAllowed("read_note", allowed))
	assert.False(t, isAllowed("write_note", allowed))
	assert.False(t, isAllowed("", allowed))
}

func TestAllowAll(t *testing.T) {
	assert.True(t, allowAll(nil))
	assert.True(t, allowAll([]string{}))
	assert.False(t, allowAll([]string{"search"}))
}

// TestIsUnlocked_NilOrEmptyUnlocksNothing is isUnlocked's deliberate
// opposite of isAllowed's "nil/empty means no restriction" convention
// (FR9): a search-mode session that has not called search_tools yet must
// not be able to dispatch anything beyond search_tools itself.
func TestIsUnlocked_NilOrEmptyUnlocksNothing(t *testing.T) {
	assert.False(t, isUnlocked("read_note", nil))
	assert.False(t, isUnlocked("read_note", []string{}))
}

func TestIsUnlocked_NonEmptyRestrictsToListedNames(t *testing.T) {
	unlocked := []string{"search", "read_note"}

	assert.True(t, isUnlocked("search", unlocked))
	assert.True(t, isUnlocked("read_note", unlocked))
	assert.False(t, isUnlocked("write_note", unlocked))
	assert.False(t, isUnlocked("", unlocked))
}
