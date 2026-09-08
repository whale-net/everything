package workshop

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/manmanv2/models"
)

// batchItemFunc processes a single batch job item and returns the addon ID
// it resulted in. Implementations are expected to run inside their own
// per-item database transaction so a failure rolls back in full, never
// leaving a half-created addon row, a dangling workshop_library_addons
// junction row, or an orphan cache-entry row (NFR5) -- runBatchJob itself
// only sequences items and records outcomes, it does not manage the
// transaction.
type batchItemFunc func(ctx context.Context, item *manman.WorkshopBatchJobItem) (addonID int64, err error)

// runBatchJob is the per-item batch runner shared by batch-create (FR2/FR3,
// this issue) and the collection bulk-add (FR1, reused from here). It
// executes fn once per item, records each item's outcome via
// UpdateBatchJobItemResult once that item's work has settled, and never
// aborts the loop on an individual item error -- it always processes the
// entire list and returns aggregate succeeded/failed counts.
//
// TODO(#2177 Implementation):
//   - recover a panic inside fn and record it as that item's failure rather
//     than letting it crash the job
//   - set the job's terminal status via UpdateBatchJobStatus once every item
//     has settled: "completed" (0 failures), "completed_with_errors"
//     (mixed), "failed" (all items failed)
func (wm *WorkshopManager) runBatchJob(ctx context.Context, jobID int64, items []*manman.WorkshopBatchJobItem, fn batchItemFunc) (succeeded, failed int, err error) {
	return 0, 0, fmt.Errorf("runBatchJob: not yet implemented")
}

// BatchCreateAddons takes a pasted block of mixed raw Workshop IDs and
// Workshop URLs and creates an addon per valid entry, committing valid
// entries even when some entries are invalid (FR2, FR3). Reuses the
// library-scoped workshop_library_addons attachment path (NFR2) via
// runBatchJob (NFR5).
//
// TODO(#2177 Implementation):
//  1. ParseWorkshopEntries the raw block; persist a workshop_batch_jobs row
//     (job_type = "batch_create") and one workshop_batch_job_items row per
//     non-blank line, preserving paste order in display_order.
//  2. Mark lines that failed to parse as "failed" immediately with the
//     parser's reason -- they are never sent to Steam.
//  3. Run each parsed item through runBatchJob: fetch metadata, create (or
//     reuse an existing (game_id, workshop_id) addon), and attach it to
//     libraryID via the library-scoped junction when libraryID != 0.
//  4. Return once every item has been processed; reserve a non-nil error
//     for job-level failures (bad game_id, DB unavailable), never for
//     individual bad entries.
func (wm *WorkshopManager) BatchCreateAddons(ctx context.Context, gameID, libraryID int64, entries string, presetID int64) (*manman.WorkshopBatchJob, []*manman.WorkshopBatchJobItem, error) {
	return nil, nil, fmt.Errorf("BatchCreateAddons: not yet implemented")
}
