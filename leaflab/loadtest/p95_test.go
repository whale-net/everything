//go:build integration

// p95_test.go is NFR3.3's load harness scaffold -- see this package's own
// doc comment (scenario.go) for the fixture shape and Percentile
// (percentile.go) for the shared percentile routine.
//
// Testing-phase update (#1350): HouseholdLanding.Run is, and will remain,
// permanently nil in *this* package's own test binary -- not a temporary
// scaffold state waiting on a later phase. Reason: leaflab/api (home of
// Repository/LeafLabAPIServer/GetHouseholdLanding) is a `package main`, and
// Go refuses to import a main package from anywhere else ("is a program,
// not an importable package") -- confirmed with `go vet` against this
// exact import during #1350's Testing phase. leaflab/loadtest therefore
// cannot construct a real server to measure, no matter which later phase
// tries to wire Run.
//
// NFR3.3's gate is enforced instead in leaflab/api's own integration test
// suite, which already *is* that package and can import this one (the
// restriction is one-directional): see
// leaflab/api/landing_p95_integration_test.go's
// TestHouseholdLandingP95_RealFixture, runnable via
// `bazel test //leaflab/api:landing_p95_integration_test`. That file
// imports HouseholdLanding and Percentile from here, so the fixture shape
// and percentile arithmetic are still defined exactly once, in this
// package, as originally intended -- only the measurement and gate
// assertion live elsewhere. Making the literal
// `bazel test //leaflab/loadtest:...` target itself report a non-skipped
// p95 would require moving GetHouseholdLanding's handler construction out
// of `package main` into an importable package -- an Implementation-phase
// restructuring, out of Testing's scope; noted for Validation/a future
// task.
//
// Build-tagged `integration` (gazelle skips it when scanning for go_test
// srcs) and hand-written, mirroring leaflab/api's Real-Postgres integration
// test targets and libs/go/dbtest:postgres_constraints_test.
package loadtest

import "testing"

// TestScenarioReadingCount pins HouseholdLanding's fixture cardinality
// arithmetic (Scenario.ReadingCount) against NFR3.3's literal fixture text
// -- "10 boards, 6 sensors each, 12 months of readings at one reading per
// sensor per 5 minutes" -- so a later change to ReadingCount's arithmetic
// that silently drifts the seeded fixture size is caught here, independent
// of leaflab/api's own p95 measurement (see this file's doc comment).
func TestScenarioReadingCount(t *testing.T) {
	const wantReadingsPerSensor = 12 * 30 * 24 * 60 / 5 // 12 months, 30-day months, one reading per 5 minutes
	want := int64(10*6) * int64(wantReadingsPerSensor)
	if got := HouseholdLanding.ReadingCount(); got != want {
		t.Errorf("HouseholdLanding.ReadingCount() = %d, want %d", got, want)
	}
}

// TestHouseholdLandingP95 documents, and guards, this package's own
// structural limit (see this file's doc comment): HouseholdLanding.Run is
// permanently nil here, so this always skips in this binary. The real gate
// is leaflab/api/landing_p95_integration_test.go's
// TestHouseholdLandingP95_RealFixture.
func TestHouseholdLandingP95(t *testing.T) {
	if HouseholdLanding.Run == nil {
		t.Skip("structural: leaflab/loadtest cannot import leaflab/api (a `package main`) to construct a real server -- the enforced NFR3.3 gate is leaflab/api/landing_p95_integration_test.go's TestHouseholdLandingP95_RealFixture (bazel test //leaflab/api:landing_p95_integration_test); see this file's doc comment")
	}

	p95 := HouseholdLanding.Run(t.Context(), t)
	if p95 > HouseholdLanding.P95Budget {
		t.Errorf("household landing p95 = %s, want <= %s (NFR3.3)", p95, HouseholdLanding.P95Budget)
	}
	t.Logf("household landing p95 = %s (budget %s)", p95, HouseholdLanding.P95Budget)
}
