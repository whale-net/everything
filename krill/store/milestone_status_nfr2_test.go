package store_test

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// forbiddenMilestoneStatusMethodSubstrings names the verb shapes NFR2/LB3
// forbid anywhere on MilestoneStatusEventStore: a status transition is an
// addition to history, never an overwrite or removal, so no method on
// this interface -- now or later -- may look like a mutation-in-place or a
// deletion of an existing row.
var forbiddenMilestoneStatusMethodSubstrings = []string{"Update", "Delete", "Remove", "Edit", "Modify", "Patch", "Overwrite"}

// TestMilestoneStatusEventStore_InterfaceExposesOnlyAppendOnlyMethods_NFR2
// is issue #2685's Testing item 5's structural half: reflects over
// store.MilestoneStatusEventStore itself (not a fake or a mock) and
// asserts its method set is exactly the four append-only accessors
// (RecordTransition, CurrentStatus, CurrentStatuses, ListTransitions) --
// no more, no fewer -- and that none of them carries an
// update/delete-shaped name. A future change that adds a fifth method to
// this interface must update expectedMilestoneStatusEventStoreMethods
// deliberately; it cannot silently slip in an update-or-delete-shaped
// method unnoticed.
func TestMilestoneStatusEventStore_InterfaceExposesOnlyAppendOnlyMethods_NFR2(t *testing.T) {
	expected := []string{"RecordTransition", "CurrentStatus", "CurrentStatuses", "ListTransitions"}

	typ := reflect.TypeOf((*store.MilestoneStatusEventStore)(nil)).Elem()
	require.Equal(t, len(expected), typ.NumMethod(), "MilestoneStatusEventStore must expose exactly these methods -- nothing more, nothing fewer: %v", expected)

	var got []string
	for i := 0; i < typ.NumMethod(); i++ {
		name := typ.Method(i).Name
		got = append(got, name)

		for _, forbidden := range forbiddenMilestoneStatusMethodSubstrings {
			assert.NotContains(t, name, forbidden,
				"MilestoneStatusEventStore.%s looks like an update/delete method -- NFR2/LB3 forbid any such path: a transition is an addition to history, never an overwrite", name)
		}
	}
	assert.ElementsMatch(t, expected, got)
}

// forbiddenMilestoneStatusSQLVerbs are the SQL statement shapes NFR2/LB3
// forbid against milestone_status_event: an UPDATE or a DELETE targeting
// this table would mutate or erase history in place.
var forbiddenMilestoneStatusSQLVerbs = []string{"UPDATE milestone_status_event", "DELETE FROM milestone_status_event", "DELETE milestone_status_event", "TRUNCATE milestone_status_event"}

// TestMilestoneStatusStore_SourceNeverIssuesUpdateOrDeleteSQL_NFR2 is
// issue #2685's Testing item 5's other structural half, mirroring
// import_completion_nfr3_test.go's own technique (read the package's own
// source file back, declared as `data` on this test's BUILD.bazel target,
// rather than needing a database): milestone_status.go -- the only file in
// this repository that writes to `milestone_status_event` -- must never
// contain an UPDATE, DELETE, or TRUNCATE statement naming that table.
func TestMilestoneStatusStore_SourceNeverIssuesUpdateOrDeleteSQL_NFR2(t *testing.T) {
	src, err := os.ReadFile("milestone_status.go")
	if err != nil {
		t.Fatalf("read milestone_status.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := strings.ToUpper(string(src))
	for _, forbidden := range forbiddenMilestoneStatusSQLVerbs {
		assert.NotContains(t, body, strings.ToUpper(forbidden),
			"milestone_status.go must never issue %q -- NFR2/LB3: milestone_status_event is append-only, a row once written is never mutated or removed", forbidden)
	}
}
