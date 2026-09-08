package workshop

import (
	"context"
	"fmt"
	"log"

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
// An unresolvable collectionInput, a Steam error fetching the collection
// itself, or a collectionInput that does not point at a collection is a
// job-level error and returns before any workshop_batch_jobs or
// workshop_addons row is written. A child that Steam cannot resolve is only
// that child's failure -- runBatchJob still commits the rest of the
// collection (FR3).
func (wm *WorkshopManager) AddCollectionToLibrary(ctx context.Context, gameID, libraryID int64, collectionInput string, presetID int64) (*manman.WorkshopBatchJob, int64, []*manman.WorkshopBatchJobItem, error) {
	collectionWorkshopID, err := ParseWorkshopEntry(collectionInput)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("could not resolve collection_input %q: %w", collectionInput, err)
	}

	// Fetch the collection's children before persisting anything. A genuine
	// Steam-side error here must be a job-level error with no partial rows
	// written, so this must happen before resolveCollectionAddon's
	// addonRepo.Create -- otherwise a Steam error on this call would leave
	// an orphaned collection addon row with no corresponding
	// workshop_batch_jobs row behind it.
	children, err := wm.steamClient.GetCollectionDetails(ctx, collectionWorkshopID)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to fetch collection %s details: %w", collectionWorkshopID, err)
	}

	collectionAddon, err := wm.resolveCollectionAddon(ctx, gameID, collectionWorkshopID, presetID)
	if err != nil {
		return nil, 0, nil, err
	}

	var libraryIDPtr *int64
	if libraryID != 0 {
		libraryIDPtr = &libraryID
	}
	sourceInput := collectionInput

	job := &manman.WorkshopBatchJob{
		JobType:     "collection_add",
		GameID:      gameID,
		LibraryID:   libraryIDPtr,
		SourceInput: &sourceInput,
		Status:      "pending",
		TotalItems:  len(children),
	}

	createdJob, err := wm.batchJobRepo.CreateBatchJob(ctx, job)
	if err != nil {
		return nil, 0, nil, fmt.Errorf("failed to create batch job: %w", err)
	}

	items := make([]*manman.WorkshopBatchJobItem, 0, len(children))
	for i, child := range children {
		workshopID := child.WorkshopID
		items = append(items, &manman.WorkshopBatchJobItem{
			RawInput:     child.WorkshopID,
			WorkshopID:   &workshopID,
			Status:       "pending",
			DisplayOrder: i,
		})
	}

	if len(items) > 0 {
		if err := wm.batchJobRepo.CreateBatchJobItems(ctx, createdJob.BatchJobID, items); err != nil {
			return nil, 0, nil, fmt.Errorf("failed to create batch job items: %w", err)
		}
	}

	fn := wm.makeCollectionChildFunc(gameID, libraryID, presetID, collectionAddon.AddonID)

	succeeded, failed, err := wm.runBatchJob(ctx, createdJob.BatchJobID, items, fn)
	if err != nil {
		return createdJob, collectionAddon.AddonID, items, err
	}

	createdJob.TotalItems = len(items)
	createdJob.SucceededItems = succeeded
	createdJob.FailedItems = failed
	switch {
	case len(items) == 0:
		createdJob.Status = "completed"
	case failed == 0:
		createdJob.Status = "completed"
	case succeeded == 0:
		createdJob.Status = "failed"
	default:
		createdJob.Status = "completed_with_errors"
	}

	return createdJob, collectionAddon.AddonID, items, nil
}

// resolveCollectionAddon fetches the collection's own Steam metadata,
// rejects a collectionInput that does not point at an actual Steam Workshop
// collection, and reuses an existing (game_id, workshop_id) addon row for
// the collection rather than creating a duplicate on a re-run.
func (wm *WorkshopManager) resolveCollectionAddon(ctx context.Context, gameID int64, collectionWorkshopID string, presetID int64) (*manman.WorkshopAddon, error) {
	if existing, err := wm.addonRepo.GetByWorkshopID(ctx, gameID, collectionWorkshopID, manman.PlatformTypeSteamWorkshop); err == nil && existing != nil {
		return existing, nil
	}

	metadata, err := wm.steamClient.GetWorkshopItemDetails(ctx, collectionWorkshopID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch collection %s metadata: %w", collectionWorkshopID, err)
	}
	if !metadata.IsCollection {
		return nil, fmt.Errorf("workshop item %s is not a collection", collectionWorkshopID)
	}

	addon := &manman.WorkshopAddon{
		GameID:        gameID,
		WorkshopID:    collectionWorkshopID,
		PlatformType:  manman.PlatformTypeSteamWorkshop,
		Name:          metadata.Title,
		Description:   &metadata.Description,
		FileSizeBytes: &metadata.FileSize,
		IsCollection:  true,
		LastUpdated:   &metadata.TimeUpdated,
		PresetID:      presetID,
	}

	created, err := wm.addonRepo.Create(ctx, addon)
	if err != nil {
		return nil, fmt.Errorf("failed to create collection addon for %s: %w", collectionWorkshopID, err)
	}
	return created, nil
}

// makeCollectionChildFunc builds the per-item batchItemFunc for
// AddCollectionToLibrary: fetch metadata, create (or reuse an existing
// (game_id, workshop_id) addon) linked back to the collection via
// CollectionID, and attach it to libraryID via the library-scoped junction
// when libraryID != 0. Mirrors makeBatchCreateAddonFunc's idempotent-reuse
// and compensating-delete behavior (NFR5).
func (wm *WorkshopManager) makeCollectionChildFunc(gameID, libraryID, presetID, collectionAddonID int64) batchItemFunc {
	return func(ctx context.Context, item *manman.WorkshopBatchJobItem) (int64, error) {
		if item.WorkshopID == nil {
			if item.ErrorMessage != nil {
				return 0, fmt.Errorf("%s", *item.ErrorMessage)
			}
			return 0, fmt.Errorf("collection child could not be resolved to a Workshop ID")
		}
		workshopID := *item.WorkshopID

		// A child already added by a prior run of this same collection (or
		// independently) must be reused rather than duplicated.
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

		collectionID := collectionAddonID
		addon := &manman.WorkshopAddon{
			GameID:        gameID,
			WorkshopID:    workshopID,
			PlatformType:  manman.PlatformTypeSteamWorkshop,
			Name:          metadata.Title,
			Description:   &metadata.Description,
			FileSizeBytes: &metadata.FileSize,
			IsCollection:  metadata.IsCollection,
			CollectionID:  &collectionID,
			LastUpdated:   &metadata.TimeUpdated,
			PresetID:      presetID,
		}

		created, err := wm.addonRepo.Create(ctx, addon)
		if err != nil {
			return 0, fmt.Errorf("failed to create addon for collection child %s: %w", workshopID, err)
		}

		if libraryID != 0 {
			if err := wm.libraryRepo.AddAddon(ctx, libraryID, created.AddonID, 0); err != nil {
				// No cross-repository transaction is available here, so
				// atomicity (NFR5) is enforced by explicitly compensating:
				// delete the addon just created so this item's failure
				// never leaves a half-created addon row or a dangling
				// junction row behind.
				if delErr := wm.addonRepo.Delete(ctx, created.AddonID); delErr != nil {
					log.Printf("Warning: failed to roll back addon %d after library attach failure for collection child %s: %v", created.AddonID, workshopID, delErr)
				}
				return 0, fmt.Errorf("failed to attach addon %d to library %d: %w", created.AddonID, libraryID, err)
			}
		}

		return created.AddonID, nil
	}
}
