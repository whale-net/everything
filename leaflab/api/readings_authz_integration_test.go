//go:build integration

// Real-Postgres integration coverage for this task's Testing section
// (#1362): "A non-member gets NFR2's not-found for another household's
// sensor." Exercises GetReadingSeries's full authorizeEntity path
// (server.go) against a real authz.PGResolver and a real household /
// household_membership / board / sensor schema -- the same NFR2
// "not-found, never a distinguishable out-of-scope response" contract
// authz_scope_integration_test.go already proves for ListBoards/
// GetDeviceConfig, applied here to the bounded-read-path RPCs'
// authorizeEntity gate (server.go) instead.
//
// Schema is self-contained hand-written DDL, deliberately separate from
// both dbtest_helpers_integration_test.go's testSchema (no household
// concept) and authzTestSchema (no sensor/sensor_type) -- see this
// package's other integration test files' doc comments for why each
// keeps its own hermetic schema rather than sharing one that would carry
// tables unrelated tests don't need.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:readings_authz_integration_test --test_output=all
package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/leaflab/api/readings"
	"github.com/whale-net/everything/leaflab/api/tiers"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/grpcauth"
)

const readingsAuthzTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE household_membership (
		membership_id     BIGSERIAL PRIMARY KEY,
		household_id      BIGINT NOT NULL REFERENCES household(household_id),
		principal_subject TEXT NOT NULL,
		valid_from        TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to          TIMESTAMPTZ
	);
	CREATE INDEX idx_household_membership_current
		ON household_membership(principal_subject) WHERE valid_to IS NULL;

	CREATE TABLE board (
		board_id     BIGSERIAL PRIMARY KEY,
		household_id BIGINT REFERENCES household(household_id)
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE
	);
	INSERT INTO sensor_type (name) VALUES ('temperature');

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id),
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id)
	);
`

// stubReadingsReader panics if any method is ever called -- used by the
// non-member test below to prove authorizeEntity refuses the request
// before the readings package is ever reached, mirroring the same
// "refusal writes/queries nothing" discipline as
// TestPushDeviceConfig_RefusalWritesNothing.
type stubReadingsReader struct {
	fixedSeries readings.SeriesResult
}

func (s stubReadingsReader) Series(ctx context.Context, entity authz.EntityRef, window readings.Window, measurementTypeID int64, requested tiers.Tier, page readings.Page) (readings.SeriesResult, error) {
	if s.fixedSeries.Points == nil && s.fixedSeries.NextPageToken == "" && s.fixedSeries.Tier.Tier == "" {
		panic("stubReadingsReader.Series called -- authorizeEntity should have refused this request first")
	}
	return s.fixedSeries, nil
}

func (s stubReadingsReader) CurrentValues(ctx context.Context, entity authz.EntityRef) (readings.CurrentValuesResult, error) {
	panic("stubReadingsReader.CurrentValues called -- not exercised by this file's tests")
}

func (s stubReadingsReader) PeriodSummary(ctx context.Context, regionID int64, period readings.Window, measurementTypeID int64) (readings.PeriodSummaryResult, error) {
	panic("stubReadingsReader.PeriodSummary called -- not exercised by this file's tests")
}

func (s stubReadingsReader) Compare(ctx context.Context, entities []authz.EntityRef, window readings.Window, measurementTypeID int64, requested tiers.Tier, page readings.Page) (readings.CompareResult, error) {
	panic("stubReadingsReader.Compare called -- not exercised by this file's tests")
}

func newReadingsAuthzTestServer(t *testing.T, readingsSvc readingsReader) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: readingsAuthzTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, readingsSvc, nil, nil, nil, discardLogger()), db.Pool
}

func insertReadingsHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func insertReadingsMembership(t *testing.T, pool *pgxpool.Pool, householdID int64, subject string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO household_membership (household_id, principal_subject) VALUES ($1, $2)`,
		householdID, subject); err != nil {
		t.Fatalf("insert membership for %q: %v", subject, err)
	}
}

