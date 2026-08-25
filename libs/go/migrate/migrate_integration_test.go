//go:build integration

// This file only builds under the "integration" build tag so that `bazel
// test //...` (which builds and runs the whole tree, including on
// Docker-less machines) never even compiles it, let alone runs it. See the
// integration_test go_test target's gotags in BUILD.bazel and
// //libs/go/dbtest's README for how to run it.
package migrate_test

import (
	"context"
	"database/sql"
	"embed"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed testdata/migrations/*.sql
var testMigrations embed.FS

// openDB opens a *sql.DB against db's isolated dbtest database/role, using
// the pgx stdlib driver Runner requires, and registers it for cleanup.
func openDB(t *testing.T, db *dbtest.Postgres) *sql.DB {
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

func TestRunner_Up_AppliesAllMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Up, got dirty")
	}
	if version != 2 {
		t.Fatalf("expected version 2 after applying both migrations, got %d", version)
	}

	if _, err := db.Pool.Exec(ctx, `INSERT INTO widgets (name, price) VALUES ($1, $2)`, "widget-a", 42); err != nil {
		t.Fatalf("insert into migrated table: %v", err)
	}

	var name string
	var price int
	if err := db.Pool.QueryRow(ctx, `SELECT name, price FROM widgets WHERE name = $1`, "widget-a").Scan(&name, &price); err != nil {
		t.Fatalf("select from migrated table: %v", err)
	}
	if name != "widget-a" || price != 42 {
		t.Fatalf("unexpected row: name=%q price=%d", name, price)
	}
}

func TestRunner_Down_RollsBackAllMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}
	if err := runner.Down(); err != nil {
		t.Fatalf("Down: %v", err)
	}

	// After a full Down, the migrate library reports version 0 with no
	// error (ErrNilVersion is swallowed by Runner.Version).
	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version after Down: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Down, got dirty")
	}
	if version != 0 {
		t.Fatalf("expected version 0 after Down, got %d", version)
	}

	_, err = db.Pool.Exec(ctx, `SELECT 1 FROM widgets`)
	if err == nil {
		t.Fatal("expected widgets table to no longer exist after Down")
	}
}

func TestRunner_Steps_AppliesOneMigrationAtATime(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Steps(1); err != nil {
		t.Fatalf("Steps(1): %v", err)
	}

	version, _, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if version != 1 {
		t.Fatalf("expected version 1 after one step, got %d", version)
	}

	// price column shouldn't exist yet -- only migration 0001 has run.
	if _, err := db.Pool.Exec(ctx, `SELECT price FROM widgets`); err == nil {
		t.Fatal("expected price column to not exist before migration 0002")
	}

	if err := runner.Steps(1); err != nil {
		t.Fatalf("Steps(1) second call: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `SELECT price FROM widgets`); err != nil {
		t.Fatalf("expected price column to exist after migration 0002: %v", err)
	}
}

func TestRunner_Migrate_AppliesUpWhenTargetIsAheadOfCurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Steps(1); err != nil {
		t.Fatalf("Steps(1): %v", err)
	}

	// Migrate(2) from version 1 exercises the "bypass to an explicit
	// version" path going up, same as it exercises going down elsewhere.
	if err := runner.Migrate(2); err != nil {
		t.Fatalf("Migrate(2): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate, got dirty")
	}
	if version != 2 {
		t.Fatalf("expected version 2 after Migrate(2), got %d", version)
	}
	if _, err := db.Pool.Exec(ctx, `SELECT price FROM widgets`); err != nil {
		t.Fatalf("expected price column to exist after migrating up to version 2: %v", err)
	}
}

func TestRunner_LatestVersion_ReturnsHighestSourceVersion(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	latest, err := runner.LatestVersion()
	if err != nil {
		t.Fatalf("LatestVersion: %v", err)
	}
	if latest != 2 {
		t.Fatalf("expected latest version 2, got %d", latest)
	}
}

func TestRunner_Migrate_RollsBackWhenTargetIsBehindCurrent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("Up: %v", err)
	}

	// Simulate an older image running after a rollback: target version 1,
	// even though the DB is currently at 2. Migrate should detect the DB is
	// ahead and step down, rather than no-op like Up() would.
	if err := runner.Migrate(1); err != nil {
		t.Fatalf("Migrate(1): %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty {
		t.Fatalf("expected clean state after Migrate, got dirty")
	}
	if version != 1 {
		t.Fatalf("expected version 1 after Migrate(1), got %d", version)
	}

	// price was added by migration 0002 - it should be gone again after
	// rolling back to version 1.
	if _, err := db.Pool.Exec(ctx, `SELECT price FROM widgets`); err == nil {
		t.Fatal("expected price column to no longer exist after rolling back to version 1")
	}
}

func TestRunner_UpWithTracking_RecordsHistory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.UpWithTracking(); err != nil {
		t.Fatalf("UpWithTracking: %v", err)
	}

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version: %v", err)
	}
	if dirty || version != 2 {
		t.Fatalf("expected clean version 2, got version=%d dirty=%v", version, dirty)
	}

	history := runner.History()

	successful, err := history.GetSuccessfulVersions()
	if err != nil {
		t.Fatalf("GetSuccessfulVersions: %v", err)
	}
	if len(successful) != 2 || successful[0] != 1 || successful[1] != 2 {
		t.Fatalf("expected successful versions [1 2], got %v", successful)
	}

	status, err := history.GetStatus(2)
	if err != nil {
		t.Fatalf("GetStatus: %v", err)
	}
	if !status.HasBeenAttempted || !status.IsSafe || status.LastStatus != "success" {
		t.Fatalf("unexpected status for version 2: %+v", status)
	}

	safe, reason, err := history.IsVersionSafe(2)
	if err != nil {
		t.Fatalf("IsVersionSafe: %v", err)
	}
	if !safe {
		t.Fatalf("expected version 2 to be safe to force to, got reason: %s", reason)
	}
}
