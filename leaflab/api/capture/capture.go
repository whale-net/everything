// Package capture implements FR20's two-phase boundary capture: a bucket a
// plant moved through must resolve exactly for the life of the tier that
// holds it, including after raw rows have aged out (FR20.2), permanently
// and with no horizon (A14).
//
// Phase one, Recorder, runs at the instant a placement boundary is
// recorded -- called from leaflab/api/placement's writer (FR19), and later
// from Phase 5's FR51/FR74 -- and inserts one boundary_capture row
// (migration 033) per affected sensor and tier, in the same transaction as
// the placement write.
//
// Phase two, Completer, runs at bucket close: for each pending capture
// whose bucket has closed, it computes that bucket's partials from raw,
// both sides, independently -- never by subtraction from the full bucket
// (A17: min and max are not invertible, and the code has one shape rather
// than a special case for sum/count). Results are durably written as
// boundary_partial rows (migration 033) before the capture is marked
// completed.
//
// Both tables are keyed by sensor_id and instant, never by plant_id
// (FR20.2: "the capture is keyed by sensor and instant, never by plant").
// Attribution -- which plant, if any, a sensor's reading is attributed to
// -- is resolved above the aggregate at read time (the read-path task that
// follows this one), never baked into the capture itself.
//
// Completer is intended to run inside leaflab/processor rather than as a
// separate scheduled job (per this task's Scaffold instructions: "state
// which and why"). leaflab/processor already holds the long-lived pgxpool
// this package needs and is deployed as a single-replica worker
// (leaflab/processor/BUILD.bazel's release_app app_type = "worker"), so
// running the completer on a short, predictable ticker inside a process
// that is already always up keeps NFR5's ordering constraint -- every
// capture must complete before raw retention elapses for the chunk
// containing its boundary -- easy to reason about against migration 022's
// refresh/retention ordering, without introducing a second scheduling
// interval (a cron-style Job's own schedule) on top of it. leaflab/processor
// wires RunPending onto exactly that ticker (see leaflab/processor/capture.go).
//
// This task's Implementation phase (#1360) fills in the actual
// bucket-boundary arithmetic, the raw both-side scan, the N-boundary
// partial split (FR20.3) and the finer-tier composition (FR20.3's "a
// coarser tier's partials are composed from the finer tier's rather than
// from a second raw scan"). Wiring Recorder.Record into a caller (the
// placement writer, FR19) is deliberately left out of this task's scope --
// see recorder.go's doc comment on why affected-sensor computation is the
// caller's responsibility, and this task's own scope note on the writer not
// yet calling in.
package capture

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrPendingNearRetention is returned (wrapped) by Completer.RunPending when
// NFR5's ordering check finds a boundary_capture row still 'pending' as its
// raw chunk approaches raw retention -- the completer has fallen far enough
// behind that the capture is at risk of losing the raw data it depends on.
// This must surface loudly (a returned error the caller logs/alerts on),
// never be silently swallowed.
var ErrPendingNearRetention = errors.New("capture: boundary_capture row(s) still pending near raw retention (NFR5)")

// querier is the minimal SQL surface capture's helpers need -- satisfied by
// both pgx.Tx (Recorder's caller-supplied transaction, and each of
// Completer's own per-bucket transactions) and *pgxpool.Pool (Completer's
// top-level listing queries, which need no transactional isolation of their
// own). Keeping this as an interface lets every helper below run unchanged
// whether it is passed a transaction or the pool directly.
type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}
