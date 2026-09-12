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
