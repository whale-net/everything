// Package loadtest builds NFR3.3-class fixtures and measures server-side
// latency against a stated p95 budget, run as a Bazel target (NFR3.3: "a
// load harness run as a Bazel target"). It is deliberately reusable across
// gates rather than single-purpose: NFR3.3's own gate (household landing,
// this task) and the Phase 3 series gate this task's issue names ("This
// harness is reused by the Phase 3 series gate") share one fixture builder
// and one percentile-measurement routine, selecting between them only by
// which named Scenario they run -- neither gate re-derives its own fixture
// shape or its own percentile arithmetic.
//
// Scaffold state (this task, #1350): Scenario and HouseholdLanding below
// are complete -- the fixture shape and its cardinality arithmetic do not
// depend on anything not yet built. Scenario.Run is nil until
// Implementation wires GetHouseholdLanding into leaflab/api/server.go: the
// harness has no endpoint to call yet. p95_test.go's TestHouseholdLandingP95
// skips with an explanatory message until Run is supplied.
package loadtest

import (
	"context"
	"testing"
	"time"
)

// Scenario names one latency-gated fixture and target this harness can
// run: how many boards/sensors/months of readings to seed, and the p95
// budget the measured Run must clear.
type Scenario struct {
	// Name identifies this scenario in test output and -- once the Phase 3
	// series gate adds its own Scenario value -- in flag/CLI selection
	// ("build it to take a named scenario", this task's issue body).
	Name string

	// Boards is the fixture's board count.
	Boards int
	// SensorsPerBoard is the fixture's sensor count per board.
	SensorsPerBoard int
	// ReadingIntervalMinutes is the cadence, in minutes, at which each
	// sensor's fixture readings are spaced.
	ReadingIntervalMinutes int
	// MonthsOfReadings is how much reading history (in 30-day months, see
	// ReadingCount) the fixture seeds per sensor.
	MonthsOfReadings int

	// P95Budget is the maximum acceptable p95 latency this scenario's Run
	// may report, measured server-side -- excluding network transport and
	// browser render time (NFR3.3).
	P95Budget time.Duration

	// Run executes this scenario's endpoint call the stated number of
	// times against a seeded database and returns the measured p95
	// latency. nil until the endpoint under test has a real handler to
	// call -- see this package's doc comment.
	Run func(ctx context.Context, tb testing.TB) time.Duration
}

// ReadingCount returns how many sensor_reading rows s's fixture inserts in
// total: Boards * SensorsPerBoard readings, each spaced
// ReadingIntervalMinutes apart across MonthsOfReadings 30-day months.
func (s Scenario) ReadingCount() int64 {
	const hoursPerDay = 24
	const daysPerMonth = 30
	minutesPerMonth := int64(daysPerMonth) * hoursPerDay * 60
	readingsPerSensor := int64(s.MonthsOfReadings) * minutesPerMonth / int64(s.ReadingIntervalMinutes)
	return int64(s.Boards) * int64(s.SensorsPerBoard) * readingsPerSensor
}

// HouseholdLanding is NFR3.3's own fixture and gate: "10 boards, 6 sensors
// each, 12 months of readings at one reading per sensor per 5 minutes ...
// Household landing view at most 500 ms p95."
var HouseholdLanding = Scenario{
	Name:                   "household_landing",
	Boards:                 10,
	SensorsPerBoard:        6,
	ReadingIntervalMinutes: 5,
	MonthsOfReadings:       12,
	P95Budget:              500 * time.Millisecond,
}
