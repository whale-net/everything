//go:build integration

// Real-migration round-trip coverage for migration 044 (M7 FR2, plan #2777,
// task #2808): proves migration 044 itself (not a paraphrase) applies on top
// of the full prior history, backfills every pre-existing backups row to
// trigger_source = 'unknown' -- never guessing 'manual' or 'scheduled' for
// history that predates the column -- enforces the CHECK constraint, and
// drops the column cleanly on down.
//
// Same precedent as migration_041_integration_test.go/
// migration_043_integration_test.go: build-tagged `integration`, drives the
// real embedded migrations FS through //libs/go/migrate's Runner, run
// explicitly with Docker. This target compiles only its own srcs (main.go +
// this file), so it carries its own copy of the seed/open helpers rather
// than sharing another migration test's.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_044_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
	"github.com/whale-net/everything/manmanv2/migrate/schema"
)

// openMigrateTestDB044 mirrors migration_041_integration_test.go's helper --
// this target compiles only its own srcs, so it carries its own copy.
func openMigrateTestDB044(t *testing.T, db *dbtest.Postgres) *sql.DB {
	t.Helper()

	sqlDB, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("sql.Open: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	if err := sqlDB.Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return sqlDB
}

// seedBackupFixture044 walks the full parent chain a backups row requires
// (games -> game_configs -> servers -> server_game_configs -> sessions) and
// inserts a single pre-044 backups row, returning its backup_id.
func seedBackupFixture044(ctx context.Context, t *testing.T, db *dbtest.Postgres, name string) int64 {
	t.Helper()

	var gameID, configID, serverID, sgcID, sessionID, backupID int64
	seed := func(q string, dest *int64, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, q, args...).Scan(dest); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seed(`INSERT INTO games (name) VALUES ($1) RETURNING game_id`, &gameID, name+"-game")
	seed(`INSERT INTO game_configs (game_id, name, image) VALUES ($1, $2, 'image') RETURNING config_id`, &configID, gameID, name+"-config")
	seed(`INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, &serverID, name+"-server")
	seed(`INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, &sgcID, serverID, configID)
	seed(`INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, &sessionID, sgcID)
	seed(`INSERT INTO backups (session_id, server_game_config_id) VALUES ($1, $2) RETURNING backup_id`, &backupID, sessionID, sgcID)
	return backupID
}

// TestMigration044_BackfillsExistingRowsToUnknownAndEnforcesCheck proves
// migration 044 applies cleanly on top of the full prior history, lands at
// version 44, backfills a backups row that existed *before* the migration
// ran to trigger_source = 'unknown' (the DEFAULT clause backfill, not just
// new-row behavior -- FR2's explicit "never guess manual/scheduled for
// history" rule, and acceptance criteria 1-2), and that the CHECK
// constraint accepts 'scheduled'/'manual' but rejects any other value.
func TestMigration044_BackfillsExistingRowsToUnknownAndEnforcesCheck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB044(t, db)

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 46 {
		t.Fatalf("expected the latest migration source version to be 46, got %d -- update this test if a newer migration has since landed", latest)
	}

	// Apply everything up to but excluding 044, seed a pre-existing backups
	// row, then apply 044 -- this is what actually proves the DEFAULT clause
	// backfills existing rows rather than just new inserts picking it up.
	if err := runner.Migrate(43); err != nil {
		t.Fatalf("Migrate(43) (applying every migration up to but excluding 044): %v", err)
	}

	preExistingBackupID := seedBackupFixture044(ctx, t, db, "m044-pre-existing")

	if err := runner.Migrate(44); err != nil {
		t.Fatalf("Migrate(44) (applying migration 044): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(44), got dirty")
	}
	if version != 44 {
		t.Fatalf("expected version 44 after Migrate(44), got %d", version)
	}

	var triggerSource string
	if err := db.Pool.QueryRow(ctx,
		`SELECT trigger_source FROM backups WHERE backup_id = $1`, preExistingBackupID,
	).Scan(&triggerSource); err != nil {
		t.Fatalf("query pre-existing backup's trigger_source: %v", err)
	}
	if triggerSource != "unknown" {
		t.Fatalf("expected pre-existing backup's trigger_source = %q (FR2: never guess manual/scheduled for history), got %q", "unknown", triggerSource)
	}

	// The CHECK constraint rejects any value outside the three allowed ones.
	if _, err := db.Pool.Exec(ctx,
		`UPDATE backups SET trigger_source = 'bogus' WHERE backup_id = $1`, preExistingBackupID,
	); err == nil {
		t.Fatal("expected an invalid trigger_source value to be rejected by the CHECK constraint, it succeeded")
	}

	// Every valid trigger_source value is accepted.
	for _, source := range []string{"scheduled", "manual", "unknown"} {
		if _, err := db.Pool.Exec(ctx,
			`UPDATE backups SET trigger_source = $2 WHERE backup_id = $1`, preExistingBackupID, source,
		); err != nil {
			t.Fatalf("expected trigger_source = %q to be accepted, got: %v", source, err)
		}
	}

	// A brand-new insert with no explicit trigger_source also lands on
	// 'unknown' -- the DEFAULT applies to future inserts too, not only the
	// backfill of pre-existing rows.
	newBackupID := seedBackupFixture044(ctx, t, db, "m044-post-migration")
	var newTriggerSource string
	if err := db.Pool.QueryRow(ctx,
		`SELECT trigger_source FROM backups WHERE backup_id = $1`, newBackupID,
	).Scan(&newTriggerSource); err != nil {
		t.Fatalf("query new backup's trigger_source: %v", err)
	}
	if newTriggerSource != "unknown" {
		t.Fatalf("expected new backup's default trigger_source = %q, got %q", "unknown", newTriggerSource)
	}
}

// TestMigration044_DownDropsColumnCleanly proves migration 044's down
// migration drops the trigger_source column without disturbing sibling
// backup data, and that re-applying up restores it defaulted to 'unknown'.
func TestMigration044_DownDropsColumnCleanly(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB044(t, db)

	runner := migrate.NewRunner(sqlDB, schema.Migrations, schema.Dir)
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (full history): %v", err)
	}

	backupID := seedBackupFixture044(ctx, t, db, "m044-roundtrip")

	// Roll back to exactly one below 044 (rather than a relative Steps()
	// count, which breaks every time another migration lands on top of
	// 044) -- this is what keeps this round-trip testing 044's own down
	// migration specifically, regardless of how many later migrations now
	// exist.
	if err := runner.Migrate(43); err != nil {
		t.Fatalf("Migrate(43) (rolling back 044): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after rolling back to 43, got dirty")
	}
	if version != 43 {
		t.Fatalf("expected version 43 after Migrate(43), got %d", version)
	}

	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'backups' AND column_name = 'trigger_source'
		)`).Scan(&exists); err != nil {
		t.Fatalf("check trigger_source column after down: %v", err)
	}
	if exists {
		t.Fatalf("expected trigger_source column to be dropped by the down migration")
	}

	// Up again: column restored with the default, sibling data untouched.
	if err := runner.Migrate(44); err != nil {
		t.Fatalf("Migrate(44) (re-apply): %v", err)
	}
	var triggerSource string
	var sessionID int64
	if err := db.Pool.QueryRow(ctx,
		`SELECT session_id, trigger_source FROM backups WHERE backup_id = $1`, backupID,
	).Scan(&sessionID, &triggerSource); err != nil {
		t.Fatalf("backup row must survive the round-trip: %v", err)
	}
	if triggerSource != "unknown" {
		t.Fatalf("expected trigger_source = %q after re-apply, got %q", "unknown", triggerSource)
	}
}
