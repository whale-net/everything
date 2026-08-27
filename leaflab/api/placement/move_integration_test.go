//go:build integration

// Real-Postgres integration coverage for Writer.Move's SCD2 close-and-open
// write path (FR19, NFR6.1) and the database-side no-back-dating guard
// (NFR6.2) it writes against. testSchema below is self-contained,
// hand-written DDL mirroring migration 017_plant_region_history's table and
// trigger exactly (not an embed of leaflab/migrate's migrations -- see
// dbtest's README on why these tests stay hermetic). See
// //libs/go/dbtest's README for how to run tests like this one.
package placement

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/libs/go/dbtest"
)

// testSchema mirrors migration 017_plant_region_history.up.sql's table,
// indexes and no-back-dating trigger, plus the minimal plant/region tables
// Move's foreign keys and plant.region_id cache write need. Deliberately
// does not pull in the rest of leaflab's schema (board, sensor, etc.) --
// this package's Writer touches only plant, region and plant_region_history.
const testSchema = `
	CREATE TABLE region (
		region_id BIGSERIAL PRIMARY KEY,
		name      VARCHAR(255) NOT NULL
	);

	CREATE TABLE plant (
		plant_id  BIGSERIAL PRIMARY KEY,
		region_id BIGINT NOT NULL REFERENCES region(region_id),
		name      VARCHAR(128) NOT NULL
	);

	CREATE TABLE plant_region_history (
		plant_region_history_id BIGSERIAL PRIMARY KEY,
		plant_id           BIGINT NOT NULL REFERENCES plant(plant_id),
		region_id          BIGINT NOT NULL REFERENCES region(region_id),
		valid_from         TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		valid_to           TIMESTAMPTZ,
		relocation_induced BOOLEAN NOT NULL DEFAULT FALSE
	);

	CREATE INDEX idx_plant_region_history_plant_id_current
		ON plant_region_history(plant_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_plant_region_history_region_id_current
		ON plant_region_history(region_id) WHERE valid_to IS NULL;
	CREATE INDEX idx_plant_region_history_plant_id_temporal
		ON plant_region_history(plant_id, valid_from, valid_to);
	CREATE INDEX idx_plant_region_history_region_id_temporal
		ON plant_region_history(region_id, valid_from, valid_to);

	CREATE FUNCTION enforce_plant_region_history_no_future_valid_from() RETURNS TRIGGER AS $$
	BEGIN
		IF NEW.valid_from > NOW() THEN
			RAISE EXCEPTION 'plant_region_history.valid_from (%) cannot be later than now (%) -- an interval opens at the instant it is recorded (FR19, NFR6.2)',
				NEW.valid_from, NOW();
		END IF;
		RETURN NEW;
	END;
	$$ LANGUAGE plpgsql;

	CREATE TRIGGER trg_plant_region_history_no_future_valid_from
		BEFORE INSERT ON plant_region_history
		FOR EACH ROW
		EXECUTE FUNCTION enforce_plant_region_history_no_future_valid_from();
`

// newTestFixture starts a real Postgres container with testSchema applied,
// seeds one region and one plant already open into it (mimicking the state
// migration 017's backfill leaves every existing plant in), and returns a
// Writer plus the raw pool and ids for assertions.
func newTestFixture(t *testing.T) (writer *Writer, pool *pgxpool.Pool, regionID, otherRegionID, plantID int64) {
	t.Helper()
	ctx := context.Background()
	db := dbtest.NewPostgres(ctx, t, dbtest.Options{Schema: testSchema})

	mustExec := func(dest *int64, query string, args ...any) {
		t.Helper()
		if err := db.Pool.QueryRow(ctx, query, args...).Scan(dest); err != nil {
			t.Fatalf("fixture setup %q: %v", query, err)
		}
	}

	mustExec(&regionID, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "origin-region")
	mustExec(&otherRegionID, `INSERT INTO region (name) VALUES ($1) RETURNING region_id`, "destination-region")
	mustExec(&plantID, `INSERT INTO plant (region_id, name) VALUES ($1, $2) RETURNING plant_id`, regionID, "test-plant")

	// The initial open interval every plant already has after migration
	// 017's backfill -- Move's close-and-open write path is only meaningful
	// against a plant that already has a current interval to close.
	if _, err := db.Pool.Exec(ctx, `INSERT INTO plant_region_history (plant_id, region_id) VALUES ($1, $2)`, plantID, regionID); err != nil {
		t.Fatalf("seed initial open interval: %v", err)
	}

	return NewWriter(db.Pool), db.Pool, regionID, otherRegionID, plantID
}

