//go:build integration

// Real-migration round-trip coverage for migration 043 (M6 #2370, plan
// #2359): proves migration 043 itself (not a paraphrase) applies on top of
// the full prior history and drops sgc_workshop_libraries (NFR1), refuses
// loudly instead of silently discarding configuration when a GameConfig has
// an unresolved workshop_library_migration_conflicts row with no
// gameconfig_workshop_libraries attachments yet (the guard the issue's
// Implementation section calls for), allows the drop once that conflict is
// resolved -- or once the GameConfig already has some GC-level attachments
// despite the conflict still being open -- and rolls its down migration back
// to an empty sgc_workshop_libraries table (a schema rollback, not a data
// restore, per the down migration's own comment).
//
// Same precedent as migration_042_integration_test.go: build-tagged
// `integration`, drives the real embedded migrations FS through
// //libs/go/migrate's Runner, run explicitly with Docker. This target
// compiles only its own srcs (main.go + this file), so it carries its own
// copy of the seed/open helpers rather than sharing migration_042's.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_043_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// openMigrateTestDB043 mirrors migration_042_integration_test.go's helper --
// this target compiles only its own srcs, so it carries its own copy.
func openMigrateTestDB043(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// seedFixtures043 holds the ids seeded by seedGameWithConfig043.
type seedFixtures043 struct {
	gameID   int64
	configID int64
}

func seedGameWithConfig043(ctx context.Context, t *testing.T, db *dbtest.Postgres, name string) seedFixtures043 {
	t.Helper()

	var gameID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO games (name) VALUES ($1) RETURNING game_id`, name+"-game").Scan(&gameID); err != nil {
		t.Fatalf("seed game: %v", err)
	}
	var configID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO game_configs (game_id, name, image) VALUES ($1, $2, 'image') RETURNING config_id`, gameID, name+"-config",
	).Scan(&configID); err != nil {
		t.Fatalf("seed game_config: %v", err)
	}
	return seedFixtures043{gameID: gameID, configID: configID}
}

func seedLibrary043(ctx context.Context, t *testing.T, db *dbtest.Postgres, gameID int64, name string) int64 {
	t.Helper()

	var libraryID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO workshop_libraries (game_id, name) VALUES ($1, $2) RETURNING library_id`, gameID, name,
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library %s: %v", name, err)
	}
	return libraryID
}

// seedUnresolvedConflict043 seeds an unresolved
// workshop_library_migration_conflicts row for configID -- the state the
// issue's guard exists to protect: a conflict a Server Manager was asked to
// resolve (#2368) that has not actually been resolved.
func seedUnresolvedConflict043(ctx context.Context, t *testing.T, db *dbtest.Postgres, configID int64) int64 {
	t.Helper()

	var conflictID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO workshop_library_migration_conflicts (config_id) VALUES ($1) RETURNING conflict_id`, configID,
	).Scan(&conflictID); err != nil {
		t.Fatalf("seed unresolved conflict for config %d: %v", configID, err)
	}
	return conflictID
}

// seedGCAttachment043 seeds a gameconfig_workshop_libraries row directly --
// the "already has some GC-level attachments" case that must not block the
// drop even if an unresolved conflict row also exists for the same config
// (a partial resolution, or a Server Manager writing the outcome directly).
func seedGCAttachment043(ctx context.Context, t *testing.T, db *dbtest.Postgres, configID, libraryID int64) {
	t.Helper()

	if _, err := db.Pool.Exec(ctx,
		`INSERT INTO gameconfig_workshop_libraries (config_id, library_id) VALUES ($1, $2)`, configID, libraryID,
	); err != nil {
		t.Fatalf("seed gameconfig_workshop_libraries(config_id=%d, library_id=%d): %v", configID, libraryID, err)
	}
}

