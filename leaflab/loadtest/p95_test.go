//go:build integration

// p95_test.go is NFR3.3's load harness -- run as a Bazel target
// (`bazel test //leaflab/loadtest:...`), reused by the Phase 3 series gate
// via a different Scenario value. Build-tagged `integration` (gazelle skips
// it when scanning for go_test srcs) and hand-written, mirroring
// leaflab/api's Real-Postgres integration test targets and
// libs/go/dbtest:postgres_constraints_test -- this file will seed a real
// Postgres once Scenario.Run is wired at Implementation.
package loadtest

import "testing"

// TestScenarioReadingCount pins HouseholdLanding's fixture cardinality
// arithmetic (Scenario.ReadingCount) against NFR3.3's literal fixture text
// -- "10 boards, 6 sensors each, 12 months of readings at one reading per
// sensor per 5 minutes" -- so a later change to ReadingCount's arithmetic
// that silently drifts the seeded fixture size is caught here, independent
// of the (not yet wired) p95 measurement below.
func TestScenarioReadingCount(t *testing.T) {
	const wantReadingsPerSensor = 12 * 30 * 24 * 60 / 5 // 12 months, 30-day months, one reading per 5 minutes
	want := int64(10*6) * int64(wantReadingsPerSensor)
	if got := HouseholdLanding.ReadingCount(); got != want {
		t.Errorf("HouseholdLanding.ReadingCount() = %d, want %d", got, want)
	}
}

// TestHouseholdLandingP95 is NFR3.3's actual gate: seed the fixture, call
// the household landing endpoint the stated number of times, and assert
// p95 <= HouseholdLanding.P95Budget, measured server-side. Skipped until
// Implementation wires GetHouseholdLanding into leaflab/api/server.go and
// supplies HouseholdLanding.Run -- see this package's doc comment.
func TestHouseholdLandingP95(t *testing.T) {
	if HouseholdLanding.Run == nil {
		t.Skip("scaffold: GetHouseholdLanding has no handler yet -- Implementation supplies HouseholdLanding.Run and this gate starts asserting the p95 budget")
	}

	p95 := HouseholdLanding.Run(t.Context(), t)
	if p95 > HouseholdLanding.P95Budget {
		t.Errorf("household landing p95 = %s, want <= %s (NFR3.3)", p95, HouseholdLanding.P95Budget)
	}
	t.Logf("household landing p95 = %s (budget %s)", p95, HouseholdLanding.P95Budget)
}
