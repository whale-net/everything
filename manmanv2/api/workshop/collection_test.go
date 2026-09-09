package workshop

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/steam"
	"github.com/whale-net/everything/manmanv2/models"
)

// TestAddCollectionToLibrary_AllChildrenSucceed covers the base case: a
// collection with N children creates N addons, all attached to the target
// library, all with CollectionID pointing at the collection's own addon, and
// a job that ends "completed".
func TestAddCollectionToLibrary_AllChildrenSucceed(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, batchJobRepo, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Map Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "222222", Title: "Map 2"},
		{WorkshopID: "333333", Title: "Map 3"},
	}
	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}
	steamClient.items["333333"] = &steam.WorkshopItemMetadata{WorkshopID: "333333", Title: "Map 3", FileSize: 4096, TimeUpdated: time.Now()}

	libraryID := int64(50)
	gameID := int64(1)

	job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, gameID, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("AddCollectionToLibrary returned unexpected error: %v", err)
	}

	if job.JobType != "collection_add" {
		t.Errorf("expected job_type 'collection_add', got %s", job.JobType)
	}
	if job.Status != "completed" {
		t.Errorf("expected job status 'completed', got %s", job.Status)
	}
	if job.TotalItems != 3 || job.SucceededItems != 3 || job.FailedItems != 0 {
		t.Errorf("expected total=3 succeeded=3 failed=0, got total=%d succeeded=%d failed=%d", job.TotalItems, job.SucceededItems, job.FailedItems)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}

	collectionAddon, err := addonRepo.Get(ctx, collectionAddonID)
	if err != nil {
		t.Fatalf("expected collection addon to exist: %v", err)
	}
	if !collectionAddon.IsCollection {
		t.Error("expected collection addon IsCollection true")
	}

	if len(libraryRepo.attachments[libraryID]) != 3 {
		t.Errorf("expected 3 library attachments, got %d", len(libraryRepo.attachments[libraryID]))
	}

	for _, item := range items {
		if item.Status != "succeeded" {
			t.Errorf("item %q: expected status 'succeeded', got %s", item.RawInput, item.Status)
		}
		if item.AddonID == nil {
			t.Fatalf("item %q: expected an addon ID", item.RawInput)
			continue
		}
		child, err := addonRepo.Get(ctx, *item.AddonID)
		if err != nil {
			t.Fatalf("expected child addon %d to exist: %v", *item.AddonID, err)
		}
		if child.CollectionID == nil || *child.CollectionID != collectionAddonID {
			t.Errorf("item %q: expected CollectionID %d, got %v", item.RawInput, collectionAddonID, child.CollectionID)
		}
	}

	// The collection's own addon row and 3 children == 4 total addons.
	if len(addonRepo.addons) != 4 {
		t.Errorf("expected 4 addons (1 collection + 3 children), got %d", len(addonRepo.addons))
	}

	if len(batchJobRepo.jobs) != 1 {
		t.Errorf("expected 1 batch job row, got %d", len(batchJobRepo.jobs))
	}
}

// TestAddCollectionToLibrary_URLVsRawIDIdentical covers giving the collection
// as a Workshop URL vs. as a raw numeric ID: both must resolve identically.
func TestAddCollectionToLibrary_URLVsRawIDIdentical(t *testing.T) {
	ctx := context.Background()
	collectionID := "999999"

	runWith := func(collectionInput string) (*manman.WorkshopBatchJob, int64, []*manman.WorkshopBatchJobItem) {
		manager, _, _, _, steamClient := createTestBatchManager()
		steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
			WorkshopID: collectionID, Title: "Map Collection", IsCollection: true, TimeUpdated: time.Now(),
		}
		steamClient.collections[collectionID] = []steam.CollectionItem{
			{WorkshopID: "111111", Title: "Map 1"},
		}
		steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}

		job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, 1, 50, collectionInput, 0)
		if err != nil {
			t.Fatalf("AddCollectionToLibrary(%q) returned unexpected error: %v", collectionInput, err)
		}
		return job, collectionAddonID, items
	}

	jobByID, addonIDByID, itemsByID := runWith(collectionID)
	jobByURL, addonIDByURL, itemsByURL := runWith("https://steamcommunity.com/sharedfiles/filedetails/?id=" + collectionID)

	if jobByID.Status != jobByURL.Status || jobByID.TotalItems != jobByURL.TotalItems || jobByID.SucceededItems != jobByURL.SucceededItems {
		t.Errorf("expected identical job outcome for raw ID vs URL, got %+v vs %+v", jobByID, jobByURL)
	}
	if addonIDByID != addonIDByURL {
		// Each run uses its own fresh manager/addonRepo, so the collection
		// addon ID being equal (both first insert -> ID 1) confirms
		// identical resolution behavior, not shared state.
		t.Errorf("expected identical collection addon resolution behavior, got %d vs %d", addonIDByID, addonIDByURL)
	}
	if len(itemsByID) != len(itemsByURL) {
		t.Errorf("expected identical item count, got %d vs %d", len(itemsByID), len(itemsByURL))
	}
}

