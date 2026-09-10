//go:build integration

// Real-Postgres coverage for migration 017
// (leaflab/migrate/schema/migrations/017_region_parent_history.up.sql) --
// the region_parent_history / board_region_history SCD2 tables and the
// one-open-row-per-region backfill (FR1). Same pattern as
// migration_016_integration_test.go: schema setup runs the real migrations
// via //leaflab/migrate/schema, so these tests exercise the actual SQL
// rather than a hand-maintained paraphrase of it.
//
// Unlike the migration_016 tests (which can start from
// newLeafLabTestPool's fully-migrated DB), the backfill tests here must
// seed regions *before* migration 017 applies, so this file drives the
// runner itself: provision a bare Postgres via //libs/go/dbtest, migrate to
// version 16, seed the region hierarchy, then step 017 up (and down, and up
// again, for the round-trip).
package main

import (
	"context"
	"database/sql"
	"testing"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/migrate/schema"
	"github.com/whale-net/everything/libs/go/dbtest"
	"github.com/whale-net/everything/libs/go/migrate"
)

// migration017SeedRegion inserts a region (optionally with a parent) and
// returns its region_id. parentID may be 0 for a top-level region.
func migration017SeedRegion(t *testing.T, db *sql.DB, name string, parentID int64) int64 {
	t.Helper()
	var id int64
	var err error
	if parentID == 0 {
		err = db.QueryRow(`INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id)
	} else {
		err = db.QueryRow(`INSERT INTO region (parent_region_id, name) VALUES ($1, $2) RETURNING region_id`,
			parentID, name).Scan(&id)
	}
	if err != nil {
		t.Fatalf("seed region %s: %v", name, err)
	}
	return id
}

// migration016SeedRegion is the pgxpool-based twin of migration017SeedRegion,
// for use with newLeafLabTestPool pools.
func migration016SeedRegion(t *testing.T, pool *pgxpool.Pool, name string, parentID int64) int64 {
	t.Helper()
	ctx := context.Background()
	var id int64
	var err error
	if parentID == 0 {
		err = pool.QueryRow(ctx, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, name).Scan(&id)
	} else {
		err = pool.QueryRow(ctx, `INSERT INTO region (parent_region_id, name) VALUES ($1, $2) RETURNING region_id`,
			parentID, name).Scan(&id)
	}
	if err != nil {
		t.Fatalf("seed region %s: %v", name, err)
	}
	return id
}

// openParentRowsForRegion returns the (count, parent of the open row) for
// region_parent_history rows for the given region.
func openParentRowsForRegion(t *testing.T, db *sql.DB, regionID int64) (int, *int64) {
	t.Helper()
	var count int
	var parent *int64
	if err := db.QueryRow(`
		SELECT COUNT(*), MIN(parent_region_id)
		FROM region_parent_history
		WHERE region_id = $1 AND valid_to IS NULL`, regionID).Scan(&count, &parent); err != nil {
		t.Fatalf("count open parent history rows for region %d: %v", regionID, err)
	}
	return count, parent
}

// TestMigration017_BackfillOneOpenRowPerRegion seeds a region hierarchy at
// migration 16, then applies 017 and proves FR1's backfill: every
// pre-existing region ends up with exactly one open region_parent_history
// row, and its parent_region_id matches the region's current
// region.parent_region_id (NULL for top-level regions -- recorded state,
// not an absence).
func TestMigration017_BackfillOneOpenRowPerRegion(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: leaflabTimescaleImage})
	dbSQL, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open sql handle: %v", err)
	}
	t.Cleanup(func() { _ = dbSQL.Close() })
	runner := migrate.NewRunner(dbSQL, schema.Migrations, schema.Dir)

	if err := runner.Migrate(16); err != nil {
		t.Fatalf("migrate to 016: %v", err)
	}

	// Seed a three-level hierarchy plus a second top-level region, all
	// before 017 applies so the backfill must cover them.
	grandparent := migration017SeedRegion(t, dbSQL, "campus", 0)
	parent := migration017SeedRegion(t, dbSQL, "greenhouse-a", grandparent)
	child := migration017SeedRegion(t, dbSQL, "shelf-1", parent)
	otherTop := migration017SeedRegion(t, dbSQL, "annex", 0)

	if err := runner.Migrate(17); err != nil {
		t.Fatalf("migrate to 017: %v", err)
	}

	for _, tc := range []struct {
		name         string
		regionID     int64
		wantParentID int64 // 0 = top-level, expect NULL
	}{
		{"campus (top-level)", grandparent, 0},
		{"greenhouse-a", parent, grandparent},
		{"shelf-1", child, parent},
		{"annex (top-level)", otherTop, 0},
	} {
		open, gotParent := openParentRowsForRegion(t, dbSQL, tc.regionID)
		if open != 1 {
			t.Fatalf("%s: expected exactly one open region_parent_history row, got %d", tc.name, open)
		}
		if tc.wantParentID == 0 {
			if gotParent != nil {
				t.Fatalf("%s: expected open row's parent_region_id to be NULL (top-level), got %d", tc.name, *gotParent)
			}
		} else if gotParent == nil || *gotParent != tc.wantParentID {
			t.Fatalf("%s: expected open row's parent_region_id to be %d, got %v", tc.name, tc.wantParentID, gotParent)
		}
	}

	// No stray rows at all: one history row per pre-existing region.
	var totalRegions, totalHistory int
	if err := dbSQL.QueryRow(`SELECT COUNT(*) FROM region`).Scan(&totalRegions); err != nil {
		t.Fatalf("count regions: %v", err)
	}
	if err := dbSQL.QueryRow(`SELECT COUNT(*) FROM region_parent_history`).Scan(&totalHistory); err != nil {
		t.Fatalf("count region_parent_history rows: %v", err)
	}
	if totalHistory != totalRegions {
		t.Fatalf("expected one history row per region (%d regions), got %d history rows", totalRegions, totalHistory)
	}
}

// TestMigration017_UpDownRoundTrip proves migration 017 applies cleanly
// from 016, rolls back cleanly via its down migration, and re-applies with
// the backfill still producing exactly one open row per region.
func TestMigration017_UpDownRoundTrip(t *testing.T) {
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Image: leaflabTimescaleImage})
	dbSQL, err := sql.Open("pgx", db.ConnString)
	if err != nil {
		t.Fatalf("open sql handle: %v", err)
	}
	t.Cleanup(func() { _ = dbSQL.Close() })
	runner := migrate.NewRunner(dbSQL, schema.Migrations, schema.Dir)

	if err := runner.Migrate(16); err != nil {
		t.Fatalf("migrate to 016: %v", err)
	}
	top := migration017SeedRegion(t, dbSQL, "roundtrip-top", 0)
	nested := migration017SeedRegion(t, dbSQL, "roundtrip-nested", top)

	if err := runner.Migrate(17); err != nil {
		t.Fatalf("migrate up to 017: %v", err)
	}
	version, dirty, verr := runner.Version()
	if verr != nil || version != 17 || dirty {
		t.Fatalf("expected clean version 17 after up, got version=%d dirty=%v err=%v", version, dirty, verr)
	}

	// Backfill present after up.
	for _, regionID := range []int64{top, nested} {
		if open, _ := openParentRowsForRegion(t, dbSQL, regionID); open != 1 {
			t.Fatalf("region %d: expected 1 open history row after up, got %d", regionID, open)
		}
	}

	// Down: 017's tables and the board mirror column must be gone.
	if err := runner.Migrate(16); err != nil {
		t.Fatalf("migrate down to 016: %v", err)
	}
	version, dirty, verr = runner.Version()
	if verr != nil || version != 16 || dirty {
		t.Fatalf("expected clean version 16 after down, got version=%d dirty=%v err=%v", version, dirty, verr)
	}
	for _, table := range []string{"region_parent_history", "board_region_history"} {
		var exists bool
		if err := dbSQL.QueryRow(`SELECT EXISTS (
			SELECT 1 FROM information_schema.tables
			WHERE table_schema = 'public' AND table_name = $1)`, table).Scan(&exists); err != nil {
			t.Fatalf("check table %s after down: %v", table, err)
		}
		if exists {
			t.Fatalf("expected table %s to be dropped by migration 017's down migration", table)
		}
	}
	var boardHasRegionColumn bool
	if err := dbSQL.QueryRow(`SELECT EXISTS (
		SELECT 1 FROM information_schema.columns
		WHERE table_schema = 'public' AND table_name = 'board' AND column_name = 'region_id')`,
	).Scan(&boardHasRegionColumn); err != nil {
		t.Fatalf("check board.region_id after down: %v", err)
	} else if boardHasRegionColumn {
		t.Fatal("expected board.region_id to be dropped by migration 017's down migration")
	}

	// Re-up: tables return and the backfill re-runs for the same regions.
	if err := runner.Migrate(17); err != nil {
		t.Fatalf("re-migrate up to 017: %v", err)
	}
	for _, regionID := range []int64{top, nested} {
		open, parent := openParentRowsForRegion(t, dbSQL, regionID)
		if open != 1 {
			t.Fatalf("region %d: expected exactly 1 open history row after re-up, got %d", regionID, open)
		}
		if regionID == top && parent != nil {
			t.Fatalf("region %d: expected NULL parent after re-up backfill, got %d", regionID, *parent)
		}
		if regionID == nested && (parent == nil || *parent != top) {
			t.Fatalf("region %d: expected parent %d after re-up backfill, got %v", regionID, top, parent)
		}
	}
}

// TestMigration017_Shape_NullableParentAndNotNullRegion proves the
// deliberate asymmetry from the issue: region_parent_history.parent_region_id
// is nullable (a top-level region's NULL parent is recorded state, so an
// explicit NULL insert succeeds), while board_region_history.region_id is
// NOT NULL (a board's un-recorded state is the absence of an open row).
func TestMigration017_Shape_NullableParentAndNotNullRegion(t *testing.T) {
	pool := newLeafLabTestPool(t) // fully migrated, 017 included
	ctx := context.Background()

	top := migration016SeedRegion(t, pool, "shape-top", 0)


	// NULL parent on an open row must be accepted for regions.
	var rowID int64
	if err := pool.QueryRow(ctx, `
		INSERT INTO region_parent_history (region_id, parent_region_id)
		VALUES ($1, NULL) RETURNING history_id`, top).Scan(&rowID); err != nil {
		t.Fatalf("inserting an open region_parent_history row with NULL parent_region_id should succeed: %v", err)
	}

	// board_region_history.region_id must reject NULL.
	boardID := migration016SeedBoard(t, pool, "board-shape")
	if _, err := pool.Exec(ctx, `
		INSERT INTO board_region_history (board_id, region_id)
		VALUES ($1, NULL)`, boardID); err == nil {
		t.Fatal("inserting board_region_history with NULL region_id should have failed (region_id NOT NULL), got no error")
	}

	// A valid board_region_history row works, and the mirror column is
	// independently settable (and stays NULL when unset).
	var mirror *int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM board WHERE board_id = $1`, boardID).Scan(&mirror); err != nil {
		t.Fatalf("select board.region_id mirror: %v", err)
	}
	if mirror != nil {
		t.Fatalf("expected board.region_id mirror to default NULL with no recorded region, got %d", *mirror)
	}
	if _, err := pool.Exec(ctx, `UPDATE board SET region_id = $1 WHERE board_id = $2`, top, boardID); err != nil {
		t.Fatalf("set board.region_id mirror: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO board_region_history (board_id, region_id)
		VALUES ($1, $2)`, boardID, top); err != nil {
		t.Fatalf("insert valid board_region_history row: %v", err)
	}
}

// TestMigration017_OpenRowPartialIndexesExist proves the SCD2-convention
// partial indexes exist on both new history tables.
func TestMigration017_OpenRowPartialIndexesExist(t *testing.T) {
	pool := newLeafLabTestPool(t)
	ctx := context.Background()

	for _, tc := range []struct {
		index string
		table string
	}{
		{"idx_region_parent_history_current", "region_parent_history"},
		{"idx_board_region_history_current", "board_region_history"},
	} {
		var partial string
		if err := pool.QueryRow(ctx,
			`SELECT indexdef FROM pg_indexes WHERE indexname = $1`, tc.index).Scan(&partial); err != nil {
			t.Fatalf("expected partial index %s to exist: %v", tc.index, err)
		}
		if !containsIgnoreCase(partial, "valid_to IS NULL") {
			t.Fatalf("index %s exists but is not the open-row partial index; indexdef = %s", tc.index, partial)
		}
		if !containsIgnoreCase(partial, tc.table) {
			t.Fatalf("index %s is not on %s; indexdef = %s", tc.index, tc.table, partial)
		}
	}
}

func containsIgnoreCase(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if equalFold(s[i:i+len(sub)], sub) {
			return true
		}
	}
	return false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
