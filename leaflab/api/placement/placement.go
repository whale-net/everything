// Package placement implements the SCD2 close-and-open writer for
// plant_region_history (migration 016, FR19, FR21, NFR6.1) and the
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
// Scaffold only (this task's Scaffold phase): the close-and-open write and
// the back-dating refusal are filled in during this task's Implementation
// phase, ordered before FR25/FR28/FR72 per NFR8. The migration's
// attribution-neutral, snapped-to-hour backfill (FR21) and the
// database-side no-back-dating guard (NFR6.2: CHECK/trigger asserting
// valid_from <= NOW() on insert) land in that same Implementation-phase
// migration -- not here.
package placement

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotImplemented is returned by every Writer method and by
// RefuseIfBackdated until this task's Implementation phase fills in the
// close-and-open write path and the back-dating guard.
var ErrNotImplemented = errors.New("placement: not implemented (Implementation phase, FR19/FR21)")

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
// necessarily "now" as observed by the writer. Implementation fills in:
// refusing via RefuseIfBackdated when requestedAt is earlier than the
// instant the request is processed (FR19: "an interval opens at the
// instant the change is recorded", never back-dated), then performing the
// close-and-open write per AGENTS.md's SCD2 write path.
func (w *Writer) Move(ctx context.Context, plantID, regionID int64, requestedAt time.Time) error {
	return ErrNotImplemented
}

// RefuseIfBackdated is the no-back-dating guard (FR19): it returns a
// contract.Refuse error, stating the reason per FR59.3, when requestedAt
// is earlier than the moment the check is performed. Move calls this
// before writing; the database also enforces the same rule independently
// (NFR6.2) so the rule holds even for a direct insert that bypasses this
// package.
func RefuseIfBackdated(requestedAt time.Time) error {
	return ErrNotImplemented
}