// TestAddCollectionToLibrary_ChildNotFoundIsPerItemFailure covers FR3: a
// child that Steam cannot resolve is that child's failure only; the rest of
// the collection still commits and the job ends "completed_with_errors".
func TestAddCollectionToLibrary_ChildNotFoundIsPerItemFailure(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Map Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "404404", Title: "Missing Map"},
	}
	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	// "404404" is deliberately absent from steamClient.items.

	job, _, items, err := manager.AddCollectionToLibrary(ctx, 1, 50, collectionID, 0)
	if err != nil {
		t.Fatalf("AddCollectionToLibrary returned unexpected job-level error: %v", err)
	}

	if job.Status != "completed_with_errors" {
		t.Errorf("expected job status 'completed_with_errors', got %s", job.Status)
	}
	if job.SucceededItems != 1 || job.FailedItems != 1 {
		t.Errorf("expected succeeded=1 failed=1, got succeeded=%d failed=%d", job.SucceededItems, job.FailedItems)
	}

	failedItem := findItem(items, "404404")
	if failedItem == nil {
		t.Fatal("expected an item for 404404")
	}
	if failedItem.Status != "failed" {
		t.Errorf("expected 404404 status 'failed', got %s", failedItem.Status)
	}
	if failedItem.ErrorMessage == nil || !strings.Contains(*failedItem.ErrorMessage, "404404") {
		t.Errorf("expected a readable per-item error message naming the workshop item, got %v", failedItem.ErrorMessage)
	}

	okItem := findItem(items, "111111")
	if okItem == nil || okItem.Status != "succeeded" {
		t.Error("expected 111111 to still succeed")
	}

	// Collection addon (1) + the one succeeded child (1) == 2 total.
	if len(addonRepo.addons) != 2 {
		t.Errorf("expected exactly 2 addons committed (collection + 1 succeeded child), got %d", len(addonRepo.addons))
	}
}

// TestAddCollectionToLibrary_UnresolvableCollectionLeavesNoRows covers: an
// unresolvable collection ID (Steam reports it as not found / not a
// collection) is a job-level RPC error with no partial rows written -- no
// workshop_batch_jobs row and no addon rows are left behind.
func TestAddCollectionToLibrary_UnresolvableCollectionLeavesNoRows(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, batchJobRepo, _ := createTestBatchManager()

	// "000000" is absent from steamClient.items, so GetWorkshopItemDetails
	// reports it as not found when resolveCollectionAddon fetches it.
	badCollectionID := "000000"

	job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, 1, 50, badCollectionID, 0)
	if err == nil {
		t.Fatal("expected an error for an unresolvable collection ID, got nil")
	}
	if job != nil {
		t.Errorf("expected no batch job returned on collection resolution failure, got %+v", job)
	}
	if collectionAddonID != 0 {
		t.Errorf("expected no collection addon ID on failure, got %d", collectionAddonID)
	}
	if items != nil {
		t.Errorf("expected no items on failure, got %v", items)
	}
	if len(addonRepo.addons) != 0 {
		t.Errorf("expected no addon rows left behind, got %d", len(addonRepo.addons))
	}
	if len(batchJobRepo.jobs) != 0 {
		t.Errorf("expected no batch job rows left behind, got %d", len(batchJobRepo.jobs))
	}
}

