//go:build integration

// This file only builds under the "integration" build tag (see
// //libs/go/dbtest's README and krill/slice/query_integration_test.go for
// the pattern) -- real-Postgres coverage of Assemble's Testing-phase
// criteria (issue #2721's Testing section: NFR4's byte-equal slice
// regression test, the empty-dependency-list case, and the rest) lands
// here in this task's Testing phase. Scaffold-phase placeholder only.
//
// Run it explicitly once populated (requires a working Docker daemon):
//
//	bazel test //krill/work:payload_integration_test --test_output=all
package work_test

import "testing"

func TestAssemble_Integration_Placeholder(t *testing.T) {
	t.Skip("full coverage added in this task's Testing phase (issue #2721)")
}
