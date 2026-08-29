//go:build integration

// Real-Postgres integration coverage for migration 035_config_derived_from's
// append-only enforcement (FR40, NFR6.2) -- the half of this task's Testing
// section that can only be proven against the real migration file, not a
// hermetic test schema: the BEFORE UPDATE OR DELETE trigger genuinely
// blocks UPDATE of every payload/identity column and DELETE, while leaving
// the device-ack columns (accepted, acked_at, rejection_reason) updatable,
// and the composite (board_id, derived_from_version) self-FK genuinely
// refuses a cross-board reference. FR40's own restore-guarantee round trip
// (byte-identical stored payload, per-board dispatch, audit) is covered at
// the handler/repository level in
// //leaflab/api:rollback_device_config_integration_test, which composes
// Repository.InsertDeviceConfigNextVersion/GetConfigVersionRow directly
// against a hand-rolled schema mirroring this migration's device_config
// shape -- see that file's own doc comment for why the split follows
// audit_log_migration_integration_test.go's own precedent (trigger tested
// here, handler behavior tested at the api level).
//
// Reuses ownership_migration_integration_test.go's package-level
// migrations/timescaleImage fixtures -- same file-sharing rationale as
// audit_log_migration_integration_test.go. See //libs/go/dbtest/README.md
// for how to run tests like this one.
package main_test

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// newPreConfigDerivedFromDB migrates up through 034 (the last migration
// before 035_config_derived_from) and returns the runner (still positioned
// at 034) plus a pool for fixture setup and assertions.
func newPreConfigDerivedFromDB(t *testing.T) (*migrate.Runner, *dbtest.Postgres) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	t.Cleanup(cancel)

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: timescaleImage})

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Migrate(34); err != nil {
		t.Fatalf("migrate to version 34 (pre-config-derived-from): %v", err)
	}

	return runner, db
}

// insertConfigDerivedFromBoard/insertConfigDerivedFromVersion are minimal
// fixture helpers shared by every test in this file.
func insertConfigDerivedFromBoard(t *testing.T, db *dbtest.Postgres, deviceID string) int64 {
	t.Helper()
	var boardID int64
	if err := db.Pool.QueryRow(context.Background(),
		`INSERT INTO board (device_id) VALUES ($1) RETURNING board_id`, deviceID,
	).Scan(&boardID); err != nil {
		t.Fatalf("insert board %s: %v", deviceID, err)
	}
	return boardID
}

func insertConfigDerivedFromVersion(t *testing.T, db *dbtest.Postgres, boardID, version int64) int64 {
	t.Helper()
	var configID int64
	if err := db.Pool.QueryRow(context.Background(), `
		INSERT INTO device_config (board_id, version, config_json)
		VALUES ($1, $2, '{}'::jsonb)
		RETURNING config_id
	`, boardID, version).Scan(&configID); err != nil {
		t.Fatalf("insert device_config board=%d version=%d: %v", boardID, version, err)
	}
	return configID
}

// TestConfigDerivedFromMigration_AppendOnlyTriggerBlocksPayloadUpdateAndDelete
// is this task's load-bearing append-only assertion: after migration 035
// applies, UPDATE of any payload/identity column (config_json here) and
// DELETE both fail, and the row is left completely unchanged by the
// refused UPDATE -- matching audit_log_migration_integration_test.go's own
// "the row must still be exactly as inserted" check.
func TestConfigDerivedFromMigration_AppendOnlyTriggerBlocksPayloadUpdateAndDelete(t *testing.T) {
	runner, db := newPreConfigDerivedFromDB(t)
	ctx := context.Background()

	boardID := insertConfigDerivedFromBoard(t, db, "device-append-only")
	configID := insertConfigDerivedFromVersion(t, db, boardID, 1)

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 035: %v", err)
	}

	if _, err := db.Pool.Exec(ctx, `UPDATE device_config SET config_json = '{"tampered":true}'::jsonb WHERE config_id = $1`, configID); err == nil {
		t.Error("UPDATE device_config.config_json succeeded, want it refused (NFR6.2 append-only)")
	}
	if _, err := db.Pool.Exec(ctx, `UPDATE device_config SET version = 999 WHERE config_id = $1`, configID); err == nil {
		t.Error("UPDATE device_config.version succeeded, want it refused (NFR6.2 append-only)")
	}
	if _, err := db.Pool.Exec(ctx, `DELETE FROM device_config WHERE config_id = $1`, configID); err == nil {
		t.Error("DELETE FROM device_config succeeded, want it refused (NFR6.2 append-only)")
	}

	var configJSON []byte
	var version int64
	if err := db.Pool.QueryRow(ctx, `SELECT config_json, version FROM device_config WHERE config_id = $1`, configID).Scan(&configJSON, &version); err != nil {
		t.Fatalf("read back device_config row after refused UPDATE/DELETE: %v", err)
	}
	if string(configJSON) != "{}" {
		t.Errorf("config_json = %s after a refused UPDATE, want unchanged %q", configJSON, "{}")
	}
	if version != 1 {
		t.Errorf("version = %d after a refused UPDATE, want unchanged 1", version)
	}
}

