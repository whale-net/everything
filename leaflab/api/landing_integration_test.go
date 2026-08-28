//go:build integration

// Real-Postgres integration coverage for FR62/NFR3.1's household landing
// classification (#1350) that the pure/fake-repo tests in
// leaflab/api/landing/classify_test.go and landing_glue_test.go can't
// reach:
//
//   - FR22.4's retired-board exclusion actually happening in SQL
//     (landingBoardSignalsQuery's `WHERE ... retired_at IS NULL`), not just
//     assumed by landingSignalsForHousehold's caller-already-filtered
//     contract.
//   - NFR3.1's "bounded, constant number of queries independent of the
//     number of boards" as a real round-trip count against a real pgx pool,
//     not just a doc comment.
//   - GetHouseholdLanding's condition 5 (no household) end to end through
//     the real RPC handler and a real Postgres household_membership table.
//
// Conditions 1-3 (sensor silent / board silent / household silent) are
// exercised here via Repository.LandingBoardSignals +
// landingSignalsForHousehold + landing.Classify directly, the same
// production call sequence GetHouseholdLanding's handler uses (see
// landing.go), rather than through the full RPC. Reason: GetHouseholdLanding
// unconditionally calls isServiceDegraded first, which (with the nil
// rmqConn every integration test in this package uses -- no RabbitMQ
// container here) always reports DEGRADED, which is condition 4's own
// higher-priority match and would mask 1-3 before Classify ever saw them.
// Condition 4 itself, and condition 5, do not have this problem: condition
// 5 returns before isServiceDegraded is ever called, and condition 4 *is*
// isServiceDegraded's real signal, faithfully reproduced by a real,
// disconnected pool/rmqConn -- see TestGetHouseholdLanding_ServiceDegraded_RealProbe.
//
// Schema is self-contained hand-written DDL, mirroring
// admin_elevation_integration_test.go's household/board/device_config/
// sensor shape (this file adds sensor.last_seen_at, which that file's
// tests never needed) plus household_membership -- deliberately not shared
// with any other integration test file's schema, same rationale as those
// files' own doc comments.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:landing_integration_test --test_output=all
package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/claim"
	"github.com/whale-net/everything/leaflab/api/landing"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/ratelimit"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const landingTestSchema = `
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
	CREATE INDEX idx_landing_membership_principal_current
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
`

// newLandingTestRepository starts a real Postgres container, applies
// landingTestSchema, and returns a *Repository plus the raw pool for
// fixture setup / assertions.
func newLandingTestRepository(t *testing.T) (*Repository, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: landingTestSchema})
	return NewRepository(db.Pool), db.Pool
}

// newLandingTestServer is newLandingTestRepository plus a real
// LeafLabAPIServer wired to it -- rmqConn/publisher nil, same as
// dbtest_helpers_integration_test.go's newTestServer, so
// isServiceDegraded's mqUp check is always false (see this file's doc
// comment for why that's a feature, not a bug, for condition 4/5 tests).
func newLandingTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	repo, pool := newLandingTestRepository(t)
	server := NewLeafLabAPIServer(repo, stubAuthz{}, nil, nil, discardLogger(), claim.DefaultConfig, ratelimit.NewInMemoryLimiter(nil))
	return server, pool
}

func landingCtxFor(subject string) context.Context {
	return grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: subject})
}

func insertLandingHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household (name) VALUES ('') RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func insertLandingMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`, householdID, subject); err != nil {
		t.Fatalf("insert membership: %v", err)
	}
}

// landingConfigJSON returns an accepted device_config payload with one
// sensor configured at pollIntervalMs -- mirrors
// server_fleet_health_test.go's fleetConfigJSON, duplicated here rather
// than shared since that file isn't part of this go_test target's srcs
// (this file's own go_test rule lists only its own sources, matching this
// package's other integration test files' convention).
func landingConfigJSON(t *testing.T, pollIntervalMs uint32) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(`{"sensors":[{"pollIntervalMs":%d}]}`, pollIntervalMs))
}