// TestAddCollectionToLibrary_NotACollectionIsRejected covers the other
// unresolvable-collection case: collection_input resolves to a real Workshop
// item, but that item is not a collection. This is anchored on the
// GetCollectionDetails-error gate (its Steam result-code check), not on
// metadata.IsCollection -- resolveCollectionAddon no longer gates on
// IsCollection at all, and GetCollectionDetails already runs (and fails)
// before resolveCollectionAddon is ever reached. The mock's GetCollectionDetails
// distinguishes "not a collection" (a real item exists for this ID) from
// "collection not found" (no such ID at all), so this asserts the specific
// failure mode rather than just "some error occurred".
func TestAddCollectionToLibrary_NotACollectionIsRejected(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, batchJobRepo, steamClient := createTestBatchManager()

	notACollectionID := "555555"
	// IsCollection is irrelevant to this rejection now (real Steam never
	// populates it), but is set false here to mirror what the real API
	// actually returns for an individual item.
	steamClient.items[notACollectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: notACollectionID, Title: "Just a Map", IsCollection: false, TimeUpdated: time.Now(),
	}
	// Deliberately absent from steamClient.collections: the mock's
	// GetCollectionDetails reports this as "not a collection" (as opposed to
	// "collection not found") precisely because it's a known item, mirroring
	// Steam's real non-1 result code for an individual Workshop item.

	job, _, _, err := manager.AddCollectionToLibrary(ctx, 1, 50, notACollectionID, 0)
	if err == nil {
		t.Fatal("expected an error when collection_input is not a collection, got nil")
	}
	if !strings.Contains(err.Error(), "is not a collection") {
		t.Errorf("expected the GetCollectionDetails 'is not a collection' failure mode, got: %v", err)
	}
	if job != nil {
		t.Errorf("expected no batch job returned, got %+v", job)
	}
	if len(addonRepo.addons) != 0 {
		t.Errorf("expected no addon rows left behind, got %d", len(addonRepo.addons))
	}
	if len(batchJobRepo.jobs) != 0 {
		t.Errorf("expected no batch job rows left behind, got %d", len(batchJobRepo.jobs))
	}
}

// TestAddCollectionToLibrary_SucceedsDespiteUnpopulatedIsCollection is the
// regression test for #2224: Steam's real GetPublishedFileDetails response
// never populates file_type, so metadata.IsCollection is always false for
// every real collection. This simulates exactly that -- IsCollection: false
// on the collection's own metadata -- while GetCollectionDetails (Steam's
// real collection-membership signal) reports a genuine non-empty child
// list, and asserts AddCollectionToLibrary still succeeds end-to-end: job
// completed, collection addon created, children attached. Before the fix,
// resolveCollectionAddon's `!metadata.IsCollection` gate would have
// rejected this with "is not a collection" even though Steam's own
// GetCollectionDetails call had just confirmed it is one.
func TestAddCollectionToLibrary_SucceedsDespiteUnpopulatedIsCollection(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, _, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		// This is the exact defect #2224 found: real Steam responses never
		// set file_type, so IsCollection is always false here.
		WorkshopID: collectionID, Title: "Map Collection", IsCollection: false, TimeUpdated: time.Now(),
	}
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "222222", Title: "Map 2"},
	}
	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}

	libraryID := int64(50)
	job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, 1, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("AddCollectionToLibrary returned unexpected error despite a real GetCollectionDetails child list: %v", err)
	}
	if job.Status != "completed" {
		t.Errorf("expected job status 'completed', got %s", job.Status)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}

	collectionAddon, err := addonRepo.Get(ctx, collectionAddonID)
	if err != nil {
		t.Fatalf("expected collection addon to exist: %v", err)
	}
	// The persisted collection addon row is still marked IsCollection: true
	// (resolveCollectionAddon hardcodes this on the row it creates) even
	// though Steam's metadata for it reported IsCollection: false.
	if !collectionAddon.IsCollection {
		t.Error("expected collection addon IsCollection true")
	}
	if len(libraryRepo.attachments[libraryID]) != 2 {
		t.Errorf("expected 2 library attachments, got %d", len(libraryRepo.attachments[libraryID]))
	}
}