// TestWriterMove_ClosesCurrentIntervalAndOpensNew proves the close-and-open
// write path (FR19): after Move, the plant's prior interval is closed
// (valid_to set), exactly one open interval exists for the plant, that open
// interval names the new region, and nothing was updated in place (the
// original row is still present, just closed -- not deleted or repurposed).
func TestWriterMove_ClosesCurrentIntervalAndOpensNew(t *testing.T) {
	writer, pool, regionID, otherRegionID, plantID := newTestFixture(t)
	ctx := context.Background()

	if err := writer.Move(ctx, plantID, otherRegionID, time.Now().Add(1 * time.Second)); err != nil {
		t.Fatalf("Move: %v", err)
	}

	var totalRows int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plantID).Scan(&totalRows); err != nil {
		t.Fatalf("count total intervals: %v", err)
	}
	if totalRows != 2 {
		t.Fatalf("total plant_region_history rows for plant = %d, want 2 (original closed + new open, nothing updated in place)", totalRows)
	}

	var openCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plantID).Scan(&openCount); err != nil {
		t.Fatalf("count open intervals: %v", err)
	}
	if openCount != 1 {
		t.Fatalf("open plant_region_history rows for plant = %d, want exactly 1", openCount)
	}

	var currentRegion int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plantID).Scan(&currentRegion); err != nil {
		t.Fatalf("read current interval region: %v", err)
	}
	if currentRegion != otherRegionID {
		t.Errorf("current interval region_id = %d, want the new region %d", currentRegion, otherRegionID)
	}

	var originalClosed bool
	if err := pool.QueryRow(ctx, `
		SELECT valid_to IS NOT NULL FROM plant_region_history WHERE plant_id = $1 AND region_id = $2
	`, plantID, regionID).Scan(&originalClosed); err != nil {
		t.Fatalf("read original interval closed state: %v", err)
	}
	if !originalClosed {
		t.Error("original interval's valid_to is still NULL after Move, want it closed")
	}
}

// TestWriterMove_SyncsPlantRegionIDCache proves plant.region_id -- the
// pre-FR72 read-path cache -- is updated in the same transaction as the
// history write.
func TestWriterMove_SyncsPlantRegionIDCache(t *testing.T) {
	writer, pool, _, otherRegionID, plantID := newTestFixture(t)
	ctx := context.Background()

	if err := writer.Move(ctx, plantID, otherRegionID, time.Now().Add(1 * time.Second)); err != nil {
		t.Fatalf("Move: %v", err)
	}

	var cachedRegionID int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant WHERE plant_id = $1`, plantID).Scan(&cachedRegionID); err != nil {
		t.Fatalf("read plant.region_id cache: %v", err)
	}
	if cachedRegionID != otherRegionID {
		t.Errorf("plant.region_id cache = %d, want the new region %d", cachedRegionID, otherRegionID)
	}
}

// TestWriterMove_RefusesBackdatedRequest proves Move applies
// RefuseIfBackdated before writing anything: a requestedAt in the past is
// refused, and the plant's history is left completely untouched (still the
// single open interval from setup).
func TestWriterMove_RefusesBackdatedRequest(t *testing.T) {
	writer, pool, regionID, otherRegionID, plantID := newTestFixture(t)
	ctx := context.Background()

	past := time.Now().Add(-1 * time.Hour)
	err := writer.Move(ctx, plantID, otherRegionID, past)
	if err == nil {
		t.Fatal("Move with a backdated requestedAt succeeded, want it refused")
	}

	var rowCount int
	if err := pool.QueryRow(ctx, `SELECT COUNT(*) FROM plant_region_history WHERE plant_id = $1`, plantID).Scan(&rowCount); err != nil {
		t.Fatalf("count intervals after refused Move: %v", err)
	}
	if rowCount != 1 {
		t.Fatalf("plant_region_history rows after refused Move = %d, want 1 (untouched -- nothing written before the refusal)", rowCount)
	}

	var currentRegion int64
	if err := pool.QueryRow(ctx, `SELECT region_id FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plantID).Scan(&currentRegion); err != nil {
		t.Fatalf("read current interval region after refused Move: %v", err)
	}
	if currentRegion != regionID {
		t.Errorf("current interval region_id after refused Move = %d, want unchanged original region %d", currentRegion, regionID)
	}
}

