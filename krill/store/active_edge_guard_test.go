package store_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// rawEdgeRead matches a SELECT-side reference to the raw entity_milestone
// table (FROM/JOIN, with or without a schema qualifier). Readers must use
// entity_milestone_active; only DELETE ... FROM entity_milestone (writers)
// is exempt.
var rawEdgeRead = regexp.MustCompile(`(?i)\b(from|join)\s+(\w+\.)?entity_milestone\b`)

// TestEntityMilestoneReaders_UseActiveEdgeView fails if any store source
// reads the raw entity_milestone table instead of entity_milestone_active,
// which would resurface withdrawn edges.
func TestEntityMilestoneReaders_UseActiveEdgeView(t *testing.T) {
	files, err := filepath.Glob("*.go")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		require.NoError(t, err)
		for i, line := range strings.Split(string(src), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if !rawEdgeRead.MatchString(line) {
				continue
			}
			if regexp.MustCompile(`(?i)\bdelete\s+from\b`).MatchString(line) {
				continue
			}
			t.Errorf("%s:%d reads raw entity_milestone; use entity_milestone_active: %s", f, i+1, strings.TrimSpace(line))
		}
	}
}