func insertScopedBoardForReadings(t *testing.T, pool *pgxpool.Pool, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (household_id) VALUES ($1) RETURNING board_id`, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board: %v", err)
	}
	return id
}

func insertScopedSensor(t *testing.T, pool *pgxpool.Pool, boardID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO sensor (board_id, sensor_type_id) VALUES ($1, 1) RETURNING sensor_id`, boardID).Scan(&id); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}
	return id
}

// TestGetReadingSeries_NonMemberGetsNotFound_ForAnotherHouseholdsSensor
// proves this task's Testing-section requirement: a caller who is not a
// member of the household owning a sensor gets the exact same
// contract.NotFound failure a nonexistent sensor_id would produce (NFR2),
// and the readings package is never even queried -- stubReadingsReader
// panics if it is.
func TestGetReadingSeries_NonMemberGetsNotFound_ForAnotherHouseholdsSensor(t *testing.T) {
	server, pool := newReadingsAuthzTestServer(t, stubReadingsReader{})

	householdA := insertReadingsHousehold(t, pool)
	insertReadingsMembership(t, pool, householdA, "alice")
	boardA := insertScopedBoardForReadings(t, pool, householdA)
	sensorA := insertScopedSensor(t, pool, boardA)

	// "mallory" belongs to no household at all -- the sharpest non-member
	// case (FR5.1's "no household" empty-scope posture), distinct from
	// merely belonging to a *different* household.
	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "mallory"})

	// Window is deliberately left nil -- authorizeEntity's refusal happens
	// before windowFromProto is even consulted, so this proves NFR2's gate
	// fires first, not merely that some other validation happened to catch
	// this request.
	_, err := server.GetReadingSeries(ctx, &pb.GetReadingSeriesRequest{
		Entity: &pb.EntityRef{Entity: &pb.EntityRef_SensorId{SensorId: sensorA}},
	})
	if err == nil {
		t.Fatal("GetReadingSeries for a non-member's sensor returned nil error, want NFR2's not-found")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureNotFound) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureNotFound)
	}
	if detail.Entity != "sensor" {
		t.Errorf("Entity = %q, want %q", detail.Entity, "sensor")
	}
}

// TestGetReadingSeries_HouseholdMemberSucceeds_ControlCase is the control
// case for the test above: a genuine member of the sensor's own household
// is authorized (the request reaches stubReadingsReader.Series, which
// returns a fixed result rather than panicking) -- proving the non-member
// refusal above is actually gated on household membership, not some
// unconditional failure in this fixture's wiring.
func TestGetReadingSeries_HouseholdMemberSucceeds_ControlCase(t *testing.T) {
	fixedTier := tiers.Selection{Tier: tiers.TierRaw}
	server, pool := newReadingsAuthzTestServer(t, stubReadingsReader{
		fixedSeries: readings.SeriesResult{Tier: fixedTier},
	})

	householdA := insertReadingsHousehold(t, pool)
	insertReadingsMembership(t, pool, householdA, "alice")
	boardA := insertScopedBoardForReadings(t, pool, householdA)
	sensorA := insertScopedSensor(t, pool, boardA)

	ctx := grpcauth.ContextWithClaims(context.Background(), &grpcauth.Claims{Subject: "alice"})

	resp, err := server.GetReadingSeries(ctx, &pb.GetReadingSeriesRequest{
		Entity: &pb.EntityRef{Entity: &pb.EntityRef_SensorId{SensorId: sensorA}},
		Window: &pb.TimeWindow{Start: contract.Now(), End: contract.Now()},
	})
	if err != nil {
		t.Fatalf("GetReadingSeries for the sensor's own household member: %v", err)
	}
	if resp == nil {
		t.Fatal("GetReadingSeries returned a nil response with a nil error")
	}
}