// TestWriterMove_AcceptsLiteralNowBoundary proves Move succeeds for FR19's
// primary use case: a caller recording a move as of the instant they ask for
// it, by passing time.Now() captured immediately before the call -- the most
// natural way to express "move this plant right now". RefuseIfBackdated's
// current implementation compares requestedAt.Before(time.Now()) using a
// strict less-than against a second, later call to time.Now(): any
// requestedAt captured even one nanosecond earlier than that second call
// -- which describes every real caller, since capturing a timestamp and
// then calling Move always takes a non-zero, non-negative amount of time
// -- is judged "before now" and refused. That makes literally every
// legitimate move-it-now caller indistinguishable from FR19's actual target
// (a caller explicitly asking to open a boundary meaningfully in the past),
// even though nothing was actually backdated. Move's own doc comment
// confirms the intent: "requestedAt is the boundary the caller is asking to
// open -- not necessarily 'now' as observed by the writer" -- describing
// exactly this case as one that should be accepted, not refused.
func TestWriterMove_AcceptsLiteralNowBoundary(t *testing.T) {
	writer, _, _, otherRegionID, plantID := newTestFixture(t)
	ctx := context.Background()

	requestedAt := time.Now()
	if err := writer.Move(ctx, plantID, otherRegionID, requestedAt); err != nil {
		t.Errorf("Move(plantID, otherRegionID, time.Now()) = %v, want nil -- a literal \"move it now\" boundary must not be refused as backdated (FR19)", err)
	}
}

// TestWriterMove_DatabaseGuardIndependentOfWriter proves NFR6.2's
// independent database-side guard: a direct INSERT with a future valid_from
// -- bypassing Writer.Move entirely -- is still refused by the trigger.
func TestWriterMove_DatabaseGuardIndependentOfWriter(t *testing.T) {
	_, pool, regionID, _, plantID := newTestFixture(t)
	ctx := context.Background()

	future := time.Now().Add(1 * time.Hour)
	_, err := pool.Exec(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, valid_from) VALUES ($1, $2, $3)
	`, plantID, regionID, future)
	if err == nil {
		t.Error("direct INSERT with a future valid_from succeeded, want it refused by the database trigger (NFR6.2)")
	}
}

// TestWriterMove_WrittenValidFromIsDatabaseNowNotRequestedAt proves Move's
// doc-commented behavior: the interval actually opens at the database's own
// NOW() at INSERT time (the column DEFAULT), not at the requestedAt value
// the caller supplied, even when requestedAt is further in the future than
// the instant Move actually executes.
func TestWriterMove_WrittenValidFromIsDatabaseNowNotRequestedAt(t *testing.T) {
	writer, pool, _, otherRegionID, plantID := newTestFixture(t)
	ctx := context.Background()

	before := time.Now()
	requestedAt := before.Add(24 * time.Hour) // far in the future -- accepted by RefuseIfBackdated (not in the past), but must not be written verbatim.
	if err := writer.Move(ctx, plantID, otherRegionID, requestedAt); err != nil {
		t.Fatalf("Move: %v", err)
	}
	after := time.Now()

	var writtenValidFrom time.Time
	if err := pool.QueryRow(ctx, `SELECT valid_from FROM plant_region_history WHERE plant_id = $1 AND valid_to IS NULL`, plantID).Scan(&writtenValidFrom); err != nil {
		t.Fatalf("read written valid_from: %v", err)
	}

	if writtenValidFrom.Equal(requestedAt) {
		t.Fatal("written valid_from equals the caller-supplied requestedAt, want the database's own NOW() at insert time")
	}
	if writtenValidFrom.Before(before) || writtenValidFrom.After(after) {
		t.Errorf("written valid_from = %v, want it between the Move call's start (%v) and end (%v)", writtenValidFrom, before, after)
	}
}
