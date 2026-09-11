//go:build integration

// Real-migration round-trip and backfill coverage for migration 042 (#2361,
// plan #2359): proves migration 042 itself (not a paraphrase) applies on
// top of the full prior history, creates the GC-level attachment table and
// the two FR12 conflict tables with the expected shape, backfills every
// pre-existing SGC-scoped attachment per the issue's Implementation item
// (non-conflicting union, genuine conflict detection, the "one attaching
// SGC among bare siblings" and "no attachments at all" edge cases), is
// idempotent under re-run, and rolls back cleanly leaving
// sgc_workshop_libraries untouched (NFR1, acceptance criterion 3).
//
// Same precedent as migration_040_integration_test.go: build-tagged
// `integration`, drives the real embedded migrations FS through
// //libs/go/migrate's Runner, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_042_integration_test --test_output=all
package main

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// openMigrateTestDB042 mirrors migration_040_integration_test.go's helper --
// this target compiles only its own srcs, so it carries its own copy.
func openMigrateTestDB042(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// seedFixtures holds the ids seeded by seedGameWithConfig, so backfill test
// bodies can attach SGCs/libraries without re-deriving them.
type seedFixtures struct {
	gameID   int64
	configID int64
}

// seedGameWithConfig seeds one game and one game_config, the shared parent
// every backfill scenario below hangs its SGCs and libraries off of.
func seedGameWithConfig(ctx context.Context, t *testing.T, db *dbtest.Postgres, name string) seedFixtures {
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
	return seedFixtures{gameID: gameID, configID: configID}
}

// seedSGC seeds a server and a server_game_config attaching it to configID.
func seedSGC(ctx context.Context, t *testing.T, db *dbtest.Postgres, configID int64, serverName string) int64 {
	t.Helper()

	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ($1) RETURNING server_id`, serverName).Scan(&serverID); err != nil {
		t.Fatalf("seed server %s: %v", serverName, err)
	}
	var sgcID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, serverID, configID,
	).Scan(&sgcID); err != nil {
		t.Fatalf("seed server_game_config for %s: %v", serverName, err)
	}
	return sgcID
}

// seedLibrary seeds a workshop library for gameID.
func seedLibrary(ctx context.Context, t *testing.T, db *dbtest.Postgres, gameID int64, name string) int64 {
	t.Helper()

	var libraryID int64
	if err := db.Pool.QueryRow(ctx,
		`INSERT INTO workshop_libraries (game_id, name) VALUES ($1, $2) RETURNING library_id`, gameID, name,
	).Scan(&libraryID); err != nil {
		t.Fatalf("seed library %s: %v", name, err)
	}
	return libraryID
}

// attachSGCLibrary attaches libraryID to sgcID at SGC scope (the pre-M6
// table 024's backfill reads from), with the given overrides.
func attachSGCLibrary(ctx context.Context, t *testing.T, db *dbtest.Postgres, sgcID, libraryID int64, presetID, volumeID *int64, installationPathOverride *string) {
	t.Helper()

	if _, err := db.Pool.Exec(ctx, `
		INSERT INTO sgc_workshop_libraries (sgc_id, library_id, preset_id, volume_id, installation_path_override)
		VALUES ($1, $2, $3, $4, $5)`,
		sgcID, libraryID, presetID, volumeID, installationPathOverride,
	); err != nil {
		t.Fatalf("attach library %d to sgc %d: %v", libraryID, sgcID, err)
	}
}

// migrateTo41ThenSeedAndApply42 brings a fresh db to version 41 (every
// migration through 041, i.e. everything migration 042's backfill reads
// from, but not 042 itself), lets the caller seed SGC-scoped fixtures
// against that schema, then applies exactly migration 042 (schema creation
// + backfill) and returns the runner for further assertions/steps.
func migrateTo41ThenSeedAndApply42(ctx context.Context, t *testing.T, db *dbtest.Postgres, sqlDB *sql.DB, seed func()) *migrate.Runner {
	t.Helper()

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 43 {
		t.Fatalf("expected the latest migration source version to be 43, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Migrate(41); err != nil {
		t.Fatalf("Migrate(41) (everything migration 042 backfills from, minus 042 itself): %v", err)
	}

	seed()

	if err := runner.Migrate(42); err != nil {
		t.Fatalf("Migrate(42) (schema + backfill): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(42), got dirty")
	}
	if version != 42 {
		t.Fatalf("expected version 42, got %d", version)
	}

	return runner
}

func columnExists042(ctx context.Context, t *testing.T, db *dbtest.Postgres, table, column string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = $1 AND column_name = $2
		)`, table, column,
	).Scan(&exists); err != nil {
		t.Fatalf("query information_schema.columns for %s.%s: %v", table, column, err)
	}
	return exists
}

