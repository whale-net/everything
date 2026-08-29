// Package placement implements the SCD2 close-and-open writer for
// plant_region_history (migration 017, FR19, FR21, NFR6.1) and the
// no-back-dating guard that keeps a placement boundary from ever being
// recorded into the past (FR19, FR59.3).
//
// This is defect 1's fix: plant, plant_type and v_sensor_reading_with_plant
// have existed since migration 001 and nothing writes them -- the view
// joins p.region_id = e.region_id (current placement, exact equality), so
// moving a plant re-attributes every reading it ever produced.
// plant_region_history breaks that by recording placement as a proper SCD2
// history instead of an in-place mutation on plant.region_id.
//
// Migration 017 carries the attribution-neutral, snapped-to-hour backfill
// (FR21) and the database-side no-back-dating guard (NFR6.2: a BEFORE
// INSERT trigger asserting valid_from <= NOW(), since CHECK constraints
// cannot call the non-IMMUTABLE NOW()). This package is the caller-facing
// half: Writer.Move performs the close-and-open write, and
// RefuseIfBackdated is the guard Move applies before writing.
package placement

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/contract"
)

// Writer is the SCD2 close-and-open writer for plant_region_history: a
// move closes the plant's current open interval and opens a new one --
// nothing is updated in place (FR19). Both index directions required by
// NFR6.1 (plant-to-region at T, region-to-plant at T) are served by the
// plant_region_history table this Writer writes to.
type Writer struct {
	db *pgxpool.Pool
}

// NewWriter constructs a Writer over db.
func NewWriter(db *pgxpool.Pool) *Writer {
	return &Writer{db: db}
}

// Move records plantID's placement into regionID as of requestedAt,
// closing the plant's current open plant_region_history interval (if any)
// and opening a new one in the same transaction.
//
// requestedAt is the boundary the caller is asking to open -- not
// necessarily "now" as observed by the writer. Move first calls
// RefuseIfBackdated(requestedAt): if the caller is asking for a boundary
// earlier than the instant the request is processed, nothing is written
// (FR19: "an interval opens at the instant the change is recorded", never
// back-dated; FR59.3's refuse-before-write contract). The actual written
// valid_from is always the database's own NOW() at INSERT time (the
// column's DEFAULT), never requestedAt verbatim -- that is what keeps the
// interval boundary from ever drifting from "the instant it was recorded"
// even when a caller supplies a requestedAt further in the future than the
// instant Move actually executes.
//
// plant.region_id is updated in the same transaction as a current-value
// cache (see migration 017's up.sql doc comment) so the pre-FR72 read path
// keeps resolving a plant's region correctly until that later task repoints
// it onto plant_region_history directly.
func (w *Writer) Move(ctx context.Context, plantID, regionID int64, requestedAt time.Time) error {
	tx, err := w.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("placement: begin move transaction for plant %d: %w", plantID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit succeeds

	if _, err := MoveTx(ctx, tx, plantID, regionID, requestedAt); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("placement: commit move for plant %d: %w", plantID, err)
	}
	return nil
}

// MoveTx is Move's transaction-scoped twin: it performs the identical
// close-and-open write against tx (the caller's own already-open
// transaction) and returns the written valid_from, without beginning or
// committing anything itself. Move (above) is now a thin wrapper around
// this -- begin, MoveTx, commit -- kept as the single-shot entry point for
// a caller with no transaction of its own (e.g. this package's own
// integration tests).
//
// MoveTx exists for callers that must combine this write with something
// else in the exact same transaction -- Phase 5's CreatePlant/MovePlant
// (leaflab/api/plants.go) combine it with FR20's phase-one boundary
// capture (leaflab/api/capture.Recorder.Record) and FR8's audit record, per
// FR20's coupling requirement ("every placement boundary written here must
// trigger phase one of FR20's two-phase boundary capture, in the same
// transaction"); see capture.Recorder.Record's doc comment, which names
// this package's writer as one of its two intended callers.
//
// Every other behavior (RefuseIfBackdated applied first, valid_from is
// always the database's own NOW() at INSERT time, plant.region_id synced
// in the same transaction) is identical to Move -- see Move's doc comment.
func MoveTx(ctx context.Context, tx pgx.Tx, plantID, regionID int64, requestedAt time.Time) (time.Time, error) {
	return moveTx(ctx, tx, plantID, regionID, requestedAt, false)
}

