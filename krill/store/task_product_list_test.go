// Pure unit tests for the product-wide paged task read's own pure-Go
// pieces (task_product_list.go, FR cfcd1104): the "incomplete" container
// predicate and the SQL predicate rendered from it. No Postgres -- the
// query's real behaviour is krill/store/task_integration_test.go's job.
//
// The point of the second test is that ListProductTasks runs a SQL
// NOT IN list while IsIncompleteContainerStatus is the Go spelling of the
// same rule. Deriving the list from the same slice keeps them one rule
// rather than two; this asserts they still agree over every status the
// enumeration has, so adding a ninth status cannot quietly leave one
// spelling behind. `package store`, since incompleteContainerFilterSQL is
// unexported.
package store

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// allMilestoneStatuses is the full fixed set of MilestoneStatus values --
// the same eight migration 0xx's CHECK constraint enumerates.
var allMilestoneStatuses = []MilestoneStatus{
	MilestoneStatusNotStarted,
	MilestoneStatusInDesign,
	MilestoneStatusDesigned,
	MilestoneStatusPlanned,
	MilestoneStatusInProgress,
	MilestoneStatusShipped,
	MilestoneStatusPartiallyComplete,
	MilestoneStatusAbandoned,
}

// TestIsIncompleteContainerStatus is FR2's incomplete rule stated directly:
// only shipped and abandoned drop out, and partially complete stays -- the
// case a naive "is it finished?" reading would get wrong.
func TestIsIncompleteContainerStatus(t *testing.T) {
	for _, tc := range []struct {
		status MilestoneStatus
		want   bool
	}{
		{status: MilestoneStatusNotStarted, want: true},
		{status: MilestoneStatusInDesign, want: true},
		{status: MilestoneStatusDesigned, want: true},
		{status: MilestoneStatusPlanned, want: true},
		{status: MilestoneStatusInProgress, want: true},
		{status: MilestoneStatusPartiallyComplete, want: true},
		{status: MilestoneStatusShipped, want: false},
		{status: MilestoneStatusAbandoned, want: false},
	} {
		assert.Equal(t, tc.want, IsIncompleteContainerStatus(tc.status),
			"status %q", tc.status)
	}
}

// TestIncompleteContainerFilterSQL_MatchesTheGoPredicate is the anti-drift
// check: over every status the enumeration has, the SQL predicate the
// query runs agrees with IsIncompleteContainerStatus. Both render from
// completeContainerStatuses, so a divergence here means one of them has
// stopped reading that slice.
func TestIncompleteContainerFilterSQL_MatchesTheGoPredicate(t *testing.T) {
	sql := incompleteContainerFilterSQL("c.id")

	// Only the NOT IN list is the exclusion set. The rest of the
	// predicate legitimately names other statuses -- the COALESCE default
	// for a container with no status history is 'not started' -- so a
	// search over the whole clause would read that default as an
	// exclusion.
	_, notIn, found := strings.Cut(sql, "NOT IN (")
	require.True(t, found, "the predicate must exclude the complete statuses by a NOT IN list")
	notIn = strings.TrimSuffix(strings.TrimSpace(notIn), ")")

	for _, status := range allMilestoneStatuses {
		excluded := strings.Contains(notIn, "'"+string(status)+"'")
		assert.Equal(t, !IsIncompleteContainerStatus(status), excluded,
			"status %q: the SQL NOT IN list and the Go predicate disagree", status)
	}

	// And it is the derived-status column the row reports, never a stored
	// one, so the filter and the reported status cannot come from
	// different sources.
	assert.Contains(t, sql, "milestone_status_event")
}
