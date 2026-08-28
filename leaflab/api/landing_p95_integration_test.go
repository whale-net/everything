//go:build integration

// NFR3.3's household landing p95 gate (#1350), run against real Postgres.
//
// Architectural note, recorded here because it explains why this gate
// lives in leaflab/api rather than leaflab/loadtest despite
// leaflab/loadtest/p95_test.go being NFR3.3's originally scaffolded home
// (see that file's and scenario.go's doc comments, written at Scaffold):
// leaflab/api is a `package main` -- Go (and rules_go, which compiles
// packages the same way `go build`/`go vet` reason about them) refuses to
// import a main package from anywhere else ("is a program, not an
// importable package"). leaflab/loadtest therefore cannot construct a real
// Repository/LeafLabAPIServer to measure -- only leaflab/api's own test
// binary can, since it already *is* that package. This file is that
// binary's contribution: it imports leaflab/loadtest (a perfectly ordinary
// package -- the restriction is only one-directional) for the shared
// fixture shape (loadtest.HouseholdLanding) and percentile routine
// (loadtest.Percentile), and performs the actual measurement and gate
// assertion here.
//
// Practical effect: `bazel test //leaflab/loadtest:household_landing_p95_test`
// still reports its scaffolded skip (see p95_test.go) -- Run is a
// process-global var and this file's assignment to it would only be visible
// within *this* binary, not that separate compiled test target. The real,
// enforced gate is this file's TestHouseholdLandingP95_RealFixture,
// runnable as `bazel test //leaflab/api:landing_p95_integration_test`.
// Making the literal `//leaflab/loadtest:...` target itself report a
// non-skipped number would require moving GetHouseholdLanding's handler
// construction out of `package main` into an importable package -- an
// Implementation-phase restructuring, out of this Testing task's scope;
// noted for Validation/a future task.
//
// Measurement excludes network and browser render (NFR3.3) by construction:
// GetHouseholdLanding is called as a plain Go method call on the real
// LeafLabAPIServer, never over a dialed connection.
//
// Fixture: NFR3.3's literal text -- "10 boards, 6 sensors each, 12 months
// of readings at one reading per sensor per 5 minutes" -- taken from
// loadtest.HouseholdLanding rather than restated here, so this file and
// leaflab/loadtest/p95_test.go's TestScenarioReadingCount pin the exact same
// numbers. sensor_reading is seeded (millions of rows, UNLOGGED for seed
// speed -- this is a throwaway fixture, never data at rest) specifically to
// prove NFR3.1's "no endpoint performs an unbounded scan of the readings
// hypertable": landingBoardSignalsQuery never joins sensor_reading at all,
// so this table's size must not move the measured p95.
//
// Run it explicitly (requires a working Docker daemon; large fixture, can
// take several minutes to seed):
//
//	bazel test //leaflab/api:landing_p95_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/claim"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/leaflab/loadtest"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const landingP95TestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY,
		name         TEXT NOT NULL DEFAULT ''
	);

	CREATE TABLE household_membership (
		household_membership_id BIGSERIAL PRIMARY KEY,
		household_id             BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject        TEXT NOT NULL,
		valid_from               TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to                 TIMESTAMPTZ
	);
	CREATE INDEX idx_landing_p95_membership_principal_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		household_id  BIGINT REFERENCES household(household_id),
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ
	);

	CREATE TABLE device_config (
		config_id        BIGSERIAL   PRIMARY KEY,
		board_id         BIGINT      NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		version          BIGINT      NOT NULL,
		config_json      JSONB       NOT NULL,
		accepted         BOOLEAN     NOT NULL DEFAULT FALSE,
		pushed_at        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		acked_at         TIMESTAMPTZ,
		rejection_reason TEXT,
		UNIQUE (board_id, version)
	);

	CREATE TABLE sensor (
		sensor_id    BIGSERIAL PRIMARY KEY,
		board_id     BIGINT NOT NULL REFERENCES board(board_id) ON DELETE CASCADE,
		last_seen_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);

	-- UNLOGGED: this is a throwaway load-test fixture seeded fresh every
	-- run, never data at rest -- skipping WAL makes seeding millions of
	-- rows fast without changing anything landingBoardSignalsQuery reads
	-- (it never touches this table at all; see this file's doc comment).
	CREATE UNLOGGED TABLE sensor_reading (
		reading_id  BIGSERIAL PRIMARY KEY,
		sensor_id   BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		value       DOUBLE PRECISION NOT NULL,
		uptime_ms   INTEGER NOT NULL DEFAULT 0,
		recorded_at TIMESTAMPTZ NOT NULL
	);
	CREATE INDEX idx_landing_p95_sensor_reading_sensor_id ON sensor_reading(sensor_id, recorded_at DESC);
`

// seedLandingP95Fixture seeds exactly one household shaped by s
// (loadtest.HouseholdLanding): s.Boards boards, s.SensorsPerBoard sensors
// each (all fresh -- classification outcome doesn't matter to this gate,
// only latency), and s.ReadingCount() total sensor_reading rows spanning
// s.MonthsOfReadings months at s.ReadingIntervalMinutes spacing, generated
// server-side via generate_series (a per-row Go INSERT loop would dominate
// the seeding time at this row count). Returns the household id.
func seedLandingP95Fixture(t *testing.T, pool *pgxpool.Pool, s loadtest.Scenario) int64 {
	t.Helper()
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)

	var householdID int64
	if err := pool.QueryRow(ctx, `INSERT INTO household (name) VALUES ('') RETURNING household_id`).Scan(&householdID); err != nil {
		t.Fatalf("insert household: %v", err)
	}

	for b := 0; b < s.Boards; b++ {
		var boardID int64
		if err := pool.QueryRow(ctx,
			`INSERT INTO board (device_id, household_id, last_seen_at) VALUES ($1, $2, $3) RETURNING board_id`,
			fmt.Sprintf("p95-board-%d", b), householdID, now).Scan(&boardID); err != nil {
			t.Fatalf("insert board %d: %v", b, err)
		}
		if _, err := pool.Exec(ctx,
			`INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 1, '{"sensors":[{"pollIntervalMs":60000}]}', TRUE)`,
			boardID); err != nil {
			t.Fatalf("insert device_config for board %d: %v", b, err)
		}
		for sn := 0; sn < s.SensorsPerBoard; sn++ {
			var sensorID int64
			if err := pool.QueryRow(ctx,
				`INSERT INTO sensor (board_id, last_seen_at) VALUES ($1, $2) RETURNING sensor_id`,
				boardID, now).Scan(&sensorID); err != nil {
				t.Fatalf("insert sensor %d/%d: %v", b, sn, err)
			}

			start := now.AddDate(0, 0, -30*s.MonthsOfReadings)
			interval := fmt.Sprintf("%d minutes", s.ReadingIntervalMinutes)
			if _, err := pool.Exec(ctx, `
				INSERT INTO sensor_reading (sensor_id, value, recorded_at)
				SELECT $1, 20.0, gs
				FROM generate_series($2::timestamptz, $3::timestamptz, $4::interval) AS gs
			`, sensorID, start, now, interval); err != nil {
				t.Fatalf("seed sensor_reading for sensor %d/%d: %v", b, sn, err)
			}
		}
	}

	return householdID
}

// TestHouseholdLandingP95_RealFixture is NFR3.3's actual gate: seed
// loadtest.HouseholdLanding's fixture against real Postgres, call the real
// GetHouseholdLanding handler repeatedly, and assert
// loadtest.Percentile(measured, 0.95) <= loadtest.HouseholdLanding.P95Budget.
// See this file's doc comment for why this is the enforced gate rather than
// leaflab/loadtest's own (structurally unwireable) test.
func TestHouseholdLandingP95_RealFixture(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: landingP95TestSchema})

	scenario := loadtest.HouseholdLanding
	t.Logf("seeding NFR3.3 fixture: %d boards x %d sensors, %d months at %d-minute intervals (%d sensor_reading rows)",
		scenario.Boards, scenario.SensorsPerBoard, scenario.MonthsOfReadings, scenario.ReadingIntervalMinutes, scenario.ReadingCount())
	householdID := seedLandingP95Fixture(t, db.Pool, scenario)

	repo := NewRepository(db.Pool)
	server := NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))

	const iterations = 100
	ctxCaller := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "p95-caller"})
	req := &pb.GetHouseholdLandingRequest{HouseholdId: householdID}

	// authorizeHouseholdAccess (the non-zero household_id path) needs a
	// current membership row for this caller, same as any other RPC's
	// FR5/NFR2 scope check.
	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, "p95-caller"); err != nil {
		t.Fatalf("insert membership: %v", err)
	}

	durations := make([]time.Duration, 0, iterations)
	for i := 0; i < iterations; i++ {
		start := time.Now()
		if _, err := server.GetHouseholdLanding(ctxCaller, req); err != nil {
			t.Fatalf("GetHouseholdLanding iteration %d: %v", i, err)
		}
		durations = append(durations, time.Since(start))
	}

	p95 := loadtest.Percentile(durations, 0.95)
	t.Logf("household landing p95 = %s over %d calls (budget %s)", p95, iterations, scenario.P95Budget)
	if p95 > scenario.P95Budget {
		t.Errorf("household landing p95 = %s, want <= %s (NFR3.3)", p95, scenario.P95Budget)
	}
}
