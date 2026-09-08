package workshop

import (
	"context"
	"fmt"

	"github.com/whale-net/everything/manmanv2/models"
)

// AddCollectionToLibrary resolves a Steam Workshop collection's current
// membership and adds every item in it to a library in one action (FR1,
// FR3, FR4, NFR2, NFR5). Reuses the batch-job persistence (#2176) and the
// per-item batch runner (runBatchJob, #2177) already used by
// BatchCreateAddons.
//
// This is a one-time snapshot, not a live sync: membership is captured once
// at call time. No scheduled refresh, watcher, or synced_at-driven re-poll
// belongs here or anywhere on this path -- items added to the Steam
// collection after this call are, by design, never picked up. A later
// re-run of this same RPC for the same collection only ever adds new
// children; it must never remove children that have left the Steam
// collection in the interim.
//
// TODO(#2178 Implementation):
//  1. Resolve collectionInput via ParseWorkshopEntry (#2176) so a raw
//     numeric collection ID and a Workshop collection URL behave
//     identically.
//  2. Fetch the collection via SteamClient.GetCollectionDetails to get its
//     current children, and via SteamClient.GetWorkshopItemDetails for the
//     collection's own metadata; persist/reuse the collection's own addon
//     row (is_collection = true), reusing rather than duplicating it on a
//     re-run.
//  3. Create a workshop_batch_jobs row (job_type = "collection_add",
//     source_input = collectionInput) and one workshop_batch_job_items row
//     per collection child (raw_input = child's Workshop ID, display_order
//     = collection order).
//  4. Run the items through runBatchJob: per child, fetch metadata,
//     create/reuse the addon with collection_id set to the collection
//     addon's addon_id, and attach it to libraryID via
//     workshop_library_addons. Reuse existing addon rows and library
//     attachments on a re-run rather than duplicating them.
//  5. An unresolvable collectionInput or a Steam error resolving the
//     collection itself is a job-level error with no partial rows written.
//     A child Steam cannot resolve is only that child's failure -- the rest
//     of the collection still commits.
func (wm *WorkshopManager) AddCollectionToLibrary(ctx context.Context, gameID, libraryID int64, collectionInput string, presetID int64) (*manman.WorkshopBatchJob, int64, []*manman.WorkshopBatchJobItem, error) {
	return nil, 0, nil, fmt.Errorf("AddCollectionToLibrary: not yet implemented")
}
