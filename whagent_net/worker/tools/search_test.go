package tools

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestSearchToolsNameLiteral guards against an accidental rename of the
// reserved constant -- ListToolDefinitions' enforcement (FR8) and any
// domain server's own tool naming both depend on this exact literal.
func TestSearchToolsNameLiteral(t *testing.T) {
	assert.Equal(t, "search_tools", SearchToolsName)
}
