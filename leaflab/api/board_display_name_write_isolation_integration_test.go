//go:build integration

// Real-Postgres integration coverage for this task's "make this a test, not
// a comment" bullet: Repository.SetBoardDisplayName (repository.go) writes
// only board.display_name -- device_config, sensor, region and
// sensor_region_history are byte-for-byte untouched by the call. Unlike
// //leaflab/migrate:board_display_name_migration_integration_test (which
// proves the same property by issuing the equivalent raw SQL against the
// full real migration set), this file calls the actual production
// Repository.SetBoardDisplayName method -- its own schema
// (boardDisplayNameWriteIsolationTestSchema) is hand-written and hermetic
// like dbtest_helpers_integration_test.go's testSchema, but wider: it adds
// sensor_type/sensor/region/sensor_region_history, which testSchema
// deliberately omits (see that file's doc comment), specifically so this
// property can be checked against real Repository code without depending on
// the full migration set. See //libs/go/dbtest's README for how to run
// tests like this one.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //leaflab/api:board_display_name_write_isolation_integration_test --test_output=all
package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

const boardDisplayNameWriteIsolationTestSchema = `
	CREATE TABLE board (
		board_id      BIGSERIAL PRIMARY KEY,
		device_id     VARCHAR(64) NOT NULL UNIQUE,
		registered_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		last_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		retired_at    TIMESTAMPTZ,
		display_name  TEXT
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

	CREATE TABLE region (
		region_id BIGSERIAL PRIMARY KEY,
		name      VARCHAR(255) NOT NULL
	);

	CREATE TABLE sensor_type (
		sensor_type_id BIGSERIAL PRIMARY KEY,
		name           VARCHAR(64) NOT NULL UNIQUE
	);

	CREATE TABLE sensor (
		sensor_id      BIGSERIAL PRIMARY KEY,
		board_id       BIGINT NOT NULL REFERENCES board(board_id) ON DELETE RESTRICT,
		sensor_type_id BIGINT NOT NULL REFERENCES sensor_type(sensor_type_id),
		region_id      BIGINT REFERENCES region(region_id) ON DELETE RESTRICT,
		name           VARCHAR(128) NOT NULL,
		unit           VARCHAR(16) NOT NULL,
		UNIQUE (board_id, name)
	);

	CREATE TABLE sensor_region_history (
		history_id BIGSERIAL PRIMARY KEY,
		sensor_id  BIGINT NOT NULL REFERENCES sensor(sensor_id) ON DELETE RESTRICT,
		region_id  BIGINT NOT NULL REFERENCES region(region_id) ON DELETE RESTRICT,
		valid_from TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to   TIMESTAMPTZ
	);

	CREATE TABLE audit_log (
		audit_id             BIGSERIAL PRIMARY KEY,
		actor_subject        TEXT NOT NULL,
		actor_kind           TEXT NOT NULL,
		target_household_id  BIGINT NULL,
		action                TEXT NOT NULL,
		entity_kind           TEXT NOT NULL,
		entity_id             TEXT NULL,
		reason                TEXT NULL,
		occurred_at           TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		correlation_id        TEXT NULL
	);
`

// displayNameWriteIsolationFixture is one board plus one row in each table
// TestSetBoardDisplayName_WriteTouchesOnlyDisplayNameColumn snapshots before
// and after the write.
type displayNameWriteIsolationFixture struct {
	boardID int64
}

