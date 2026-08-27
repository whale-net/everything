//go:build integration

// Real-Postgres integration coverage for FR1.2/FR1.3's push-time
// live-reference invariant against the actual SQL authz.PGResolver runs
// (the recursive region-household CTE in particular) -- not just the Go
// dispatch logic server_push_device_config_test.go already covers against
// fakeAuthz/fakeRepo. This is the task's named main-today defect
// reproduction over real SQL: "an authenticated owner can point their own
// sensor at another household's region" via PushDeviceConfig, closed by
// validatePushRegions (server.go) calling authz.AssertSameHousehold before
// anything is stored.
//
// Schema is self-contained hand-written DDL (household / board /
// device_config / region), deliberately not shared with
// authz_scope_integration_test.go's authzTestSchema (that file is its own
// go_test target; duplicating the handful of lines this file needs keeps
// each target's srcs independent, per this package's existing convention
// -- see dbtest_helpers_integration_test.go's own doc comment on why
// testSchema isn't shared with authzTestSchema either). Shared fixtures
// this file does reuse (authedCtx, countRows, discardLogger) live in
// dbtest_helpers_integration_test.go.
//
// A full same-household *success* through PushDeviceConfig cannot be
// exercised here: publisher is nil (no real-RabbitMQ test double exists in
// this repo -- see libs/go/rmq/publisher_test.go, which skips those
// cases), so the same-household case below calls validatePushRegions
// directly instead of the full RPC, proving the real SQL resolves a
// same-household region without refusing it. See //libs/go/dbtest's
// README for how to run integration tests like this one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:push_device_config_invariant_integration_test --test_output=all
package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	configpb "github.com/whale-net/everything/firmware/proto/config"
	"github.com/whale-net/everything/leaflab/api/authz"
	"github.com/whale-net/everything/leaflab/api/contract"
	pb "github.com/whale-net/everything/leaflab/api/proto"
	"github.com/whale-net/everything/libs/go/dbtest"
)

// pushInvariantTestSchema is the board/household/region/device_config
// shape validatePushRegions' real query path touches: board.household_id
// (FR1.1's nullable current-value cache) and region.household_id (only the
// tree root carries a real value, per migration 015_ownership's
// enforce_region_household_root trigger -- this file's fixtures only ever
// insert root regions, so the recursive-CTE walk-to-root is exercised
// trivially, but it is the same query authz.PGResolver.resolveRegion
// actually runs, not a hand-rolled substitute).
const pushInvariantTestSchema = `
	CREATE TABLE household (
		household_id BIGSERIAL PRIMARY KEY
	);

	CREATE TABLE region (
		region_id        BIGSERIAL PRIMARY KEY,
		parent_region_id BIGINT REFERENCES region(region_id),
		household_id     BIGINT REFERENCES household(household_id)
	);

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
`

func newPushInvariantTestServer(t *testing.T) (*LeafLabAPIServer, *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: pushInvariantTestSchema})
	repo := NewRepository(db.Pool)
	resolver := authz.NewPGResolver(db.Pool)
	return NewLeafLabAPIServer(repo, resolver, nil, nil, nil, discardLogger()), db.Pool
}

func insertPushInvariantHousehold(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO household DEFAULT VALUES RETURNING household_id`).Scan(&id); err != nil {
		t.Fatalf("insert household: %v", err)
	}
	return id
}

func insertPushInvariantBoard(t *testing.T, pool *pgxpool.Pool, deviceID string, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id, household_id) VALUES ($1, $2) RETURNING board_id`,
		deviceID, householdID).Scan(&id); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return id
}

func insertPushInvariantRootRegion(t *testing.T, pool *pgxpool.Pool, householdID int64) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(context.Background(),
		`INSERT INTO region (household_id) VALUES ($1) RETURNING region_id`, householdID).Scan(&id); err != nil {
		t.Fatalf("insert region for household %d: %v", householdID, err)
	}
	return id
}

// TestPushDeviceConfig_ForeignHouseholdRegion_RefusedInvalidArgument_WritesNothing_RealDB
// is this task's core defect reproduction over real SQL: a board in
// household A pushes a config naming a region_id that resolves (through
// authz.PGResolver's real recursive-CTE query) to household B. Before this
// task, nothing on the push path checked this at all. FR1.3 requires the
// whole push refused as invalid_argument naming the offending entry/field,
// with zero device_config rows written.
func TestPushDeviceConfig_ForeignHouseholdRegion_RefusedInvalidArgument_WritesNothing_RealDB(t *testing.T) {
	server, pool := newPushInvariantTestServer(t)
	ctx := authedCtx()

	householdA := insertPushInvariantHousehold(t, pool)
	householdB := insertPushInvariantHousehold(t, pool)
	insertPushInvariantBoard(t, pool, "device-a", householdA)
	regionB := insertPushInvariantRootRegion(t, pool, householdB)

	_, err := server.PushDeviceConfig(ctx, &pb.PushDeviceConfigRequest{
		DeviceId: "device-a",
		Sensors: []*configpb.SensorConfig{
			{Name: "sensor-1", RegionId: uint32(regionB)},
		},
	})
	if err == nil {
		t.Fatal("PushDeviceConfig referencing a foreign household's region_id returned nil error, want a refusal")
	}
	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatalf("error %v carries no Failure detail", err)
	}
	if detail.Class != string(contract.FailureInvalidArgument) {
		t.Errorf("Class = %q, want %q", detail.Class, contract.FailureInvalidArgument)
	}
	if detail.Entity != string(authz.EntityRegion) {
		t.Errorf("Entity = %q, want %q", detail.Entity, authz.EntityRegion)
	}
	if detail.Field != "sensors[0].region_id" {
		t.Errorf("Field = %q, want %q", detail.Field, "sensors[0].region_id")
	}

	if got := countRows(t, pool, "device_config"); got != 0 {
		t.Errorf("device_config rows after refused push = %d, want 0 (FR1.3: refusal must write nothing)", got)
	}
}

// TestValidatePushRegions_SameHouseholdRegion_PassesAgainstRealSQL proves
// the real authz.PGResolver query does not refuse a region_id that
// resolves to the pushing board's own household -- the companion
// "not over-refused" case, exercised directly against validatePushRegions
// (see file doc comment for why the full RPC can't be driven all the way
// through success without a real RabbitMQ).
func TestValidatePushRegions_SameHouseholdRegion_PassesAgainstRealSQL(t *testing.T) {
	server, pool := newPushInvariantTestServer(t)
	ctx := authedCtx()

	householdA := insertPushInvariantHousehold(t, pool)
	regionA := insertPushInvariantRootRegion(t, pool, householdA)

	err := server.validatePushRegions(ctx, householdA, []*configpb.SensorConfig{
		{Name: "sensor-1", RegionId: uint32(regionA)},
	})
	if err != nil {
		t.Errorf("validatePushRegions for a same-household region_id returned an error against real SQL, want nil: %v", err)
	}
}