// TestConfigDerivedFromMigration_AckColumnsRemainUpdatable is this task's
// named exception: the device-ack path (leaflab/processor's AckDeviceConfig)
// must still be able to flip accepted/acked_at/rejection_reason on an
// existing row after migration 035's trigger is in place.
func TestConfigDerivedFromMigration_AckColumnsRemainUpdatable(t *testing.T) {
	runner, db := newPreConfigDerivedFromDB(t)
	ctx := context.Background()

	boardID := insertConfigDerivedFromBoard(t, db, "device-ack-updatable")
	configID := insertConfigDerivedFromVersion(t, db, boardID, 1)

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 035: %v", err)
	}

	tag, err := db.Pool.Exec(ctx, `
		UPDATE device_config SET accepted = TRUE, acked_at = NOW(), rejection_reason = NULL
		WHERE config_id = $1
	`, configID)
	if err != nil {
		t.Fatalf("UPDATE of ack columns failed, want it permitted: %v", err)
	}
	if tag.RowsAffected() != 1 {
		t.Fatalf("ack-column UPDATE affected %d rows, want 1", tag.RowsAffected())
	}

	var accepted bool
	if err := db.Pool.QueryRow(ctx, `SELECT accepted FROM device_config WHERE config_id = $1`, configID).Scan(&accepted); err != nil {
		t.Fatalf("read back accepted: %v", err)
	}
	if !accepted {
		t.Error("accepted = false after the ack-column UPDATE, want true")
	}

	// A rejection is the ack path's other legitimate shape: accepted stays
	// FALSE and rejection_reason is set instead.
	if _, err := db.Pool.Exec(ctx, `
		UPDATE device_config SET accepted = FALSE, acked_at = NOW(), rejection_reason = 'firmware refused entry'
		WHERE config_id = $1
	`, configID); err != nil {
		t.Fatalf("UPDATE of ack columns (rejection shape) failed, want it permitted: %v", err)
	}
}

// TestConfigDerivedFromMigration_DerivedFromVersionCrossBoardRefused proves
// the composite (board_id, derived_from_version) self-FK this migration
// adds: a new version can only ever record a derived_from_version that
// exists on its *own* board, never another board's version number.
func TestConfigDerivedFromMigration_DerivedFromVersionCrossBoardRefused(t *testing.T) {
	runner, db := newPreConfigDerivedFromDB(t)
	ctx := context.Background()

	boardA := insertConfigDerivedFromBoard(t, db, "device-cross-board-a")
	boardB := insertConfigDerivedFromBoard(t, db, "device-cross-board-b")
	insertConfigDerivedFromVersion(t, db, boardA, 1) // boardA has a version 1; boardB does not

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 035: %v", err)
	}

	// boardB rolling back to "version 1" must fail: version 1 exists, but
	// on boardA, not boardB. The new row's own version is deliberately 5,
	// not 1 -- using 1 here would let the FK trigger's existence check
	// match the row being inserted against itself (board_id=boardB,
	// version=1 would itself satisfy a lookup for (boardB, 1)), masking
	// the cross-board case this test exists to catch.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json, derived_from_version)
		VALUES ($1, 5, '{}'::jsonb, 1)
	`, boardB); err == nil {
		t.Error("insert with derived_from_version pointing at another board's version succeeded, want the composite self-FK to refuse it")
	}

	// The same reference, scoped to boardA itself, must succeed.
	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO device_config (board_id, version, config_json, derived_from_version)
		VALUES ($1, 2, '{}'::jsonb, 1)
	`, boardA); err != nil {
		t.Errorf("insert with derived_from_version pointing at this same board's own version failed, want it permitted: %v", err)
	}
}

// TestConfigDerivedFromMigration_DownReversesCleanly and
// TestConfigDerivedFromMigration_UpDownUpIsIdempotentSafe mirror the
// Validation discipline 016_audit_log's own migration tests apply: up and
// down both clean, and re-running up after down is safe against a fresh
// database.
func TestConfigDerivedFromMigration_DownReversesCleanly(t *testing.T) {
	runner, db := newPreConfigDerivedFromDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("apply migration 035: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 035: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'device_config' AND column_name = 'derived_from_version')
	`).Scan(&exists); err != nil {
		t.Fatalf("check derived_from_version column after down: %v", err)
	}
	if exists {
		t.Error("device_config.derived_from_version still exists after reversing migration 035")
	}
}

func TestConfigDerivedFromMigration_UpDownUpIsIdempotentSafe(t *testing.T) {
	runner, db := newPreConfigDerivedFromDB(t)
	ctx := context.Background()

	if err := runner.Steps(1); err != nil {
		t.Fatalf("first apply of migration 035: %v", err)
	}
	if err := runner.Steps(-1); err != nil {
		t.Fatalf("reverse migration 035: %v", err)
	}
	if err := runner.Steps(1); err != nil {
		t.Fatalf("second apply of migration 035: %v", err)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM information_schema.columns WHERE table_name = 'device_config' AND column_name = 'derived_from_version')
	`).Scan(&exists); err != nil {
		t.Fatalf("check derived_from_version column after up-down-up: %v", err)
	}
	if !exists {
		t.Error("device_config.derived_from_version does not exist after up-down-up, want it re-created by the second apply")
	}
}