// TestAddCollectionToLibrary_ChildrenFetchErrorLeavesNoRows is a regression
// test for the ordering gap flagged in validation: the collection's own
// metadata resolves successfully (so resolveCollectionAddon would create or
// reuse a workshop_addons row for the collection), but the subsequent
// GetCollectionDetails call to fetch its children fails with a genuine
// Steam-side error. This must still be a job-level RPC error with zero
// addon rows and zero batch job rows left behind -- unlike
// TestAddCollectionToLibrary_UnresolvableCollectionLeavesNoRows and
// TestAddCollectionToLibrary_NotACollectionIsRejected, both of which fail
// inside resolveCollectionAddon itself (before any Create call), this
// exercises a Steam error that happens (or, prior to the fix, would have
// been observed) only after the collection's own addon row was already
// persisted.
func TestAddCollectionToLibrary_ChildrenFetchErrorLeavesNoRows(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, batchJobRepo, steamClient := createTestBatchManager()

	collectionID := "777777"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Broken Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	// Deliberately do not populate steamClient.collections[collectionID], so
	// GetCollectionDetails fails with a Steam-side error even though the
	// collection's own metadata (fetched by resolveCollectionAddon) resolves
	// fine.

	job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, 1, 50, collectionID, 0)
	if err == nil {
		t.Fatal("expected an error when fetching collection children fails, got nil")
	}
	if job != nil {
		t.Errorf("expected no batch job returned on children-fetch failure, got %+v", job)
	}
	if collectionAddonID != 0 {
		t.Errorf("expected no collection addon ID on failure, got %d", collectionAddonID)
	}
	if items != nil {
		t.Errorf("expected no items on failure, got %v", items)
	}
	if len(addonRepo.addons) != 0 {
		t.Errorf("expected no addon rows left behind (collection addon must not be orphaned), got %d", len(addonRepo.addons))
	}
	if len(batchJobRepo.jobs) != 0 {
		t.Errorf("expected no batch job rows left behind, got %d", len(batchJobRepo.jobs))
	}
}

// TestAddCollectionToLibrary_ReRunReusesExistingRows covers re-adding the
// same collection: no duplicate addon or junction rows are created, but a
// second batch job row is still recorded for the new run.
func TestAddCollectionToLibrary_ReRunReusesExistingRows(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, batchJobRepo, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Map Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "222222", Title: "Map 2"},
	}
	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}

	libraryID := int64(50)

	job1, collectionAddonID1, items1, err := manager.AddCollectionToLibrary(ctx, 1, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("first AddCollectionToLibrary call failed: %v", err)
	}
	if job1.Status != "completed" || job1.SucceededItems != 2 {
		t.Fatalf("expected first run to fully succeed, got status=%s succeeded=%d", job1.Status, job1.SucceededItems)
	}
	if len(addonRepo.addons) != 3 {
		t.Fatalf("expected 3 addons after first run (1 collection + 2 children), got %d", len(addonRepo.addons))
	}

	job2, collectionAddonID2, items2, err := manager.AddCollectionToLibrary(ctx, 1, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("second AddCollectionToLibrary call failed: %v", err)
	}
	if job2.Status != "completed" || job2.SucceededItems != 2 {
		t.Fatalf("expected second run to fully succeed, got status=%s succeeded=%d", job2.Status, job2.SucceededItems)
	}
	if collectionAddonID2 != collectionAddonID1 {
		t.Errorf("expected the collection addon to be reused, got %d vs %d", collectionAddonID1, collectionAddonID2)
	}
	if len(addonRepo.addons) != 3 {
		t.Errorf("expected no new addon rows after re-running the same collection, got %d total", len(addonRepo.addons))
	}

	// A fresh batch job row is still recorded for the new run.
	if len(batchJobRepo.jobs) != 2 {
		t.Errorf("expected 2 batch job rows (one per run), got %d", len(batchJobRepo.jobs))
	}
	if job2.BatchJobID == job1.BatchJobID {
		t.Error("expected the second run to record a distinct batch job ID")
	}

	// Both children get (re-)attached to the library on the second run.
	if len(libraryRepo.attachments[libraryID]) != 4 {
		t.Errorf("expected 4 total library attachments across both runs, got %d", len(libraryRepo.attachments[libraryID]))
	}

	if len(items1) != 2 || len(items2) != 2 {
		t.Fatalf("expected 2 items per run, got %d and %d", len(items1), len(items2))
	}
	firstAddonID := *findItem(items1, "111111").AddonID
	secondAddonID := *findItem(items2, "111111").AddonID
	if firstAddonID != secondAddonID {
		t.Errorf("expected re-run to reuse child addon %d, got a different addon %d", firstAddonID, secondAddonID)
	}
}

