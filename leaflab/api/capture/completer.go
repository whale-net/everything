package capture

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/whale-net/everything/leaflab/api/tiers"
)

// Completer is phase two of FR20's two-phase boundary capture: at bucket
// close, it computes each pending capture's partials from raw -- both
// sides, independently -- and durably writes them as boundary_partial rows
// (migration 033) before marking the capture completed. It never derives
// one side from `full_bucket - other_side`, for any of count, sum, min or
// max (A17): min and max are not invertible, and keeping every aggregate
// on one code path avoids a special case that would only be safe for
// sum/count.
type Completer struct {
	db *pgxpool.Pool
}

// NewCompleter constructs a Completer over db. Unlike Recorder, Completer
// owns its own transactions -- it runs independently of any caller's
// placement-write transaction, on its own schedule (see this package's doc
// comment for where that schedule lives and why).
func NewCompleter(db *pgxpool.Pool) *Completer {
	return &Completer{db: db}
}

// RunPending finds every boundary_capture row whose bucket has closed and
// is still state = 'pending' (idx_boundary_capture_pending, migration 033),
// computes its partials from raw, and marks the capture completed -- each
// capture as one unit of work, so a partially-completed capture is never
// left half-written.
//
// Tiers are processed finest first (five-minute, then hourly): the hourly
// tier's partials are composed from the five-minute tier's completed
// partials and full buckets (FR20.3), so every closed five-minute capture
// for a given stretch of time must finish before the hourly capture that
// covers it is attempted.
//
// After both tiers are processed, RunPending checks NFR5's ordering
// constraint -- a capture still pending as its raw chunk nears retention --
// and returns ErrPendingNearRetention (never silently) if it finds one.
func (c *Completer) RunPending(ctx context.Context) error {
	if err := c.completeTier(ctx, tiers.TierFiveMinute); err != nil {
		return err
	}
	if err := c.completeTier(ctx, tiers.TierHourly); err != nil {
		return err
	}
	return c.checkPendingNearRetention(ctx, time.Now())
}

// pendingBucket identifies one (sensor, tier, bucket) group with at least
// one pending boundary_capture row whose bucket has closed.
type pendingBucket struct {
	sensorID    int64
	bucketStart time.Time
}

// completeTier processes every closed, pending bucket at tier, oldest
// bucket first -- a stable, predictable order that also means an interrupted
// run (process restart mid-tier) simply resumes with the same oldest
// buckets on its next tick, never skipping ahead.
func (c *Completer) completeTier(ctx context.Context, tier tiers.Tier) error {
	width, ok := tierBucketWidth[tier]
	if !ok {
		return fmt.Errorf("capture: no bucket width configured for tier %q", tier)
	}

	buckets, err := c.closedPendingBuckets(ctx, tier, width)
	if err != nil {
		return err
	}

	for _, b := range buckets {
		if err := c.completeBucket(ctx, tier, width, b.sensorID, b.bucketStart); err != nil {
			return fmt.Errorf("capture: complete %s bucket %s for sensor %d: %w", tier, b.bucketStart, b.sensorID, err)
		}
	}
	return nil
}

// closedPendingBuckets lists the distinct (sensor_id, bucket_start) pairs at
// tier that have at least one pending capture whose bucket has already
// closed (bucket_start + width <= now).
func (c *Completer) closedPendingBuckets(ctx context.Context, tier tiers.Tier, width string) ([]pendingBucket, error) {
	rows, err := c.db.Query(ctx, `
		SELECT DISTINCT sensor_id, bucket_start
		FROM boundary_capture
		WHERE tier = $1
		  AND state = 'pending'
		  AND bucket_start + $2::interval <= NOW()
		ORDER BY bucket_start, sensor_id
	`, string(tier), width)
	if err != nil {
		return nil, fmt.Errorf("capture: list closed pending %s buckets: %w", tier, err)
	}
	defer rows.Close()

	var out []pendingBucket
	for rows.Next() {
		var b pendingBucket
		if err := rows.Scan(&b.sensorID, &b.bucketStart); err != nil {
			return nil, fmt.Errorf("capture: scan closed pending %s bucket: %w", tier, err)
		}
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("capture: iterate closed pending %s buckets: %w", tier, err)
	}
	return out, nil
}