func indexExists042(ctx context.Context, t *testing.T, db *dbtest.Postgres, indexName string) bool {
	t.Helper()
	var exists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM pg_indexes WHERE indexname = $1)`, indexName,
	).Scan(&exists); err != nil {
		t.Fatalf("check index %s exists: %v", indexName, err)
	}
	return exists
}

// TestMigration042_AppliesOnTopOfFullHistoryAndCreatesExpectedShape proves
// migration 042 applies cleanly, lands at version 42, creates all three
// tables with the scaffold's columns and indexes, and leaves
// sgc_workshop_libraries in place (NFR1, acceptance criterion 3).
func TestMigration042_AppliesOnTopOfFullHistoryAndCreatesExpectedShape(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	// Target version 42 explicitly rather than Up() (which now also
	// applies 043) -- same rationale as the other migration integration
	// tests' use of Migrate(N) over a relative Up()/Steps() call: this
	// test is about migration 042 specifically, not "whatever the latest
	// migration happens to be".
	if err := runner.Migrate(42); err != nil {
		t.Fatalf("Migrate(42) (applying every migration through 042): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate(42), got dirty")
	}
	if version != 42 {
		t.Fatalf("expected version 42 after Migrate(42), got %d", version)
	}

	for _, col := range []string{"config_id", "library_id", "preset_id", "volume_id", "installation_path_override", "created_at"} {
		if !columnExists042(ctx, t, db, "gameconfig_workshop_libraries", col) {
			t.Fatalf("expected column %s on gameconfig_workshop_libraries", col)
		}
	}
	for _, col := range []string{"conflict_id", "config_id", "detected_at", "resolved_at", "resolution", "resolved_library_id"} {
		if !columnExists042(ctx, t, db, "workshop_library_migration_conflicts", col) {
			t.Fatalf("expected column %s on workshop_library_migration_conflicts", col)
		}
	}
	for _, col := range []string{"conflict_id", "library_id", "sgc_id"} {
		if !columnExists042(ctx, t, db, "workshop_library_migration_conflict_candidates", col) {
			t.Fatalf("expected column %s on workshop_library_migration_conflict_candidates", col)
		}
	}

	for _, idx := range []string{
		"idx_gameconfig_workshop_libraries_config_id",
		"idx_gameconfig_workshop_libraries_library_id",
		"idx_workshop_library_migration_conflicts_unresolved",
	} {
		if !indexExists042(ctx, t, db, idx) {
			t.Fatalf("expected index %s after migration 042", idx)
		}
	}

	var sgcTableExists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'sgc_workshop_libraries')`,
	).Scan(&sgcTableExists); err != nil {
		t.Fatalf("check sgc_workshop_libraries existence: %v", err)
	}
	if !sgcTableExists {
		t.Fatal("sgc_workshop_libraries must remain in place after migration 042 (NFR1) -- it does not exist")
	}
}

