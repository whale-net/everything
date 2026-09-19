// Implementation-phase placeholder (issue #2721) -- Assemble now has real
// logic (payload.go), so this file no longer asserts the Scaffold-phase
// stub's ErrNotImplemented contract. Full unit coverage of Assemble's
// Testing-phase criteria (issue #2721's Testing section) lands here in
// this task's Testing phase, alongside payload_integration_test.go's
// real-Postgres coverage.
package work_test

import "testing"

func TestAssemble_Placeholder(t *testing.T) {
	t.Skip("full coverage added in this task's Testing phase (issue #2721)")
}