// MoveRelocatedTx is MoveTx's relocation-marked twin (FR74's "move every
// current plant placement into the mirrored regions" clause, FR24): the
// identical close-and-open write, except the opened interval is marked
// relocation_induced = TRUE, so a plant's timeline distinguishes "this
// plant moved" (MoveTx, FALSE) from "the region this plant was in moved"
// (MoveRelocatedTx, TRUE). This is FR74's reuse of this package's writer --
// not a third placement path -- see leaflab/api/relocate.go, which is this
// function's only caller. requestedAt still goes through
// RefuseIfBackdated (relocate.go always passes the relocation
// transaction's own instant, which is never in the past), and valid_from
// is still always the database's own NOW() at INSERT time, exactly like
// MoveTx -- see MoveTx's doc comment for both.
func MoveRelocatedTx(ctx context.Context, tx pgx.Tx, plantID, regionID int64, requestedAt time.Time) (time.Time, error) {
	return moveTx(ctx, tx, plantID, regionID, requestedAt, true)
}

// moveTx is MoveTx/MoveRelocatedTx's shared implementation -- identical in
// every respect except the relocation_induced value written to the opened
// interval (FR24's relocation-induced half, migration 017's column).
func moveTx(ctx context.Context, tx pgx.Tx, plantID, regionID int64, requestedAt time.Time, relocationInduced bool) (time.Time, error) {
	if err := RefuseIfBackdated(requestedAt); err != nil {
		return time.Time{}, err
	}

	// Close-and-open per AGENTS.md's SCD2 write path: close the plant's
	// current open interval (a plant with no prior interval simply closes
	// zero rows -- not an error, since this is also the first Move a plant
	// ever receives after the backfill's initial interval already covers
	// it, or the very first placement CreatePlant ever opens), then open
	// the new one. valid_from is left to the column DEFAULT NOW() rather
	// than requestedAt, per Move's doc comment above.
	if _, err := tx.Exec(ctx, `
		UPDATE plant_region_history
		SET valid_to = NOW()
		WHERE plant_id = $1 AND valid_to IS NULL
	`, plantID); err != nil {
		return time.Time{}, fmt.Errorf("placement: close current interval for plant %d: %w", plantID, err)
	}

	var validFrom time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO plant_region_history (plant_id, region_id, relocation_induced)
		VALUES ($1, $2, $3)
		RETURNING valid_from
	`, plantID, regionID, relocationInduced).Scan(&validFrom); err != nil {
		return time.Time{}, fmt.Errorf("placement: open new interval for plant %d in region %d: %w", plantID, regionID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE plant SET region_id = $2 WHERE plant_id = $1
	`, plantID, regionID); err != nil {
		return time.Time{}, fmt.Errorf("placement: sync plant.region_id cache for plant %d: %w", plantID, err)
	}

	return validFrom, nil
}

// backdateTolerance absorbs the clock-read gap between a caller capturing
// requestedAt and this function's own call to time.Now() -- two sequential
// time.Now() reads are never equal, so a strict requestedAt.Before(time.Now())
// comparison refuses every literal "move it now" caller (requestedAt
// captured a few nanoseconds before this function's Now() call), not just a
// requestedAt that is genuinely, meaningfully in the past. One second is far
// larger than that gap ever is in practice while still catching any real
// back-dated request (FR19's target is minutes/hours/days in the past, not
// nanoseconds), so it draws the line requestedAt.Before(time.Now()) failed
// to draw.
const backdateTolerance = time.Second

// RefuseIfBackdated is the no-back-dating guard (FR19): it returns a
// contract.Refuse error, stating the reason per FR59.3, when requestedAt is
// meaningfully earlier than the moment the check is performed -- "now" and
// any future instant are both accepted; see backdateTolerance's doc comment
// for why "now" needs slack at all. Move calls this before writing; the
// database also enforces the same rule independently (NFR6.2, migration
// 017's trg_plant_region_history_no_future_valid_from) so the rule holds
// even for a direct insert that bypasses this package.
func RefuseIfBackdated(requestedAt time.Time) error {
	if requestedAt.Before(time.Now().Add(-backdateTolerance)) {
		return contract.Refuse(
			"plant_region_history",
			"valid_from",
			"A placement boundary cannot be opened in the past -- it opens at the instant the move is recorded.",
			"Retry the move without a past boundary; the interval will open at the current instant.",
		)
	}
	return nil
}
