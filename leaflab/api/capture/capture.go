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
// interval (a cron-style Job's own schedule) on top of it. Wiring the
// ticker into leaflab/processor/main.go, and the actual NFR5 alerting when
// a capture is still pending as its raw chunk nears retention, are this
// task's Implementation phase, not this package's scaffold.
//
// Scaffold only (this task's Scaffold phase, #1360): Recorder.Record and
// Completer.RunPending are stubs returning ErrNotImplemented. This task's
// Implementation phase fills in the actual bucket-boundary arithmetic,
// the raw both-side scan, the N-boundary partial split (FR20.3) and the
// finer-tier composition (FR20.3's "a coarser tier's partials are
// composed from the finer tier's rather than from a second raw scan").
package capture

import "errors"

// ErrNotImplemented is returned by Recorder.Record and Completer.RunPending
// until this task's Implementation phase fills them in.
var ErrNotImplemented = errors.New("capture: not implemented (Implementation phase, FR20)")