// TestMigration042_BackfillSingleSGCPreservesOverrides covers the issue's
// first Testing bullet: one GameConfig, one SGC, two libraries -> both land
// GC-level with overrides preserved, no conflict row.
func TestMigration042_BackfillSingleSGCPreservesOverrides(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	var libA, libB int64
	presetVal := int64(0)
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "single-sgc")
		if err := db.Pool.QueryRow(ctx,
			`INSERT INTO game_addon_path_presets (game_id, name, installation_path) VALUES ($1, 'preset-a', '/data/a') RETURNING preset_id`,
			fixtures.gameID,
		).Scan(&presetVal); err != nil {
			t.Fatalf("seed preset: %v", err)
		}
		sgcID := seedSGC(ctx, t, db, fixtures.configID, "single-sgc-server")
		libA = seedLibrary(ctx, t, db, fixtures.gameID, "lib-a")
		libB = seedLibrary(ctx, t, db, fixtures.gameID, "lib-b")
		override := "/opt/custom-b"
		attachSGCLibrary(ctx, t, db, sgcID, libA, &presetVal, nil, nil)
		attachSGCLibrary(ctx, t, db, sgcID, libB, nil, nil, &override)
	})

	rows, err := db.Pool.Query(ctx, `
		SELECT library_id, preset_id, volume_id, installation_path_override
		FROM gameconfig_workshop_libraries WHERE config_id = $1 ORDER BY library_id`, fixtures.configID)
	if err != nil {
		t.Fatalf("query gameconfig_workshop_libraries: %v", err)
	}
	defer rows.Close()

	type got struct {
		libraryID int64
		presetID  *int64
		volumeID  *int64
		override  *string
	}
	var gotRows []got
	for rows.Next() {
		var g got
		if err := rows.Scan(&g.libraryID, &g.presetID, &g.volumeID, &g.override); err != nil {
			t.Fatalf("scan: %v", err)
		}
		gotRows = append(gotRows, g)
	}
	if len(gotRows) != 2 {
		t.Fatalf("expected 2 GC-level rows, got %d: %+v", len(gotRows), gotRows)
	}
	if gotRows[0].libraryID != libA || gotRows[0].presetID == nil || *gotRows[0].presetID != presetVal {
		t.Fatalf("expected lib-a row with preset_id=%d preserved, got %+v", presetVal, gotRows[0])
	}
	if gotRows[1].libraryID != libB || gotRows[1].override == nil || *gotRows[1].override != "/opt/custom-b" {
		t.Fatalf("expected lib-b row with installation_path_override preserved, got %+v", gotRows[1])
	}

	assertNoConflict(ctx, t, db, fixtures.configID)
}

// TestMigration042_BackfillIdenticalSetsAcrossTwoSGCs covers the issue's
// second Testing bullet: two SGCs of one GameConfig with identical sets ->
// GC-level set equals that set, no conflict row.
func TestMigration042_BackfillIdenticalSetsAcrossTwoSGCs(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	var lib1, lib2 int64
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "identical-sets")
		sgc1 := seedSGC(ctx, t, db, fixtures.configID, "identical-server-1")
		sgc2 := seedSGC(ctx, t, db, fixtures.configID, "identical-server-2")
		lib1 = seedLibrary(ctx, t, db, fixtures.gameID, "lib-1")
		lib2 = seedLibrary(ctx, t, db, fixtures.gameID, "lib-2")
		for _, sgcID := range []int64{sgc1, sgc2} {
			attachSGCLibrary(ctx, t, db, sgcID, lib1, nil, nil, nil)
			attachSGCLibrary(ctx, t, db, sgcID, lib2, nil, nil, nil)
		}
	})

	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM gameconfig_workshop_libraries WHERE config_id = $1`, fixtures.configID).Scan(&count); err != nil {
		t.Fatalf("count GC rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected 2 GC-level rows (lib-1, lib-2), got %d", count)
	}
	for _, libID := range []int64{lib1, lib2} {
		var exists bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM gameconfig_workshop_libraries WHERE config_id = $1 AND library_id = $2)`,
			fixtures.configID, libID,
		).Scan(&exists); err != nil {
			t.Fatalf("check library %d present: %v", libID, err)
		}
		if !exists {
			t.Fatalf("expected library %d to be backfilled at GC level", libID)
		}
	}

	assertNoConflict(ctx, t, db, fixtures.configID)
}