// completeBucket drains every pending capture for (tier, sensorID,
// bucketStart), oldest boundary_at first -- each one processed as its own
// transaction (completeOne), so N boundaries in this bucket are settled as
// N sequential, individually-durable splits (FR20.3's induction), not one
// large transaction that could leave nothing durable on failure partway
// through.
func (c *Completer) completeBucket(ctx context.Context, tier tiers.Tier, width string, sensorID int64, bucketStart time.Time) error {
	for {
		var captureID int64
		var boundaryAt time.Time
		err := c.db.QueryRow(ctx, `
			SELECT capture_id, boundary_at
			FROM boundary_capture
			WHERE tier = $1 AND sensor_id = $2 AND bucket_start = $3 AND state = 'pending'
			ORDER BY boundary_at ASC
			LIMIT 1
		`, string(tier), sensorID, bucketStart).Scan(&captureID, &boundaryAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // nothing left pending for this bucket
		}
		if err != nil {
			return fmt.Errorf("capture: find next pending capture: %w", err)
		}

		if err := c.completeOne(ctx, tier, width, sensorID, bucketStart, captureID, boundaryAt); err != nil {
			return err
		}
	}
}

// completeOne settles a single boundary_capture row: it finds the partial
// (or, for the bucket's first boundary, the implicit whole bucket) that
// boundaryAt falls inside, and replaces it with two partials split at
// boundaryAt -- each computed from raw or, for the hourly tier, composed
// from the five-minute tier (never by subtraction, A17) -- and marks the
// capture completed. If boundaryAt lands exactly on the target interval's
// own from or to (nothing on one side to split off), it writes the whole
// interval as a single partial instead, since a two-way split there would
// produce a zero-width partial violating boundary_partial's partial_from <
// partial_to CHECK (migration 033). Runs entirely inside one transaction,
// so a capture is never left half-settled: either the new partial(s) and
// the completed state land together, or nothing does.
func (c *Completer) completeOne(ctx context.Context, tier tiers.Tier, width string, sensorID int64, bucketStart time.Time, captureID int64, boundaryAt time.Time) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return fmt.Errorf("capture: begin completion for capture %d: %w", captureID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }() // no-op once Commit succeeds

	// Lock the row and re-check state: a concurrent completer run (e.g. an
	// overlapping tick) may already have settled it.
	var state string
	if err := tx.QueryRow(ctx, `
		SELECT state FROM boundary_capture WHERE capture_id = $1 FOR UPDATE
	`, captureID).Scan(&state); err != nil {
		return fmt.Errorf("capture: lock capture %d: %w", captureID, err)
	}
	if state != "pending" {
		return tx.Commit(ctx)
	}

	partialFrom, partialTo, existingPartialID, err := c.findSplitTarget(ctx, tx, tier, sensorID, bucketStart, boundaryAt, width)
	if err != nil {
		return fmt.Errorf("capture: find split target for capture %d: %w", captureID, err)
	}

	if existingPartialID.Valid {
		if _, err := tx.Exec(ctx, `DELETE FROM boundary_partial WHERE partial_id = $1`, existingPartialID.Int64); err != nil {
			return fmt.Errorf("capture: delete superseded partial %d: %w", existingPartialID.Int64, err)
		}
	}

	aggregate := rawRestrictedAggregate
	if tier == tiers.TierHourly {
		aggregate = fiveMinuteComposedAggregate
	}

	// boundaryAt can land exactly on partialFrom or partialTo (e.g. a
	// boundary aligned to the bucket's own start, the bucket's first
	// boundary case in findSplitTarget). Splitting [from, to) at either
	// endpoint would produce a zero-width partial, violating
	// boundary_partial's partial_from < partial_to CHECK (migration 033).
	// When there's nothing on one side of the split, there is nothing to
	// split: write the whole [partialFrom, partialTo) as a single partial
	// instead of two.
	if boundaryAt.Equal(partialFrom) || boundaryAt.Equal(partialTo) {
		whole, err := aggregate(ctx, tx, sensorID, partialFrom, partialTo)
		if err != nil {
			return err
		}
		if err := insertPartial(ctx, tx, captureID, tier, bucketStart, partialFrom, partialTo, whole); err != nil {
			return err
		}
	} else {
		left, err := aggregate(ctx, tx, sensorID, partialFrom, boundaryAt)
		if err != nil {
			return err
		}
		right, err := aggregate(ctx, tx, sensorID, boundaryAt, partialTo)
		if err != nil {
			return err
		}

		if err := insertPartial(ctx, tx, captureID, tier, bucketStart, partialFrom, boundaryAt, left); err != nil {
			return err
		}
		if err := insertPartial(ctx, tx, captureID, tier, bucketStart, boundaryAt, partialTo, right); err != nil {
			return err
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE boundary_capture SET state = 'completed', completed_at = NOW() WHERE capture_id = $1
	`, captureID); err != nil {
		return fmt.Errorf("capture: mark capture %d completed: %w", captureID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("capture: commit completion for capture %d: %w", captureID, err)
	}
	return nil
}

// findSplitTarget locates the [from, to) interval boundaryAt falls inside,
// for (sensor, tier, bucketStart): the existing boundary_partial row that
// contains it, if this bucket has already been split by an earlier
// boundary, or -- for the bucket's first boundary -- the whole bucket
// itself (never materialized as a row until now). existingPartialID is
// valid only in the former case, telling the caller which row to delete.
func (c *Completer) findSplitTarget(ctx context.Context, tx pgx.Tx, tier tiers.Tier, sensorID int64, bucketStart, boundaryAt time.Time, width string) (from, to time.Time, existingPartialID sql.NullInt64, err error) {
	var partialID int64
	err = tx.QueryRow(ctx, `
		SELECT bp.partial_id, bp.partial_from, bp.partial_to
		FROM boundary_partial bp
		JOIN boundary_capture bc ON bc.capture_id = bp.capture_id
		WHERE bc.sensor_id = $1 AND bp.tier = $2 AND bp.bucket_start = $3
		  AND bp.partial_from <= $4 AND bp.partial_to > $4
	`, sensorID, string(tier), bucketStart, boundaryAt).Scan(&partialID, &from, &to)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		// First boundary in this bucket -- the implicit "existing partial"
		// is the whole bucket.
		end, endErr := bucketEndInterval(ctx, tx, width, bucketStart)
		if endErr != nil {
			return time.Time{}, time.Time{}, sql.NullInt64{}, endErr
		}
		return bucketStart, end, sql.NullInt64{}, nil
	case err != nil:
		return time.Time{}, time.Time{}, sql.NullInt64{}, fmt.Errorf("capture: find containing partial: %w", err)
	default:
		return from, to, sql.NullInt64{Int64: partialID, Valid: true}, nil
	}
}

// bucketEndInterval computes bucketStart + width using Postgres interval
// arithmetic (SELECT $1::timestamptz + $2::interval), so it agrees exactly
// with the width the bucket was opened with, regardless of DST or other
// interval-arithmetic edge cases Go's time.Duration addition does not model
// the same way Postgres does.
func bucketEndInterval(ctx context.Context, q querier, width string, bucketStart time.Time) (time.Time, error) {
	var end time.Time
	if err := q.QueryRow(ctx, `SELECT $1::timestamptz + $2::interval`, bucketStart, width).Scan(&end); err != nil {
		return time.Time{}, fmt.Errorf("capture: compute bucket end for %s + %s: %w", bucketStart, width, err)
	}
	return end, nil
}

// insertPartial writes one boundary_partial row, attributed to captureID --
// the boundary event that produced this specific split (this task's
// Implementation-phase choice: a re-split partial's two resulting rows are
// always attributed to the capture that performed the split, never to the
// earlier capture that produced the row being replaced).
func insertPartial(ctx context.Context, tx pgx.Tx, captureID int64, tier tiers.Tier, bucketStart, from, to time.Time, agg aggregateResult) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO boundary_partial
			(capture_id, tier, bucket_start, partial_from, partial_to, reading_count, value_sum, value_min, value_max)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`, captureID, string(tier), bucketStart, from, to, agg.Count, agg.Sum, agg.Min, agg.Max)
	if err != nil {
		return fmt.Errorf("capture: insert boundary_partial [%s, %s) for capture %d: %w", from, to, captureID, err)
	}
	return nil
}