// insertLandingBoard inserts a board (optionally retired) with an accepted
// device_config and n sensors, all timestamped lastSeen (board, config
// push, and every sensor's last_seen_at) -- callers move individual
// sensor/board timestamps with direct SQL after this returns when a test
// needs a stale sub-fixture.
func insertLandingBoard(t *testing.T, pool *pgxpool.Pool, householdID int64, deviceID string, lastSeen time.Time, retired bool, sensorCount int) int64 {
	t.Helper()
	ctx := context.Background()
	var retiredAt any
	if retired {
		retiredAt = lastSeen
	}
	var boardID int64
	if err := pool.QueryRow(ctx,
		`INSERT INTO board (device_id, household_id, last_seen_at, retired_at) VALUES ($1, $2, $3, $4) RETURNING board_id`,
		deviceID, householdID, lastSeen, retiredAt).Scan(&boardID); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	if _, err := pool.Exec(ctx,
		`INSERT INTO device_config (board_id, version, config_json, accepted) VALUES ($1, 1, $2, TRUE)`,
		boardID, landingConfigJSON(t, 60000)); err != nil {
		t.Fatalf("insert device_config for board %s: %v", deviceID, err)
	}
	for i := 0; i < sensorCount; i++ {
		if _, err := pool.Exec(ctx,
			`INSERT INTO sensor (board_id, last_seen_at) VALUES ($1, $2)`, boardID, lastSeen); err != nil {
			t.Fatalf("insert sensor %d for board %s: %v", i, deviceID, err)
		}
	}
	return boardID
}

// -- FR22.4: retired boards excluded from LandingBoardSignals ---------------