// TestMigration042_BackfillDifferentLibrarySetsConflict covers the issue's
// third Testing bullet: two SGCs with different sets -> zero GC-level rows
// for that config, one conflict row, one candidate row per
// (library_id, sgc_id).
func TestMigration042_BackfillDifferentLibrarySetsConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	var sgc1, sgc2, lib1, lib2 int64
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "different-sets")
		sgc1 = seedSGC(ctx, t, db, fixtures.configID, "different-server-1")
		sgc2 = seedSGC(ctx, t, db, fixtures.configID, "different-server-2")
		lib1 = seedLibrary(ctx, t, db, fixtures.gameID, "lib-conflict-1")
		lib2 = seedLibrary(ctx, t, db, fixtures.gameID, "lib-conflict-2")
		attachSGCLibrary(ctx, t, db, sgc1, lib1, nil, nil, nil)
		attachSGCLibrary(ctx, t, db, sgc2, lib2, nil, nil, nil)
	})

	var gcCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM gameconfig_workshop_libraries WHERE config_id = $1`, fixtures.configID).Scan(&gcCount); err != nil {
		t.Fatalf("count GC rows: %v", err)
	}
	if gcCount != 0 {
		t.Fatalf("expected zero GC-level rows for a conflicting config, got %d", gcCount)
	}

	conflictID := requireOneUnresolvedConflict(ctx, t, db, fixtures.configID)

	candidates := candidatePairs(ctx, t, db, conflictID)
	want := map[[2]int64]bool{{lib1, sgc1}: true, {lib2, sgc2}: true}
	if len(candidates) != len(want) {
		t.Fatalf("expected %d candidate rows, got %d: %v", len(want), len(candidates), candidates)
	}
	for pair := range want {
		if !candidates[pair] {
			t.Fatalf("expected candidate (library_id=%d, sgc_id=%d), got %v", pair[0], pair[1], candidates)
		}
	}
}

// TestMigration042_BackfillSameLibraryDifferentOverrideConflict covers the
// issue's fourth Testing bullet: two SGCs attaching the same library with
// different installation_path_override -> conflict, both variants recorded.
func TestMigration042_BackfillSameLibraryDifferentOverrideConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	var sgc1, sgc2, lib int64
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "override-conflict")
		sgc1 = seedSGC(ctx, t, db, fixtures.configID, "override-server-1")
		sgc2 = seedSGC(ctx, t, db, fixtures.configID, "override-server-2")
		lib = seedLibrary(ctx, t, db, fixtures.gameID, "lib-override")
		overrideA := "/opt/variant-a"
		overrideB := "/opt/variant-b"
		attachSGCLibrary(ctx, t, db, sgc1, lib, nil, nil, &overrideA)
		attachSGCLibrary(ctx, t, db, sgc2, lib, nil, nil, &overrideB)
	})

	var gcCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM gameconfig_workshop_libraries WHERE config_id = $1`, fixtures.configID).Scan(&gcCount); err != nil {
		t.Fatalf("count GC rows: %v", err)
	}
	if gcCount != 0 {
		t.Fatalf("expected zero GC-level rows for a conflicting config, got %d", gcCount)
	}

	conflictID := requireOneUnresolvedConflict(ctx, t, db, fixtures.configID)

	candidates := candidatePairs(ctx, t, db, conflictID)
	want := map[[2]int64]bool{{lib, sgc1}: true, {lib, sgc2}: true}
	if len(candidates) != len(want) {
		t.Fatalf("expected both override variants (2 candidate rows), got %d: %v", len(candidates), candidates)
	}
	for pair := range want {
		if !candidates[pair] {
			t.Fatalf("expected candidate (library_id=%d, sgc_id=%d), got %v", pair[0], pair[1], candidates)
		}
	}
}