// TestAddCollectionToLibrary_MembershipChangeOnlyAddsNewChildren covers FR1:
// a collection whose Steam membership changed between two calls only adds
// the new children on the second call, and nothing removes children that
// left the Steam collection in the interim (this is a one-time bulk-add,
// never a live sync).
func TestAddCollectionToLibrary_MembershipChangeOnlyAddsNewChildren(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Map Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}
	steamClient.items["333333"] = &steam.WorkshopItemMetadata{WorkshopID: "333333", Title: "Map 3", FileSize: 4096, TimeUpdated: time.Now()}

	// First call: collection has items 111111 and 222222.
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "222222", Title: "Map 2"},
	}

	libraryID := int64(50)
	_, collectionAddonID1, items1, err := manager.AddCollectionToLibrary(ctx, 1, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("first AddCollectionToLibrary call failed: %v", err)
	}
	if len(items1) != 2 {
		t.Fatalf("expected 2 items on first call, got %d", len(items1))
	}

	// Membership changes: 222222 leaves the collection, 333333 joins.
	steamClient.collections[collectionID] = []steam.CollectionItem{
		{WorkshopID: "111111", Title: "Map 1"},
		{WorkshopID: "333333", Title: "Map 3"},
	}

	job2, collectionAddonID2, items2, err := manager.AddCollectionToLibrary(ctx, 1, libraryID, collectionID, 0)
	if err != nil {
		t.Fatalf("second AddCollectionToLibrary call failed: %v", err)
	}
	if collectionAddonID2 != collectionAddonID1 {
		t.Errorf("expected the same collection addon reused, got %d vs %d", collectionAddonID1, collectionAddonID2)
	}
	if job2.Status != "completed" || job2.SucceededItems != 2 {
		t.Fatalf("expected second run to fully succeed on the new membership snapshot, got status=%s succeeded=%d", job2.Status, job2.SucceededItems)
	}
	// Second call's items reflect the *current* snapshot (111111, 333333) --
	// it processes exactly what Steam reports now, including re-attaching
	// 111111 (already present) and newly adding 333333.
	if findItem(items2, "111111") == nil || findItem(items2, "333333") == nil {
		t.Errorf("expected the second run's items to reflect the current snapshot (111111, 333333), got %v", items2)
	}

	// 222222, added by the first call, must still exist as an addon: nothing
	// removes children that left the Steam collection in the interim.
	addon222, err := addonRepo.GetByWorkshopID(ctx, 1, "222222", manman.PlatformTypeSteamWorkshop)
	if err != nil || addon222 == nil {
		t.Errorf("expected addon for 222222 (added by first call) to still exist after it left the Steam collection, got err=%v", err)
	}

	// The collection's own addon + 111111, 222222, 333333 == 4 total.
	if len(addonRepo.addons) != 4 {
		t.Errorf("expected 4 addons total (collection + 3 distinct children ever seen), got %d", len(addonRepo.addons))
	}
}

// TestAddCollectionToLibrary_EmptyCollection covers an empty collection: a
// job with 0 items is recorded and no error is returned.
func TestAddCollectionToLibrary_EmptyCollection(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, steamClient := createTestBatchManager()

	collectionID := "999999"
	steamClient.items[collectionID] = &steam.WorkshopItemMetadata{
		WorkshopID: collectionID, Title: "Empty Collection", IsCollection: true, TimeUpdated: time.Now(),
	}
	steamClient.collections[collectionID] = []steam.CollectionItem{}

	job, collectionAddonID, items, err := manager.AddCollectionToLibrary(ctx, 1, 50, collectionID, 0)
	if err != nil {
		t.Fatalf("AddCollectionToLibrary returned unexpected error for an empty collection: %v", err)
	}
	if job.TotalItems != 0 || job.SucceededItems != 0 || job.FailedItems != 0 {
		t.Errorf("expected total=0 succeeded=0 failed=0, got total=%d succeeded=%d failed=%d", job.TotalItems, job.SucceededItems, job.FailedItems)
	}
	if job.Status != "completed" {
		t.Errorf("expected job status 'completed' for an empty collection, got %s", job.Status)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
	if collectionAddonID == 0 {
		t.Error("expected a collection addon ID to still be recorded for an empty collection")
	}
	// The collection's own addon row is still created even with no children.
	if len(addonRepo.addons) != 1 {
		t.Errorf("expected exactly 1 addon (the collection itself), got %d", len(addonRepo.addons))
	}
}