// checkPendingNearRetention implements NFR5's ordering assertion: a
// boundary_capture row still 'pending' once its boundary_at is old enough
// that raw retention (tiers.RawRetention) could drop the chunk containing it
// within one more completion window (tiers.CaptureCompletionWindow) means
// the completer has fallen dangerously behind -- that capture is at risk of
// losing the raw data its own completion depends on. This must fail loudly
// (a returned, wrapped ErrPendingNearRetention), never be silently dropped.
func (c *Completer) checkPendingNearRetention(ctx context.Context, now time.Time) error {
	threshold := now.Add(-(tiers.RawRetention - tiers.CaptureCompletionWindow))

	var count int64
	if err := c.db.QueryRow(ctx, `
		SELECT count(*) FROM boundary_capture WHERE state = 'pending' AND boundary_at <= $1
	`, threshold).Scan(&count); err != nil {
		return fmt.Errorf("capture: check NFR5 pending-near-retention ordering: %w", err)
	}
	if count > 0 {
		return fmt.Errorf("%w: %d capture(s) with boundary_at <= %s (raw retention is %s, completion window %s)",
			ErrPendingNearRetention, count, threshold, tiers.RawRetention, tiers.CaptureCompletionWindow)
	}
	return nil
}

// PruneExpiredPartials implements boundary_partial's differential retention
// (migration 033's "Retention" comment, FR20.2: "retention on
// boundary_partial follows the coarsest tier the partial splits -- hourly
// partials are never dropped"). boundary_partial is not a hypertable (see
// the migration comment for why), so this cannot be expressed as a single
// add_retention_policy the way migration 022's raw/5-minute tiers are --
// it is instead a row-scoped DELETE, restricted to tier = 'five_minute'
// rows whose bucket_start is older than tiers.FiveMinuteRetention
// (mirroring sensor_reading_5m's own retention window). tier = 'hourly'
// rows are never matched by this query and so are never dropped,
// regardless of age -- hourly is the coarsest tier in V1 and the tier every
// boundary_partial inherits retention from once its originating tier's
// window has passed (FR20.3).
//
// This is independent of RunPending/completeTier: a five-minute partial can
// be pruned only once its own bucket_start has aged past
// tiers.FiveMinuteRetention, which is always long after the bucket has
// closed and been completed (tiers.CaptureCompletionWindow), so pruning
// never races a pending completion for the same bucket.
func (c *Completer) PruneExpiredPartials(ctx context.Context) error {
	return c.pruneExpiredPartialsAt(ctx, time.Now())
}

// pruneExpiredPartialsAt is PruneExpiredPartials with an explicit "now",
// letting tests simulate retention passes (e.g. a 14-month-later run)
// without waiting on the clock.
func (c *Completer) pruneExpiredPartialsAt(ctx context.Context, now time.Time) error {
	cutoff := now.Add(-tiers.FiveMinuteRetention)
	if _, err := c.db.Exec(ctx, `
		DELETE FROM boundary_partial
		WHERE tier = $1 AND bucket_start < $2
	`, string(tiers.TierFiveMinute), cutoff); err != nil {
		return fmt.Errorf("capture: prune expired five_minute boundary_partial rows: %w", err)
	}
	return nil
}