// TestMigration042_BackfillLoneAttachingSGCIsNotAConflict covers the
// scaffold's "the case where only one SGC has any attachment at all":
// a sibling SGC with zero attachments must not be treated as disagreeing
// with the one that has attachments.
func TestMigration042_BackfillLoneAttachingSGCIsNotAConflict(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	var lib int64
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "lone-attacher")
		attachingSGC := seedSGC(ctx, t, db, fixtures.configID, "lone-attacher-server-1")
		seedSGC(ctx, t, db, fixtures.configID, "lone-attacher-server-2") // bare sibling, zero attachments
		lib = seedLibrary(ctx, t, db, fixtures.gameID, "lib-lone")
		attachSGCLibrary(ctx, t, db, attachingSGC, lib, nil, nil, nil)
	})

	var exists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM gameconfig_workshop_libraries WHERE config_id = $1 AND library_id = $2)`,
		fixtures.configID, lib,
	).Scan(&exists); err != nil {
		t.Fatalf("check GC-level row: %v", err)
	}
	if !exists {
		t.Fatal("expected the lone attaching SGC's library to be backfilled at GC level")
	}

	assertNoConflict(ctx, t, db, fixtures.configID)
}

// TestMigration042_BackfillNoAttachmentsProducesNothing covers the issue's
// fifth Testing bullet: a GameConfig with no SGC-level attachments at all
// produces neither GC-level rows nor a conflict row.
func TestMigration042_BackfillNoAttachmentsProducesNothing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "no-attachments")
		seedSGC(ctx, t, db, fixtures.configID, "no-attachments-server")
	})

	var gcCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM gameconfig_workshop_libraries WHERE config_id = $1`, fixtures.configID).Scan(&gcCount); err != nil {
		t.Fatalf("count GC rows: %v", err)
	}
	if gcCount != 0 {
		t.Fatalf("expected zero GC-level rows for a config with no attachments, got %d", gcCount)
	}
	assertNoConflict(ctx, t, db, fixtures.configID)
}