// TestLandingBoardSignals_RetiredBoardsExcluded is the issue's "retired
// boards excluded from condition 3": a household with one stale retired
// board and one fresh non-retired board must classify Healthy, not
// HouseholdSilent -- the retired board's staleness is invisible to
// LandingBoardSignals entirely, not merely down-weighted.
func TestLandingBoardSignals_RetiredBoardsExcluded(t *testing.T) {
	repo, pool := newLandingTestRepository(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	household := insertLandingHousehold(t, pool)
	freshBoard := insertLandingBoard(t, pool, household, "fresh-board", now, false, 1)
	retiredBoard := insertLandingBoard(t, pool, household, "retired-stale-board", now.Add(-24*time.Hour), true, 1)

	rows, err := repo.LandingBoardSignals(ctx, household)
	if err != nil {
		t.Fatalf("LandingBoardSignals: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("got %d board signal rows, want 1 (retired board must be excluded entirely): %+v", len(rows), rows)
	}
	if rows[0].BoardID != freshBoard {
		t.Errorf("got board_id %d, want the fresh board %d -- retired board %d leaked into the result set", rows[0].BoardID, freshBoard, retiredBoard)
	}

	in := landingSignalsForHousehold(rows, false, now)
	if in.HouseholdWhollySilent {
		t.Error("HouseholdWhollySilent = true, want false -- the household's only non-retired board is fresh")
	}
	if got := landing.Classify(in).Condition; got != landing.ConditionHealthy {
		t.Errorf("Classify(%+v).Condition = %v, want ConditionHealthy (retired board excluded from FR62 condition 3)", in, got)
	}
}

// TestLandingBoardSignals_HouseholdOfOnlyRetiredBoards_Healthy is the
// degenerate case: every board in the household is retired, so
// LandingBoardSignals returns zero rows -- landingSignalsForHousehold's own
// doc comment says this state is "outside FR62's five conditions", reported
// Healthy, not HouseholdSilent (a household with nothing left to monitor is
// not "every board silent").
func TestLandingBoardSignals_HouseholdOfOnlyRetiredBoards_Healthy(t *testing.T) {
	repo, pool := newLandingTestRepository(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Millisecond)

	household := insertLandingHousehold(t, pool)
	insertLandingBoard(t, pool, household, "only-retired-board", now.Add(-24*time.Hour), true, 1)

	rows, err := repo.LandingBoardSignals(ctx, household)
	if err != nil {
		t.Fatalf("LandingBoardSignals: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("got %d rows, want 0 -- the household's only board is retired", len(rows))
	}

	in := landingSignalsForHousehold(rows, false, now)
	if got := landing.Classify(in).Condition; got != landing.ConditionHealthy {
		t.Errorf("Classify(%+v).Condition = %v, want ConditionHealthy", in, got)
	}
}

// -- FR62's five conditions, real fixtures -----------------------------------

// TestGetHouseholdLanding_FiveConditions_RealFixtures drives
// Repository.LandingBoardSignals -> landingSignalsForHousehold ->
// landing.Classify -- the exact sequence GetHouseholdLanding's handler
// uses -- against real Postgres fixtures isolating each of FR62's
// conditions 1-3, plus the healthy baseline. See this file's doc comment
// for why conditions 1-3 are driven this way rather than through the full
// RPC (isServiceDegraded's nil-rmqConn always-degraded signal would mask
// them).
func TestGetHouseholdLanding_FiveConditions_RealFixtures(t *testing.T) {
	now := time.Now().Truncate(time.Millisecond)
	staleThreshold := now.Add(-1 * time.Hour) // configured poll interval floors to A23's 15-minute floor

	cases := []struct {
		name string
		seed func(t *testing.T, pool *pgxpool.Pool, household int64)
		want landing.Condition
	}{
		{
			name: "healthy: all boards and sensors fresh",
			seed: func(t *testing.T, pool *pgxpool.Pool, household int64) {
				insertLandingBoard(t, pool, household, "healthy-board", now, false, 2)
			},
			want: landing.ConditionHealthy,
		},
		{
			name: "condition 1: sensor silent, board still reports",
			seed: func(t *testing.T, pool *pgxpool.Pool, household int64) {
				boardID := insertLandingBoard(t, pool, household, "sensor-silent-board", now, false, 2)
				// Age exactly one of the two sensors past A23 staleness; the
				// board itself (and the other sensor) stay fresh.
				var sensorID int64
				if err := pool.QueryRow(context.Background(),
					`SELECT sensor_id FROM sensor WHERE board_id = $1 ORDER BY sensor_id LIMIT 1`, boardID).Scan(&sensorID); err != nil {
					t.Fatalf("select sensor: %v", err)
				}
				if _, err := pool.Exec(context.Background(),
					`UPDATE sensor SET last_seen_at = $1 WHERE sensor_id = $2`, staleThreshold, sensorID); err != nil {
					t.Fatalf("age sensor: %v", err)
				}
			},
			want: landing.ConditionSensorSilent,
		},
		{
			name: "condition 2: one board wholly silent, another board fresh",
			seed: func(t *testing.T, pool *pgxpool.Pool, household int64) {
				insertLandingBoard(t, pool, household, "silent-board", staleThreshold, false, 1)
				insertLandingBoard(t, pool, household, "reporting-board", now, false, 1)
			},
			want: landing.ConditionBoardSilent,
		},
		{
			name: "condition 3: every board in the household silent",
			seed: func(t *testing.T, pool *pgxpool.Pool, household int64) {
				insertLandingBoard(t, pool, household, "silent-board-a", staleThreshold, false, 1)
				insertLandingBoard(t, pool, household, "silent-board-b", staleThreshold, false, 1)
			},
			want: landing.ConditionHouseholdSilent,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			repo, pool := newLandingTestRepository(t)
			household := insertLandingHousehold(t, pool)
			c.seed(t, pool, household)

			rows, err := repo.LandingBoardSignals(context.Background(), household)
			if err != nil {
				t.Fatalf("LandingBoardSignals: %v", err)
			}
			in := landingSignalsForHousehold(rows, false, now)
			if got := landing.Classify(in).Condition; got != c.want {
				t.Errorf("Classify(%+v).Condition = %v, want %v (rows=%+v)", in, got, c.want, rows)
			}
		})
	}
}

// -- Condition 4: the real isServiceDegraded probe --------------------------

// TestGetHouseholdLanding_ServiceDegraded_RealProbe drives the full RPC
// handler (not the glue-layer shortcut the five-conditions test above
// uses): with a healthy household but no RabbitMQ connection (this
// package's integration tests never carry one), isServiceDegraded reports
// DEGRADED for real, and GetHouseholdLanding must classify condition 4 --
// proving the RPC handler actually threads that real signal into Classify,
// not just landingSignalsForHousehold in isolation.
func TestGetHouseholdLanding_ServiceDegraded_RealProbe(t *testing.T) {
	server, pool := newLandingTestServer(t)
	household := insertLandingHousehold(t, pool)
	insertLandingBoard(t, pool, household, "healthy-board", time.Now(), false, 1)
	insertLandingMembership(t, pool, household, "degraded-caller")

	resp, err := server.GetHouseholdLanding(landingCtxFor("degraded-caller"), &pb.GetHouseholdLandingRequest{})
	if err != nil {
		t.Fatalf("GetHouseholdLanding: %v", err)
	}
	if resp.GetCondition() != pb.LandingCondition_LANDING_CONDITION_SERVICE_DEGRADED {
		t.Errorf("Condition = %v, want LANDING_CONDITION_SERVICE_DEGRADED (no RabbitMQ connection in this test)", resp.GetCondition())
	}
	if resp.GetSentenceKey() == "" {
		t.Error("SentenceKey is empty -- FR62: never a blank page")
	}
}

// -- Condition 5: no household -----------------------------------------------

// TestGetHouseholdLanding_NoHousehold_NextStepNotBlank is the issue's
// "Condition 5 for a principal with no household -- and the response
// contains a next step, not an empty body", through the full RPC handler
// and a real Postgres household_membership table (no row for this
// principal at all).
func TestGetHouseholdLanding_NoHousehold_NextStepNotBlank(t *testing.T) {
	server, _ := newLandingTestServer(t)

	resp, err := server.GetHouseholdLanding(landingCtxFor("nobody-has-invited-me"), &pb.GetHouseholdLandingRequest{})
	if err != nil {
		t.Fatalf("GetHouseholdLanding: %v", err)
	}
	if resp.GetCondition() != pb.LandingCondition_LANDING_CONDITION_NO_HOUSEHOLD {
		t.Fatalf("Condition = %v, want LANDING_CONDITION_NO_HOUSEHOLD", resp.GetCondition())
	}
	if resp.GetSentenceKey() == "" {
		t.Error("SentenceKey is empty -- FR62: never a blank page")
	}
	if len(resp.GetNextSteps()) == 0 {
		t.Fatal("NextSteps is empty -- FR62: never a blank page, must carry a named next step")
	}
	for _, step := range resp.GetNextSteps() {
		if step.GetLabel() == "" || step.GetPath() == "" {
			t.Errorf("next step %+v has an empty label or path", step)
		}
	}
}

// -- NFR3.1: bounded, constant query count -----------------------------------

// queryCounter is a pgx.QueryTracer that counts every query round trip
// issued through a pool it's attached to -- the "counting interceptor on
// the pool" this task's Implementation section names as the assertable
// value NFR3.1's bounded-query claim needs. TraceQueryEnd is a no-op:
// TraceQueryStart already fires exactly once per query, which is all a
// round-trip count needs.
type queryCounter struct {
	n int
}

func (c *queryCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.n++
	return ctx
}

func (c *queryCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// newTracedLandingPool opens a second pool against the same database
// connString as an already-provisioned dbtest.Postgres, with counter
// attached as its pgx.QueryTracer -- dbtest.NewPostgres itself builds its
// Pool with pgxpool.New (no tracer hook), so query counting needs its own
// pool built via pgxpool.ParseConfig/NewWithConfig against the identical
// connection string.
func newTracedLandingPool(t *testing.T, connString string, counter *queryCounter) *pgxpool.Pool {
	t.Helper()
	ctx := context.Background()
	cfg, err := pgxpool.ParseConfig(connString)
	if err != nil {
		t.Fatalf("parse pool config: %v", err)
	}
	cfg.ConnConfig.Tracer = counter
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("open traced pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// TestLandingBoardSignals_QueryCount_ConstantAcrossHouseholdSize is the
// issue's literal "Query-count test: landing view issues the same number of
// queries for 1 board and for 50 boards" -- landingBoardSignalsQuery
// aggregates every board and its sensors in one SQL statement
// (landing.go's doc comment), so LandingBoardSignals must issue exactly the
// same number of round trips (one) regardless of household size.
func TestLandingBoardSignals_QueryCount_ConstantAcrossHouseholdSize(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: landingTestSchema})
	setupPool := db.Pool

	smallHousehold := insertLandingHousehold(t, setupPool)
	insertLandingBoard(t, setupPool, smallHousehold, "small-board-1", time.Now(), false, 6)

	largeHousehold := insertLandingHousehold(t, setupPool)
	for i := 0; i < 50; i++ {
		insertLandingBoard(t, setupPool, largeHousehold, fmt.Sprintf("large-board-%d", i), time.Now(), false, 6)
	}

	smallCounter := &queryCounter{}
	smallPool := newTracedLandingPool(t, db.ConnString, smallCounter)
	smallRepo := NewRepository(smallPool)
	smallRows, err := smallRepo.LandingBoardSignals(ctx, smallHousehold)
	if err != nil {
		t.Fatalf("LandingBoardSignals(small): %v", err)
	}
	if len(smallRows) != 1 {
		t.Fatalf("small household: got %d rows, want 1", len(smallRows))
	}

	largeCounter := &queryCounter{}
	largePool := newTracedLandingPool(t, db.ConnString, largeCounter)
	largeRepo := NewRepository(largePool)
	largeRows, err := largeRepo.LandingBoardSignals(ctx, largeHousehold)
	if err != nil {
		t.Fatalf("LandingBoardSignals(large): %v", err)
	}
	if len(largeRows) != 50 {
		t.Fatalf("large household: got %d rows, want 50", len(largeRows))
	}

	if smallCounter.n != 1 {
		t.Errorf("1-board household issued %d queries, want exactly 1", smallCounter.n)
	}
	if largeCounter.n != 1 {
		t.Errorf("50-board household issued %d queries, want exactly 1", largeCounter.n)
	}
	if smallCounter.n != largeCounter.n {
		t.Errorf("query count depends on household size: 1 board -> %d queries, 50 boards -> %d queries, want identical (NFR3.1)", smallCounter.n, largeCounter.n)
	}
}
