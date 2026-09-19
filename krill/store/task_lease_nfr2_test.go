// NFR2 structural check (issue #2723's own Validation item "task_lease_event
// has no UPDATE or DELETE path anywhere in krill/"): task_lease.go -- the
// only file in this repository that writes to task_lease_event -- must
// never contain an UPDATE, DELETE, or TRUNCATE statement naming that table.
// No database needed: this reads the package's own source file back
// (declared as `data` on this test's BUILD.bazel target), mirroring
// milestone_status_nfr2_test.go's own technique. Part of `bazel test //...`
// (no "manual"/"integration" tag).
package store_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// forbiddenTaskLeaseSQLVerbs are the SQL statement shapes NFR2 forbids
// against task_lease_event: an UPDATE or a DELETE targeting this table
// would mutate or erase heartbeat history in place.
var forbiddenTaskLeaseSQLVerbs = []string{"UPDATE task_lease_event", "DELETE FROM task_lease_event", "DELETE task_lease_event", "TRUNCATE task_lease_event"}

// TestTaskLease_SourceNeverIssuesUpdateOrDeleteSQL_NFR2 proves
// task_lease.go never issues an UPDATE/DELETE/TRUNCATE against
// task_lease_event -- every heartbeat is a new row, never a mutation of a
// prior one.
func TestTaskLease_SourceNeverIssuesUpdateOrDeleteSQL_NFR2(t *testing.T) {
	src, err := os.ReadFile("task_lease.go")
	if err != nil {
		t.Fatalf("read task_lease.go back (is it declared as `data` on this go_test target?): %v", err)
	}
	body := strings.ToUpper(string(src))
	for _, forbidden := range forbiddenTaskLeaseSQLVerbs {
		assert.NotContains(t, body, strings.ToUpper(forbidden),
			"task_lease.go must never issue %q -- NFR2: task_lease_event is append-only, a row once written is never mutated or removed", forbidden)
	}
}
