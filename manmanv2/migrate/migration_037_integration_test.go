//go:build integration

// Real-migration round-trip coverage for migration 037 (task #2092, plan
// #2080): proves migration 037 itself (not a paraphrase) applies on top of
// the full history, replaces the action_executions ON DELETE CASCADE FK
// with a plain FK, and that its down/up round-trip is lossless for
// soft-deleted definitions and their execution history -- the issue's
// "Migration round-trip: up/down/up preserves rows" Testing item.
//
// Same precedent as migration_036_integration_test.go: build-tagged
// `integration`, drives the real embedded migrations FS through
// //libs/go/migrate's Runner, run explicitly with Docker.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //manmanv2/migrate:migration_037_integration_test --test_output=all
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

// openMigrateTestDB037 opens a *sql.DB against db's isolated dbtest
// database/role using the pgx stdlib driver the Runner requires (same shape
// as migration_036_integration_test.go's helper; this target compiles only
// its own srcs, so it carries its own copy).
func openMigrateTestDB037(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

// TestMigration037_SoftDeleteFKAndLosslessRoundTrip covers the whole FR9/FR10
// migration contract in one database lifetime:
//
//  1. Full history applies cleanly and lands at version 37 with the
//     deleted_at column present and the CASCADE FK replaced.
//  2. A soft-deleted definition plus its execution rows survive a
//     Steps(-1)/re-Up round-trip losslessly (the down un-deletes; the
//     restored CASCADE never has anything to cascade over).
//  3. At version 37, deleting the definition's parent chain proves the FK
//     no longer cascades (deleting an action definition row with
//     executions must be rejected by the FK, not destroy history).
func TestMigration037_SoftDeleteFKAndLosslessRoundTrip(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openMigrateTestDB037(t, db)

	runner := migrate.NewRunner(sqlDB, migrations, "migrations")

	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 41 {
		t.Fatalf("expected the latest migration source version to be 41, got %d -- update this test if a newer migration has since landed", latest)
	}

	if err := runner.Up(); err != nil {
		t.Fatalf("Up (applying every migration through 037): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty || version != 41 {
		t.Fatalf("expected clean version 41 after Up, got version %d dirty=%v", version, dirty)
	}

	// deleted_at column present on action_definitions.
	var hasDeletedAt bool
	if err := db.Pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.columns
			WHERE table_name = 'action_definitions' AND column_name = 'deleted_at'
		)`).Scan(&hasDeletedAt); err != nil {
		t.Fatalf("check deleted_at column: %v", err)
	}
	if !hasDeletedAt {
		t.Fatalf("expected deleted_at column on action_definitions after migration 037")
	}

	// FK from action_executions must be plain now (no ON DELETE CASCADE).
	var fkDef string
	if err := db.Pool.QueryRow(ctx, `
		SELECT pg_get_constraintdef(oid) FROM pg_constraint
		WHERE conrelid = 'action_executions'::regclass
		  AND contype = 'f'
		  AND confrelid = 'action_definitions'::regclass
		  AND pg_get_constraintdef(oid) LIKE '%action_id%'
	`).Scan(&fkDef); err != nil {
		t.Fatalf("check executions FK: %v", err)
	}
	if contains(fkDef, "CASCADE") {
		t.Fatalf("expected plain FK after migration 037, got %q (still cascading)", fkDef)
	}

	// Seed a definition with an execution, then soft-delete it.
	var gameID, configID, serverID, sgcID, sessionID, actionID, executionID int64
	seed := func(q string, dest any, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, q, args...).Scan(dest); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	seed(`INSERT INTO games (name) VALUES ('m037-game') RETURNING game_id`, &gameID)
	seed(`INSERT INTO game_configs (game_id, name, image) VALUES ($1, 'm037-config', 'image') RETURNING config_id`, &configID, gameID)
	seed(`INSERT INTO servers (name) VALUES ('m037-server') RETURNING server_id`, &serverID)
	seed(`INSERT INTO server_game_configs (server_id, game_config_id) VALUES ($1, $2) RETURNING sgc_id`, &sgcID, serverID, configID)
	seed(`INSERT INTO sessions (sgc_id) VALUES ($1) RETURNING session_id`, &sessionID, sgcID)
	seed(`INSERT INTO action_definitions (definition_level, entity_id, name, label, command_template)
		VALUES ('game', $1, 'save_game', 'Save Game', 'save') RETURNING action_id`, &actionID, gameID)
	seed(`INSERT INTO action_executions (action_id, session_id, rendered_command)
		VALUES ($1, $2, 'save') RETURNING execution_id`, &executionID, actionID, sessionID)
	if _, err := db.Pool.Exec(ctx, `UPDATE action_definitions SET deleted_at = NOW() WHERE action_id = $1`, actionID); err != nil {
		t.Fatalf("soft-delete seeded definition: %v", err)
	}

	// 2. Round-trip: roll back to exactly one below 037 (rather than a
	// relative Steps() count, which breaks every time another migration
	// lands on top of 037), so 037's lossless down actually runs
	// regardless of how many later migrations now exist, then re-apply to
	// restore 037+038.
	if err := runner.Migrate(36); err != nil {
		t.Fatalf("Migrate(36) (rolling back 037): %v", err)
	}
	afterDown, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after down-step: %v", err)
	}
	if dirty || afterDown != 36 {
		t.Fatalf("expected version 36 after Migrate(36), got %d dirty=%v", afterDown, dirty)
	}
	if err := runner.Migrate(38); err != nil {
		t.Fatalf("Migrate(38): %v", err)
	}

	// Lossless: the definition row survived (un-deleted) and its execution
	// history is intact.
	var defCount, execCount int
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM action_definitions WHERE action_id = $1`, actionID).Scan(&defCount); err != nil {
		t.Fatalf("count definitions after round-trip: %v", err)
	}
	if defCount != 1 {
		t.Fatalf("definition row lost in migration round-trip (FR10 violation)")
	}
	if err := db.Pool.QueryRow(ctx, `SELECT COUNT(*) FROM action_executions WHERE execution_id = $1`, executionID).Scan(&execCount); err != nil {
		t.Fatalf("count executions after round-trip: %v", err)
	}
	if execCount != 1 {
		t.Fatalf("execution row lost in migration round-trip (FR10 violation)")
	}
	var deletedAt *time.Time
	if err := db.Pool.QueryRow(ctx, `SELECT deleted_at FROM action_definitions WHERE action_id = $1`, actionID).Scan(&deletedAt); err != nil {
		t.Fatalf("fetch deleted_at after round-trip: %v", err)
	}
	if deletedAt != nil {
		t.Fatalf("down migration should have un-deleted the row (lossless down), got deleted_at=%v", deletedAt)
	}

	// 3. At version 37 the FK enforces retention: a direct hard DELETE of a
	// definition that has executions must be rejected, not cascade.
	if _, err := db.Pool.Exec(ctx, `DELETE FROM action_definitions WHERE action_id = $1`, actionID); err == nil {
		t.Fatalf("hard DELETE of a definition with executions unexpectedly succeeded (CASCADE still in place?)")
	}
}

// contains is a tiny helper so this test file avoids importing strings just
// for one Contains call.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
