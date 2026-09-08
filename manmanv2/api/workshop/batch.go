package workshop

import (
	"context"
	"fmt"
	"log"

	"github.com/whale-net/everything/manmanv2/models"
)

// batchItemFunc processes a single batch job item and returns the addon ID
// it resulted in. Since the repository interfaces available to
// WorkshopManager have no cross-repository transaction primitive, an
// implementation that performs more than one write (e.g. create an addon,
// then attach it to a library) is responsible for compensating -- undoing
// its own prior writes -- if a later write in the same item fails, so a
// failure can never leave a half-created addon row, a dangling
// workshop_library_addons junction row, or an orphan cache-entry row that a
// later retry would mistake for an already-succeeded item (NFR5).
// runBatchJob itself only sequences items and records outcomes; it does not
// manage atomicity for fn.
type batchItemFunc func(ctx context.Context, item *manman.WorkshopBatchJobItem) (addonID int64, err error)

// runBatchJob is the per-item batch runner shared by batch-create (FR2/FR3,
// this issue) and the collection bulk-add (FR1, reused from here). It
// executes fn once per item, records each item's outcome via
// UpdateBatchJobItemResult once that item's work has settled, and never
// aborts the loop on an individual item error -- it always processes the
// entire list and returns aggregate succeeded/failed counts.
//
// A panic inside fn is recovered and recorded as that item's failure rather
// than crashing the job. Once every item has settled, the job's terminal
// status is set via UpdateBatchJobStatus: "completed" (0 failures),
// "completed_with_errors" (mixed), "failed" (all items failed, including the
// case of zero items).
func (wm *WorkshopManager) runBatchJob(ctx context.Context, jobID int64, items []*manman.WorkshopBatchJobItem, fn batchItemFunc) (succeeded, failed int, err error) {
	for _, item := range items {
		addonID, itemErr := wm.runBatchItem(ctx, item, fn)
		if itemErr == nil {
			succeeded++
			item.Status = "succeeded"
			item.AddonID = &addonID
			item.ErrorMessage = nil
			if updErr := wm.batchJobRepo.UpdateBatchJobItemResult(ctx, item.BatchJobItemID, "succeeded", &addonID, nil); updErr != nil {
				return succeeded, failed, fmt.Errorf("failed to record success for batch job item %d: %w", item.BatchJobItemID, updErr)
			}
			continue
		}

		failed++
		msg := itemErr.Error()
		item.Status = "failed"
		item.ErrorMessage = &msg
		if updErr := wm.batchJobRepo.UpdateBatchJobItemResult(ctx, item.BatchJobItemID, "failed", nil, &msg); updErr != nil {
			return succeeded, failed, fmt.Errorf("failed to record failure for batch job item %d: %w", item.BatchJobItemID, updErr)
		}
	}

	status := "completed"
	switch {
	case failed > 0 && succeeded == 0:
		status = "failed"
	case failed > 0:
		status = "completed_with_errors"
	}

	if updErr := wm.batchJobRepo.UpdateBatchJobStatus(ctx, jobID, status, succeeded, failed); updErr != nil {
		return succeeded, failed, fmt.Errorf("failed to update batch job %d status: %w", jobID, updErr)
	}

	return succeeded, failed, nil
}

// runBatchItem invokes fn for a single item, recovering any panic and
// reporting it as that item's failure instead of letting it crash the job.
func (wm *WorkshopManager) runBatchItem(ctx context.Context, item *manman.WorkshopBatchJobItem, fn batchItemFunc) (addonID int64, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("panic processing batch job item %d: %v", item.BatchJobItemID, r)
		}
	}()
	return fn(ctx, item)
}