// TestMigration042_BackfillIsIdempotent covers the issue's sixth Testing
// bullet: re-running the backfill is a no-op, and a resolved conflict is
// never reopened by a re-run.
func TestMigration042_BackfillIsIdempotent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	var fixtures seedFixtures
	migrateTo41ThenSeedAndApply42(ctx, t, db, sqlDB, func() {
		fixtures = seedGameWithConfig(ctx, t, db, "idempotent")

		// Non-conflicting config: one SGC, one library.
		sgc := seedSGC(ctx, t, db, fixtures.configID, "idempotent-server-1")
		nonConflictingLib := seedLibrary(ctx, t, db, fixtures.gameID, "lib-idempotent")
		attachSGCLibrary(ctx, t, db, sgc, nonConflictingLib, nil, nil, nil)

		// Conflicting config (separate game_config): two SGCs, different sets.
		conflictFixtures := seedGameWithConfig(ctx, t, db, "idempotent-conflict")
		conflictSGC1 := seedSGC(ctx, t, db, conflictFixtures.configID, "idempotent-conflict-server-1")
		conflictSGC2 := seedSGC(ctx, t, db, conflictFixtures.configID, "idempotent-conflict-server-2")
		conflictLib1 := seedLibrary(ctx, t, db, conflictFixtures.gameID, "lib-idempotent-conflict-1")
		conflictLib2 := seedLibrary(ctx, t, db, conflictFixtures.gameID, "lib-idempotent-conflict-2")
		attachSGCLibrary(ctx, t, db, conflictSGC1, conflictLib1, nil, nil, nil)
		attachSGCLibrary(ctx, t, db, conflictSGC2, conflictLib2, nil, nil, nil)
	})

	// Resolve the conflict before re-running the backfill, to prove a
	// re-run never reopens it.
	var conflictConfigID int64
	if err := db.Pool.QueryRow(ctx, `
		SELECT config_id FROM workshop_library_migration_conflicts
		WHERE conflict_id = (SELECT MIN(conflict_id) FROM workshop_library_migration_conflicts)`,
	).Scan(&conflictConfigID); err != nil {
		t.Fatalf("find conflict config_id: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `
		UPDATE workshop_library_migration_conflicts
		SET resolved_at = NOW(), resolution = 'union'
		WHERE config_id = $1`, conflictConfigID,
	); err != nil {
		t.Fatalf("resolve conflict: %v", err)
	}

	var gcCountBefore, conflictCountBefore, candidateCountBefore int
	mustScan := func(query string, dst *int) {
		if err := db.Pool.QueryRow(ctx, query).Scan(dst); err != nil {
			t.Fatalf("count query %q: %v", query, err)
		}
	}
	mustScan(`SELECT COUNT(*) FROM gameconfig_workshop_libraries`, &gcCountBefore)
	mustScan(`SELECT COUNT(*) FROM workshop_library_migration_conflicts`, &conflictCountBefore)
	mustScan(`SELECT COUNT(*) FROM workshop_library_migration_conflict_candidates`, &candidateCountBefore)

	// Re-execute the migration's own up.sql content directly -- this is
	// what "re-running the migration" means for a framework (golang-migrate)
	// that otherwise refuses to re-apply an already-applied version.
	upSQL, err := migrations.ReadFile("migrations/042_gameconfig_workshop_libraries.up.sql")
	if err != nil {
		t.Fatalf("read migration 042 up.sql: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, string(upSQL)); err != nil {
		t.Fatalf("re-exec migration 042 up.sql: %v", err)
	}

	var gcCountAfter, conflictCountAfter, candidateCountAfter int
	mustScan(`SELECT COUNT(*) FROM gameconfig_workshop_libraries`, &gcCountAfter)
	mustScan(`SELECT COUNT(*) FROM workshop_library_migration_conflicts`, &conflictCountAfter)
	mustScan(`SELECT COUNT(*) FROM workshop_library_migration_conflict_candidates`, &candidateCountAfter)

	if gcCountAfter != gcCountBefore {
		t.Fatalf("gameconfig_workshop_libraries row count changed on re-run: before=%d after=%d", gcCountBefore, gcCountAfter)
	}
	if conflictCountAfter != conflictCountBefore {
		t.Fatalf("workshop_library_migration_conflicts row count changed on re-run: before=%d after=%d", conflictCountBefore, conflictCountAfter)
	}
	if candidateCountAfter != candidateCountBefore {
		t.Fatalf("workshop_library_migration_conflict_candidates row count changed on re-run: before=%d after=%d", candidateCountBefore, candidateCountAfter)
	}

	// The resolved conflict must still be resolved -- a re-run must not
	// reopen it.
	var resolvedAtStillSet bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT resolved_at IS NOT NULL FROM workshop_library_migration_conflicts WHERE config_id = $1`, conflictConfigID,
	).Scan(&resolvedAtStillSet); err != nil {
		t.Fatalf("check resolved_at after re-run: %v", err)
	}
	if !resolvedAtStillSet {
		t.Fatal("re-running the backfill reopened an already-resolved conflict")
	}
}