func seedDisplayNameWriteIsolationFixture(t *testing.T, pool *pgxpool.Pool) displayNameWriteIsolationFixture {
	t.Helper()
	ctx := context.Background()

	var f displayNameWriteIsolationFixture
	if err := pool.QueryRow(ctx, `INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, "write-isolation-board").Scan(&f.boardID); err != nil {
		t.Fatalf("insert board: %v", err)
	}

	if _, err := pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json) VALUES ($1, $2, $3)
	`, f.boardID, 1, `{}`); err != nil {
		t.Fatalf("insert device_config: %v", err)
	}

	var regionID int64
	if err := pool.QueryRow(ctx, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "write-isolation-region").Scan(&regionID); err != nil {
		t.Fatalf("insert region: %v", err)
	}

	var sensorTypeID int64
	if err := pool.QueryRow(ctx, `INSERT INTO sensor_type (name) VALUES ($1) RETURNING sensor_type_id`, "write-isolation-sensor-type").Scan(&sensorTypeID); err != nil {
		t.Fatalf("insert sensor_type: %v", err)
	}

	var sensorID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO sensor (board_id, sensor_type_id, region_id, name, unit) VALUES ($1, $2, $3, $4, $5) RETURNING sensor_id
	`, f.boardID, sensorTypeID, regionID, "write-isolation-sensor", "degC").Scan(&sensorID); err != nil {
		t.Fatalf("insert sensor: %v", err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO sensor_region_history (sensor_id, region_id) VALUES ($1, $2)`, sensorID, regionID); err != nil {
		t.Fatalf("insert sensor_region_history: %v", err)
	}

	return f
}

func writeIsolationTableSnapshot(t *testing.T, pool *pgxpool.Pool, table string) string {
	t.Helper()
	var snap *string
	if err := pool.QueryRow(context.Background(), "SELECT string_agg(t::text, '|' ORDER BY t::text) FROM "+table+" t").Scan(&snap); err != nil {
		t.Fatalf("snapshot %s: %v", table, err)
	}
	if snap == nil {
		return ""
	}
	return *snap
}

// TestSetBoardDisplayName_WriteTouchesOnlyDisplayNameColumn calls the real
// Repository.SetBoardDisplayName and proves it changes only
// board.display_name: device_config, sensor, region and
// sensor_region_history are unchanged, and board's own other columns
// (device_id, registered_at, last_seen_at, retired_at) are unchanged too.
func TestSetBoardDisplayName_WriteTouchesOnlyDisplayNameColumn(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: boardDisplayNameWriteIsolationTestSchema})
	repo := NewRepository(db.Pool)

	f := seedDisplayNameWriteIsolationFixture(t, db.Pool)

	unaffectedTables := []string{"device_config", "sensor", "region", "sensor_region_history"}
	before := make(map[string]string, len(unaffectedTables))
	for _, table := range unaffectedTables {
		before[table] = writeIsolationTableSnapshot(t, db.Pool, table)
	}
	var boardBefore string
	if err := db.Pool.QueryRow(ctx, `
		SELECT (device_id, registered_at, last_seen_at, retired_at)::text FROM board WHERE board_id = $1
	`, f.boardID).Scan(&boardBefore); err != nil {
		t.Fatalf("snapshot board's other columns (before): %v", err)
	}

	if err := repo.SetBoardDisplayName(ctx, f.boardID, "Living Room Board", testAuditEntry()); err != nil {
		t.Fatalf("SetBoardDisplayName: %v", err)
	}

	for _, table := range unaffectedTables {
		if got := writeIsolationTableSnapshot(t, db.Pool, table); got != before[table] {
			t.Errorf("%s changed after SetBoardDisplayName -- FR57 requires this write touch only board.display_name\nbefore: %s\nafter:  %s", table, before[table], got)
		}
	}
	var boardAfter string
	if err := db.Pool.QueryRow(ctx, `
		SELECT (device_id, registered_at, last_seen_at, retired_at)::text FROM board WHERE board_id = $1
	`, f.boardID).Scan(&boardAfter); err != nil {
		t.Fatalf("snapshot board's other columns (after): %v", err)
	}
	if boardAfter != boardBefore {
		t.Errorf("board's other columns changed after SetBoardDisplayName: before=%q after=%q", boardBefore, boardAfter)
	}

	var displayName *string
	if err := db.Pool.QueryRow(ctx, `SELECT display_name FROM board WHERE board_id = $1`, f.boardID).Scan(&displayName); err != nil {
		t.Fatalf("read back display_name: %v", err)
	}
	if displayName == nil || *displayName != "Living Room Board" {
		t.Errorf("display_name = %v, want %q", displayName, "Living Room Board")
	}
}