// BatchCreateAddons takes a pasted block of mixed raw Workshop IDs and
// Workshop URLs and creates an addon per valid entry, committing valid
// entries even when some entries are invalid (FR2, FR3). Reuses the
// library-scoped workshop_library_addons attachment path (NFR2) via
// runBatchJob (NFR5).
func (wm *WorkshopManager) BatchCreateAddons(ctx context.Context, gameID, libraryID int64, entries string, presetID int64) (*manman.WorkshopBatchJob, []*manman.WorkshopBatchJobItem, error) {
	parsed := ParseWorkshopEntries(entries)

	var libraryIDPtr *int64
	if libraryID != 0 {
		libraryIDPtr = &libraryID
	}
	sourceInput := entries

	job := &manman.WorkshopBatchJob{
		JobType:     "batch_create",
		GameID:      gameID,
		LibraryID:   libraryIDPtr,
		SourceInput: &sourceInput,
		Status:      "pending",
		TotalItems:  len(parsed),
	}

	createdJob, err := wm.batchJobRepo.CreateBatchJob(ctx, job)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create batch job: %w", err)
	}

	items := make([]*manman.WorkshopBatchJobItem, 0, len(parsed))
	for i, entry := range parsed {
		item := &manman.WorkshopBatchJobItem{
			RawInput:     entry.RawInput,
			Status:       "pending",
			DisplayOrder: i,
		}
		if entry.Err != nil {
			// Lines that failed to parse are marked failed immediately with
			// the parser's reason -- they are never sent to Steam (no
			// WorkshopID is ever set on them).
			msg := entry.Err.Error()
			item.Status = "failed"
			item.ErrorMessage = &msg
		} else {
			workshopID := entry.WorkshopID
			item.WorkshopID = &workshopID
		}
		items = append(items, item)
	}

	if len(items) > 0 {
		if err := wm.batchJobRepo.CreateBatchJobItems(ctx, createdJob.BatchJobID, items); err != nil {
			return nil, nil, fmt.Errorf("failed to create batch job items: %w", err)
		}
	}

	fn := wm.makeBatchCreateAddonFunc(gameID, libraryID, presetID)

	succeeded, failed, err := wm.runBatchJob(ctx, createdJob.BatchJobID, items, fn)
	if err != nil {
		return createdJob, items, err
	}

	createdJob.TotalItems = len(items)
	createdJob.SucceededItems = succeeded
	createdJob.FailedItems = failed
	switch {
	case failed == 0:
		createdJob.Status = "completed"
	case succeeded == 0 && failed > 0:
		createdJob.Status = "failed"
	default:
		createdJob.Status = "completed_with_errors"
	}

	return createdJob, items, nil
}

// makeBatchCreateAddonFunc builds the per-item batchItemFunc for
// BatchCreateAddons: fetch metadata, create (or reuse an existing
// (game_id, workshop_id) addon), and attach it to libraryID via the
// library-scoped junction when libraryID != 0.
func (wm *WorkshopManager) makeBatchCreateAddonFunc(gameID, libraryID, presetID int64) batchItemFunc {
	return func(ctx context.Context, item *manman.WorkshopBatchJobItem) (int64, error) {
		if item.WorkshopID == nil {
			// This item never resolved to a Workshop ID (parse failure) --
			// it must never be sent to Steam. Surface the reason already
			// recorded for it.
			if item.ErrorMessage != nil {
				return 0, fmt.Errorf("%s", *item.ErrorMessage)
			}
			return 0, fmt.Errorf("entry could not be resolved to a Workshop ID")
		}
		workshopID := *item.WorkshopID

		// A re-paste of an overlapping list, or a duplicate line resolved
		// independently by ID and URL, must be safe: reuse the existing
		// addon rather than erroring or duplicating it.
		if existing, err := wm.addonRepo.GetByWorkshopID(ctx, gameID, workshopID, manman.PlatformTypeSteamWorkshop); err == nil && existing != nil {
			if libraryID != 0 {
				if err := wm.libraryRepo.AddAddon(ctx, libraryID, existing.AddonID, 0); err != nil {
					return 0, fmt.Errorf("failed to attach existing addon %d to library %d: %w", existing.AddonID, libraryID, err)
				}
			}
			return existing.AddonID, nil
		}

		metadata, err := wm.steamClient.GetWorkshopItemDetails(ctx, workshopID)
		if err != nil {
			return 0, fmt.Errorf("workshop item %s: %w", workshopID, err)
		}

		addon := &manman.WorkshopAddon{
			GameID:        gameID,
			WorkshopID:    workshopID,
			PlatformType:  manman.PlatformTypeSteamWorkshop,
			Name:          metadata.Title,
			Description:   &metadata.Description,
			FileSizeBytes: &metadata.FileSize,
			IsCollection:  metadata.IsCollection,
			LastUpdated:   &metadata.TimeUpdated,
			PresetID:      presetID,
		}

		created, err := wm.addonRepo.Create(ctx, addon)
		if err != nil {
			return 0, fmt.Errorf("failed to create addon for workshop item %s: %w", workshopID, err)
		}

		if libraryID != 0 {
			if err := wm.libraryRepo.AddAddon(ctx, libraryID, created.AddonID, 0); err != nil {
				// No cross-repository transaction is available here, so
				// atomicity (NFR5) is enforced by explicitly compensating:
				// delete the addon just created so this item's failure
				// never leaves a half-created addon row or a dangling
				// junction row behind.
				if delErr := wm.addonRepo.Delete(ctx, created.AddonID); delErr != nil {
					log.Printf("Warning: failed to roll back addon %d after library attach failure for workshop item %s: %v", created.AddonID, workshopID, delErr)
				}
				return 0, fmt.Errorf("failed to attach addon %d to library %d: %w", created.AddonID, libraryID, err)
			}
		}

		return created.AddonID, nil
	}
}