func sgcWorkshopLibrariesTableExists043(ctx context.Context, t *testing.T, db *dbtest.Postgres) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'sgc_workshop_libraries')`,
	).Scan(&exists); err != nil {
		t.Fatalf("check sgc_workshop_libraries existence: %v", err)
	}
	return exists
}

func indexExists043(ctx context.Context, t *testing.T, db *dbtest.Postgres, indexName string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, indexName,
	).Scan(&exists); err != nil {
		t.Fatalf("check index %s exists: %v", indexName, err)
	}
	return exists
}

// migrateTo42_043 brings a fresh db to version 42 -- everything migration
// 043 needs in place (gameconfig_workshop_libraries and the conflict
// tables), but not 043's own drop -- so a test can seed conflict/attachment
// fixtures against that schema before exercising Migrate(43).
func migrateTo42_043(ctx context.Context, t *testing.T, db *dbtest.Postgres, sqlDB *sql.DB) *migrate.Runner {
	t.Helper()

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 43 {
		t.Fatalf("expected the latest migration source version to be 43, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Migrate(42); err != nil {
		t.Fatalf("Migrate(42) (everything migration 043 needs, minus 043 itself): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after Migrate(42): %v", err)
	}
	if dirty || version != 42 {
		t.Fatalf("expected clean version 42, got version=%d dirty=%v", version, dirty)
	}

	return runner
}

// TestMigration043_AppliesOnTopOfFullHistoryAndDropsTable proves migration
// 043 applies cleanly on top of the full prior history, lands at version 43,
// and drops sgc_workshop_libraries and its indexes (NFR1, acceptance
// criterion 2).
func TestMigration043_AppliesOnTopOfFullHistoryAndDropsTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB043(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up (applying every migration through 043): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}
	if version != 43 {
		t.Fatalf("expected version 43 after Up, got %d", version)
	}

	if sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("expected sgc_workshop_libraries to be dropped by migration 043 (NFR1)")
	}
	for _, idx := range []string{"idx_sgc_workshop_libraries_library_id", "idx_sgc_workshop_libraries_sgc_id"} {
		if indexExists043(ctx, t, db, idx) {
			t.Fatalf("expected index %s to be dropped along with sgc_workshop_libraries", idx)
		}
	}
}

// TestMigration043_RefusesWhenUnresolvedConflictBlocksDrop covers the
// issue's guard: a GameConfig with an unresolved conflict row and no
// gameconfig_workshop_libraries attachments yet must block the drop rather
// than silently discarding that configuration.
func TestMigration043_RefusesWhenUnresolvedConflictBlocksDrop(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB043(t, db)

	runner := migrateTo42_043(ctx, t, db, sqlDB)

	fixtures := seedGameWithConfig043(ctx, t, db, "blocked-drop")
	seedUnresolvedConflict043(ctx, t, db, fixtures.configID)
	// Deliberately no gameconfig_workshop_libraries row for this config --
	// the exact state the guard exists to catch.

	err := runner.Migrate(43)
	if err == nil {
		t.Fatal("expected Migrate(43) to fail loudly with an unresolved conflict and no GC-level attachments, got nil error")
	}

	// The failure must be loud and specific, not a generic wrapping --
	// pointing at the conflict-resolution surface (#2368) per the issue.
	for _, want := range []string{"unresolved", "gameconfig_workshop_libraries", "2368"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected the error to mention %q (naming the unresolved-conflict guard and pointing at #2368), got: %v", want, err)
		}
	}

	// The table must still exist -- the guard must have prevented the drop
	// entirely, not partially applied it.
	if !sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("sgc_workshop_libraries must still exist after a refused migration 043 -- the guard must block the drop, not just the error return")
	}
}

// TestMigration043_AllowsWhenConflictResolved covers the acceptance
// criterion's other half: once the conflict is resolved, the drop proceeds
// normally.
func TestMigration043_AllowsWhenConflictResolved(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB043(t, db)

	runner := migrateTo42_043(ctx, t, db, sqlDB)

	fixtures := seedGameWithConfig043(ctx, t, db, "resolved-conflict")
	conflictID := seedUnresolvedConflict043(ctx, t, db, fixtures.configID)
	if _, err := db.Pool.Exec(ctx,
		`UPDATE workshop_library_migration_conflicts SET resolved_at = NOW(), resolution = 'union' WHERE conflict_id = $1`, conflictID,
	); err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}

	if err := runner.Migrate(43); err != nil {
		t.Fatalf("Migrate(43) with a resolved conflict must succeed: %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty || version != 43 {
		t.Fatalf("expected clean version 43, got version=%d dirty=%v", version, dirty)
	}
	if sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("expected sgc_workshop_libraries to be dropped once the blocking conflict was resolved")
	}
}

// TestMigration043_AllowsWhenGCAttachmentsExistDespiteUnresolvedConflict
// covers the guard's carve-out: a GameConfig with SOME
// gameconfig_workshop_libraries rows already (a partial resolution, or a
// Server Manager writing the union/override outcome directly) must not
// block the drop even if its conflict row is still technically unresolved.
func TestMigration043_AllowsWhenGCAttachmentsExistDespiteUnresolvedConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB043(t, db)

	runner := migrateTo42_043(ctx, t, db, sqlDB)

	fixtures := seedGameWithConfig043(ctx, t, db, "partial-resolution")
	seedUnresolvedConflict043(ctx, t, db, fixtures.configID)
	lib := seedLibrary043(ctx, t, db, fixtures.gameID, "lib-partial")
	seedGCAttachment043(ctx, t, db, fixtures.configID, lib)

	if err := runner.Migrate(43); err != nil {
		t.Fatalf("Migrate(43) must succeed once the config has some GC-level attachments, even with the conflict row still unresolved: %v", err)
	}
	if sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("expected sgc_workshop_libraries to be dropped -- existing GC-level attachments must not block the drop")
	}
}

// TestMigration043_DownRecreatesEmptyTable proves migration 043's down
// recreates sgc_workshop_libraries with the expected shape, empty -- a
// schema rollback, not a data restore, per the down migration's own
// comment.
func TestMigration043_DownRecreatesEmptyTable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB043(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("sanity check: sgc_workshop_libraries should be dropped after Up")
	}

	if err := runner.Steps(-1); err != nil {
		t.Fatalf("Steps(-1) (rolling back migration 043): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after down-step, got dirty")
	}
	if version != 42 {
		t.Fatalf("expected version 42 after rolling back migration 043, got %d", version)
	}

	if !sgcWorkshopLibrariesTableExists043(ctx, t, db) {
		t.Fatal("expected sgc_workshop_libraries to be recreated by migration 043's down")
	}
	for _, idx := range []string{"idx_sgc_workshop_libraries_sgc_id", "idx_sgc_workshop_libraries_library_id"} {
		if !indexExists043(ctx, t, db, idx) {
			t.Fatalf("expected index %s to be recreated by migration 043's down", idx)
		}
	}

	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM sgc_workshop_libraries`).Scan(&count); err != nil {
		t.Fatalf("count sgc_workshop_libraries rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected the recreated sgc_workshop_libraries to be empty (schema rollback, not a data restore), got %d rows", count)
	}
}
