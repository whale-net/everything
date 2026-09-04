//go:build integration

// This file only builds under the "integration" build tag -- see
// migrate_integration_test.go's identical header for why.
package migrate_test

import (
	"context"
	"embed"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

//go:embed testdata/migrations_extra/*.sql
var testExtraMigrations embed.FS

// TestApplySource_TracksIndependentlyOfDomainMigrations is the regression
// test for the bug ApplySource's doc comment describes: a shared library's
// migrations must never share the domain's own migrations-tracking table,
// because golang-migrate's Up() only ever looks for the next version
// strictly greater than the current one -- it can never go back for a lower
// one. If ApplySource's Source shared the domain's table, applying it here
// (a version far above the domain's own 1-2) would make the domain's own
// migrations 1 and 2 permanently invisible to the domain Runner's Up().
// This test proves that doesn't happen: both apply cleanly, into separate
// tables, regardless of order or how far apart their version numbers are.
func TestApplySource_TracksIndependentlyOfDomainMigrations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	db := dbtest.NewPostgres(ctx, t, dbtest.Options{})
	sqlDB := openDB(t, db)

	// Apply the "shared library" source first, exactly like RunCLI does
	// (before touching the caller's own migrations at all).
	if err := migrate.ApplySource(sqlDB, migrate.Source{
		Name: "extra",
		FS:   testExtraMigrations,
		Dir:  "testdata/migrations_extra",
	}); err != nil {
		t.Fatalf("ApplySource: %v", err)
	}

	// The domain's own migrations (small, ordinary numbers 1 and 2) must
	// still apply normally -- this is the exact scenario that a shared
	// version pointer would break.
	runner := migrate.NewRunner(sqlDB, testMigrations, "testdata/migrations")
	if err := runner.Up(); err != nil {
		t.Fatalf("domain Runner.Up: %v", err)
	}

	domainVersion, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("domain Runner.Version: %v", err)
	}
	if dirty || domainVersion != 2 {
		t.Fatalf("expected domain version 2 (unaffected by the library source), got version=%d dirty=%v", domainVersion, dirty)
	}

	// Both tables exist, proving each source's own migration actually ran.
	if _, err := db.Pool.Exec(ctx, `SELECT 1 FROM extra_thing`); err != nil {
		t.Fatalf("expected extra_thing table from the library source to exist: %v", err)
	}
	if _, err := db.Pool.Exec(ctx, `SELECT 1 FROM widgets`); err != nil {
		t.Fatalf("expected widgets table from the domain's own migrations to exist: %v", err)
	}

	// The two sources are tracked in separate tables with independent
	// version numbers -- not one shared, merged sequence.
	var extraVersion int
	if err := db.Pool.QueryRow(ctx, `SELECT version FROM schema_migrations_extra`).Scan(&extraVersion); err != nil {
		t.Fatalf("expected schema_migrations_extra to exist and hold the library source's own version: %v", err)
	}
	if extraVersion != 1 {
		t.Fatalf("expected schema_migrations_extra version 1, got %d", extraVersion)
	}

	// Calling ApplySource again (as RunCLI does on every invocation) must
	// stay a no-op -- confirming it's safe to run on every deploy, not just
	// the first one that creates the table.
	if err := migrate.ApplySource(sqlDB, migrate.Source{
		Name: "extra",
		FS:   testExtraMigrations,
		Dir:  "testdata/migrations_extra",
	}); err != nil {
		t.Fatalf("second ApplySource call: %v", err)
	}
}