// TestMigration042_DownDropsThreeTablesLeavesSGCIntact proves migration 042
// rolls back cleanly: down removes all three new tables (and their backfill
// data with them) and leaves sgc_workshop_libraries untouched.
func TestMigration042_DownDropsThreeTablesLeavesSGCIntact(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB042(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	var serverID int64
	if err := db.Pool.QueryRow(ctx, `INSERT INTO servers (name) VALUES ('m042-rollback-server') RETURNING server_id`).Scan(&serverID); err != nil {
		t.Fatalf("seed server: %v", err)
	}

	// Roll back to exactly version 41 (one before this migration), by
	// target version rather than a relative Steps(-1) off of whatever the
	// latest migration happens to be -- same rationale as
	// migration_038_integration_test.go/migration_039_integration_test.go/
	// migration_040_integration_test.go/migration_041_integration_test.go.
	// A relative Steps(-1) would instead undo whatever migration is
	// current HEAD (043 once it landed), leaving 042's own tables in
	// place and this test passing for the wrong reason.
	if err := runner.Migrate(41); err != nil {
		t.Fatalf("Migrate(41) (rolling back 042): %v", err)
	}
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after down-step, got dirty")
	}
	if version != 41 {
		t.Fatalf("expected version 41 after Migrate(41), got %d", version)
	}

	for _, table := range []string{
		"gameconfig_workshop_libraries",
		"workshop_library_migration_conflicts",
		"workshop_library_migration_conflict_candidates",
	} {
		var exists bool
		if err := db.Pool.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = $1)`, table,
		).Scan(&exists); err != nil {
			t.Fatalf("check table %s after down: %v", table, err)
		}
		if exists {
			t.Fatalf("expected %s to be dropped by the down migration", table)
		}
	}

	var sgcTableExists bool
	if err := db.Pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_name = 'sgc_workshop_libraries')`,
	).Scan(&sgcTableExists); err != nil {
		t.Fatalf("check sgc_workshop_libraries after down: %v", err)
	}
	if !sgcTableExists {
		t.Fatal("sgc_workshop_libraries must survive migration 042's down (NFR1) -- it does not exist")
	}

	var serverName string
	if err := db.Pool.QueryRow(ctx, `SELECT name FROM servers WHERE server_id = $1`, serverID).Scan(&serverName); err != nil {
		t.Fatalf("server row must survive: %v", err)
	}
	if serverName != "m042-rollback-server" {
		t.Fatalf("server row = %q, want m042-rollback-server", serverName)
	}
}

// assertNoConflict fails the test if configID has any conflict record
// (resolved or not).
func assertNoConflict(ctx context.Context, t *testing.T, db *dbtest.Postgres, configID int64) {
	t.Helper()
	var count int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM workshop_library_migration_conflicts WHERE config_id = $1`, configID).Scan(&count); err != nil {
		t.Fatalf("count conflicts for config %d: %v", configID, err)
	}
	if count != 0 {
		t.Fatalf("expected no conflict row for config %d, got %d", configID, count)
	}
}

// requireOneUnresolvedConflict fails the test unless configID has exactly
// one unresolved conflict row, and returns its conflict_id.
func requireOneUnresolvedConflict(ctx context.Context, t *testing.T, db *dbtest.Postgres, configID int64) int64 {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `
		SELECT conflict_id, resolved_at IS NULL FROM workshop_library_migration_conflicts WHERE config_id = $1`, configID)
	if err != nil {
		t.Fatalf("query conflicts for config %d: %v", configID, err)
	}
	defer rows.Close()

	var conflictID int64
	var count int
	for rows.Next() {
		var unresolved bool
		if err := rows.Scan(&conflictID, &unresolved); err != nil {
			t.Fatalf("scan conflict row: %v", err)
		}
		if !unresolved {
			t.Fatalf("expected conflict for config %d to be unresolved", configID)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate conflicts: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly one conflict row for config %d, got %d", configID, count)
	}
	return conflictID
}

// candidatePairs returns the (library_id, sgc_id) pairs recorded as
// candidates for conflictID.
func candidatePairs(ctx context.Context, t *testing.T, db *dbtest.Postgres, conflictID int64) map[[2]int64]bool {
	t.Helper()
	rows, err := db.Pool.Query(ctx, `SELECT library_id, sgc_id FROM workshop_library_migration_conflict_candidates WHERE conflict_id = $1`, conflictID)
	if err != nil {
		t.Fatalf("query candidates for conflict %d: %v", conflictID, err)
	}
	defer rows.Close()

	pairs := map[[2]int64]bool{}
	for rows.Next() {
		var libraryID, sgcID int64
		if err := rows.Scan(&libraryID, &sgcID); err != nil {
			t.Fatalf("scan candidate row: %v", err)
		}
		pairs[[2]int64{libraryID, sgcID}] = true
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate candidates: %v", err)
	}
	return pairs
}
