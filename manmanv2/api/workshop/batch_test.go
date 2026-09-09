package workshop

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/whale-net/everything/manmanv2/api/steam"
	"github.com/whale-net/everything/manmanv2/models"
)

// mockLibraryRepo is a minimal WorkshopLibraryRepository test double covering
// only what BatchCreateAddons' per-item batch runner exercises: AddAddon.
// failForAddonID lets a test force a library-attachment failure for a
// specific addon ID, without needing a real transactional store, so the
// NFR5 rollback path can be exercised deterministically.
type mockLibraryRepo struct {
	attachments    map[int64][]int64 // libraryID -> addonIDs attached, in call order
	failForAddonID map[int64]bool
}

func (m *mockLibraryRepo) Create(ctx context.Context, library *manman.WorkshopLibrary) (*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) Get(ctx context.Context, libraryID int64) (*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) List(ctx context.Context, gameID *int64, limit, offset int) ([]*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) Update(ctx context.Context, library *manman.WorkshopLibrary) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) Delete(ctx context.Context, libraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) AddAddon(ctx context.Context, libraryID, addonID int64, displayOrder int) error {
	if m.failForAddonID != nil && m.failForAddonID[addonID] {
		return fmt.Errorf("mock library attach failure for addon %d", addonID)
	}
	if m.attachments == nil {
		m.attachments = make(map[int64][]int64)
	}
	m.attachments[libraryID] = append(m.attachments[libraryID], addonID)
	return nil
}

func (m *mockLibraryRepo) RemoveAddon(ctx context.Context, libraryID, addonID int64) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) ListAddons(ctx context.Context, libraryID int64) ([]*manman.WorkshopAddonWithGame, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) AddReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) RemoveReference(ctx context.Context, parentLibraryID, childLibraryID int64) error {
	return fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) ListReferences(ctx context.Context, libraryID int64) ([]*manman.WorkshopLibrary, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockLibraryRepo) DetectCircularReference(ctx context.Context, parentLibraryID, childLibraryID int64) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

// mockBatchJobRepo is an in-memory WorkshopBatchJobRepository test double.
type mockBatchJobRepo struct {
	jobs       map[int64]*manman.WorkshopBatchJob
	items      map[int64]*manman.WorkshopBatchJobItem
	nextJobID  int64
	nextItemID int64
}

func newMockBatchJobRepo() *mockBatchJobRepo {
	return &mockBatchJobRepo{
		jobs:  make(map[int64]*manman.WorkshopBatchJob),
		items: make(map[int64]*manman.WorkshopBatchJobItem),
	}
}

func (m *mockBatchJobRepo) CreateBatchJob(ctx context.Context, job *manman.WorkshopBatchJob) (*manman.WorkshopBatchJob, error) {
	m.nextJobID++
	job.BatchJobID = m.nextJobID
	m.jobs[job.BatchJobID] = job
	return job, nil
}

func (m *mockBatchJobRepo) CreateBatchJobItems(ctx context.Context, batchJobID int64, items []*manman.WorkshopBatchJobItem) error {
	for _, item := range items {
		m.nextItemID++
		item.BatchJobItemID = m.nextItemID
		item.BatchJobID = batchJobID
		m.items[item.BatchJobItemID] = item
	}
	return nil
}

func (m *mockBatchJobRepo) UpdateBatchJobItemResult(ctx context.Context, batchJobItemID int64, status string, addonID *int64, errorMessage *string) error {
	item, ok := m.items[batchJobItemID]
	if !ok {
		return fmt.Errorf("batch job item %d not found", batchJobItemID)
	}
	item.Status = status
	item.AddonID = addonID
	item.ErrorMessage = errorMessage
	return nil
}

func (m *mockBatchJobRepo) UpdateBatchJobStatus(ctx context.Context, batchJobID int64, status string, succeeded, failed int) error {
	job, ok := m.jobs[batchJobID]
	if !ok {
		return fmt.Errorf("batch job %d not found", batchJobID)
	}
	job.Status = status
	job.SucceededItems = succeeded
	job.FailedItems = failed
	return nil
}

func (m *mockBatchJobRepo) GetBatchJob(ctx context.Context, batchJobID int64) (*manman.WorkshopBatchJob, error) {
	job, ok := m.jobs[batchJobID]
	if !ok {
		return nil, fmt.Errorf("batch job %d not found", batchJobID)
	}
	return job, nil
}

func (m *mockBatchJobRepo) ListBatchJobItems(ctx context.Context, batchJobID int64) ([]*manman.WorkshopBatchJobItem, error) {
	var out []*manman.WorkshopBatchJobItem
	for _, item := range m.items {
		if item.BatchJobID == batchJobID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (m *mockBatchJobRepo) ListBatchJobs(ctx context.Context, gameID int64, limit int) ([]*manman.WorkshopBatchJob, error) {
	return nil, fmt.Errorf("not implemented")
}

// createTestBatchManager builds a WorkshopManager wired only with the
// dependencies BatchCreateAddons exercises (NewWorkshopManager's remaining
// arguments are irrelevant to this RPC and are passed as nil, matching the
// existing createTestManager convention in manager_test.go).
func createTestBatchManager() (*WorkshopManager, *mockAddonRepo, *mockLibraryRepo, *mockBatchJobRepo, *mockSteamClient) {
	addonRepo := &mockAddonRepo{addons: make(map[int64]*manman.WorkshopAddon)}
	libraryRepo := &mockLibraryRepo{attachments: make(map[int64][]int64)}
	batchJobRepo := newMockBatchJobRepo()
	steamClient := &mockSteamClient{
		items:       make(map[string]*steam.WorkshopItemMetadata),
		collections: make(map[string][]steam.CollectionItem),
	}

	manager := NewWorkshopManager(
		addonRepo,
		nil, // installationRepo not needed for BatchCreateAddons
		libraryRepo,
		nil, // sgcRepo not needed
		nil, // gameRepo not needed
		nil, // gameConfigRepo not needed
		nil, // volumeRepo not needed
		nil, // presetRepo not needed
		nil, // sessionRepo not needed
		batchJobRepo,
		steamClient,
		nil, // rmqPublisher not needed
	)

	return manager, addonRepo, libraryRepo, batchJobRepo, steamClient
}

func findItem(items []*manman.WorkshopBatchJobItem, rawInput string) *manman.WorkshopBatchJobItem {
	for _, item := range items {
		if item.RawInput == rawInput {
			return item
		}
	}
	return nil
}

// TestBatchCreateAddons_AllValidMixedBlock covers FR2/FR3: a block of mixed
// raw IDs and Workshop URLs where every entry resolves should create one
// addon per entry, attach each to the target library, and report the job as
// fully completed.
func TestBatchCreateAddons_AllValidMixedBlock(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}

	entries := "111111\nhttps://steamcommunity.com/sharedfiles/filedetails/?id=222222"
	libraryID := int64(50)

	job, items, err := manager.BatchCreateAddons(ctx, 1, libraryID, entries, 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected job-level error: %v", err)
	}

	if job.Status != "completed" {
		t.Errorf("expected job status 'completed', got %s", job.Status)
	}
	if job.TotalItems != 2 || job.SucceededItems != 2 || job.FailedItems != 0 {
		t.Errorf("expected total=2 succeeded=2 failed=0, got total=%d succeeded=%d failed=%d", job.TotalItems, job.SucceededItems, job.FailedItems)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	for _, item := range items {
		if item.Status != "succeeded" {
			t.Errorf("item %q: expected status 'succeeded', got %s", item.RawInput, item.Status)
		}
		if item.AddonID == nil {
			t.Errorf("item %q: expected an addon ID", item.RawInput)
		}
	}
	if len(addonRepo.addons) != 2 {
		t.Errorf("expected 2 addons created, got %d", len(addonRepo.addons))
	}
	if len(libraryRepo.attachments[libraryID]) != 2 {
		t.Errorf("expected 2 library attachments, got %d", len(libraryRepo.attachments[libraryID]))
	}
}

// TestBatchCreateAddons_MalformedLineAmongValid covers FR3: one bad line
// among otherwise-valid lines must not fail the batch. The bad line is
// marked failed with the parser's reason and never reaches Steam; the RPC
// itself returns no error.
func TestBatchCreateAddons_MalformedLineAmongValid(t *testing.T) {
	ctx := context.Background()
	manager, _, _, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}

	entries := "111111\nnot-a-valid-entry\n222222"

	job, items, err := manager.BatchCreateAddons(ctx, 1, 0, entries, 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected error: %v", err)
	}

	if job.Status != "completed_with_errors" {
		t.Errorf("expected job status 'completed_with_errors', got %s", job.Status)
	}
	if job.SucceededItems != 2 || job.FailedItems != 1 {
		t.Errorf("expected succeeded=2 failed=1, got succeeded=%d failed=%d", job.SucceededItems, job.FailedItems)
	}

	badItem := findItem(items, "not-a-valid-entry")
	if badItem == nil {
		t.Fatal("expected an item for the malformed line")
	}
	if badItem.Status != "failed" {
		t.Errorf("expected malformed line status 'failed', got %s", badItem.Status)
	}
	if badItem.ErrorMessage == nil || *badItem.ErrorMessage == "" {
		t.Error("expected a parse-failure reason on the malformed item")
	}
	if badItem.WorkshopID != nil {
		t.Error("expected malformed line to never resolve a Workshop ID (never sent to Steam)")
	}

	for _, name := range []string{"111111", "222222"} {
		item := findItem(items, name)
		if item == nil || item.Status != "succeeded" {
			t.Errorf("expected %q to succeed", name)
		}
	}
}

// TestBatchCreateAddons_SteamNotFoundIsPerItemFailure covers FR3: a Workshop
// ID that Steam reports as not found is a per-item failure with a readable
// message, and other items still commit.
func TestBatchCreateAddons_SteamNotFoundIsPerItemFailure(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	// "999999" is deliberately absent from steamClient.items so
	// GetWorkshopItemDetails reports it as not found.

	entries := "111111\n999999"

	job, items, err := manager.BatchCreateAddons(ctx, 1, 0, entries, 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected error: %v", err)
	}

	if job.Status != "completed_with_errors" {
		t.Errorf("expected job status 'completed_with_errors', got %s", job.Status)
	}

	notFoundItem := findItem(items, "999999")
	if notFoundItem == nil {
		t.Fatal("expected an item for 999999")
	}
	if notFoundItem.Status != "failed" {
		t.Errorf("expected 999999 status 'failed', got %s", notFoundItem.Status)
	}
	if notFoundItem.ErrorMessage == nil || !strings.Contains(*notFoundItem.ErrorMessage, "999999") {
		t.Errorf("expected a readable per-item error message naming the workshop item, got %v", notFoundItem.ErrorMessage)
	}

	okItem := findItem(items, "111111")
	if okItem == nil || okItem.Status != "succeeded" {
		t.Error("expected 111111 to still succeed")
	}
	if len(addonRepo.addons) != 1 {
		t.Errorf("expected exactly 1 addon committed, got %d", len(addonRepo.addons))
	}
}

// TestBatchCreateAddons_NFR5RollbackOnLibraryAttachFailure is the NFR5
// regression test: a Steam client that succeeds the metadata fetch but a
// library-attachment write that fails for one item must leave no
// workshop_addons row behind for that item, while sibling items are fully
// committed.
func TestBatchCreateAddons_NFR5RollbackOnLibraryAttachFailure(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}
	steamClient.items["222222"] = &steam.WorkshopItemMetadata{WorkshopID: "222222", Title: "Map 2", FileSize: 2048, TimeUpdated: time.Now()}

	// mockAddonRepo hands out sequential IDs starting at 1, in creation
	// order. "111111" is processed first (addon ID 1) and "222222" second
	// (addon ID 2), so failing the library attach for addon ID 2 targets the
	// second item deterministically.
	libraryRepo.failForAddonID = map[int64]bool{2: true}

	libraryID := int64(77)
	entries := "111111\n222222"

	job, items, err := manager.BatchCreateAddons(ctx, 1, libraryID, entries, 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected job-level error: %v", err)
	}

	if job.Status != "completed_with_errors" {
		t.Errorf("expected job status 'completed_with_errors', got %s", job.Status)
	}
	if job.SucceededItems != 1 || job.FailedItems != 1 {
		t.Errorf("expected succeeded=1 failed=1, got succeeded=%d failed=%d", job.SucceededItems, job.FailedItems)
	}

	failedItem := findItem(items, "222222")
	succeededItem := findItem(items, "111111")
	if failedItem == nil || succeededItem == nil {
		t.Fatalf("expected both items present")
	}

	if failedItem.Status != "failed" {
		t.Errorf("expected 222222 status 'failed', got %s", failedItem.Status)
	}
	if failedItem.AddonID != nil {
		t.Errorf("expected failed item to have no addon ID recorded, got %v", *failedItem.AddonID)
	}
	// NFR5 core assertion: no orphan workshop_addons row for the failed item.
	if _, exists := addonRepo.addons[2]; exists {
		t.Error("expected addon 2 (library-attach failure) to be rolled back, but it still exists in workshop_addons")
	}
	if len(addonRepo.addons) != 1 {
		t.Errorf("expected exactly 1 addon to remain committed, got %d", len(addonRepo.addons))
	}

	if succeededItem.Status != "succeeded" {
		t.Errorf("expected 111111 status 'succeeded', got %s", succeededItem.Status)
	}
	if succeededItem.AddonID == nil {
		t.Fatal("expected succeeded item to have an addon ID")
	}
	attached := libraryRepo.attachments[libraryID]
	if len(attached) != 1 || attached[0] != *succeededItem.AddonID {
		t.Errorf("expected only the succeeded addon %d attached to the library, got %v", *succeededItem.AddonID, attached)
	}
}

// TestBatchCreateAddons_DuplicateEntriesWithinPaste covers duplicate
// detection within a single paste (same Workshop ID reachable via a raw ID
// and via a URL): only one addon is created, and the second occurrence is
// marked as a duplicate rather than erroring the job.
func TestBatchCreateAddons_DuplicateEntriesWithinPaste(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}

	entries := "111111\nhttps://steamcommunity.com/sharedfiles/filedetails/?id=111111"

	job, items, err := manager.BatchCreateAddons(ctx, 1, 0, entries, 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected error: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items (both lines recorded), got %d", len(items))
	}

	first := findItem(items, "111111")
	second := findItem(items, "https://steamcommunity.com/sharedfiles/filedetails/?id=111111")
	if first == nil || second == nil {
		t.Fatalf("expected both raw lines recorded as items")
	}

	if first.Status != "succeeded" {
		t.Errorf("expected first occurrence to succeed, got %s", first.Status)
	}
	if second.Status != "failed" {
		t.Errorf("expected duplicate occurrence to be marked failed (non-fatal), got %s", second.Status)
	}
	if second.ErrorMessage == nil || !strings.Contains(*second.ErrorMessage, "duplicate") {
		t.Errorf("expected duplicate occurrence's error message to note the duplicate, got %v", second.ErrorMessage)
	}
	if job.Status != "completed_with_errors" {
		t.Errorf("expected job status 'completed_with_errors', got %s", job.Status)
	}
	if len(addonRepo.addons) != 1 {
		t.Errorf("expected exactly 1 addon created for the duplicated ID, got %d", len(addonRepo.addons))
	}
}

// TestBatchCreateAddons_RerunSamePasteReusesExistingAddons covers the safety
// requirement that re-pasting an overlapping list is safe: existing
// (game_id, workshop_id) addons are reused, not duplicated.
func TestBatchCreateAddons_RerunSamePasteReusesExistingAddons(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, libraryRepo, _, steamClient := createTestBatchManager()

	steamClient.items["111111"] = &steam.WorkshopItemMetadata{WorkshopID: "111111", Title: "Map 1", FileSize: 1024, TimeUpdated: time.Now()}

	libraryID := int64(5)
	entries := "111111"

	job1, items1, err := manager.BatchCreateAddons(ctx, 1, libraryID, entries, 0)
	if err != nil {
		t.Fatalf("first BatchCreateAddons call failed: %v", err)
	}
	if job1.Status != "completed" || job1.SucceededItems != 1 {
		t.Fatalf("expected first run to fully succeed, got status=%s succeeded=%d", job1.Status, job1.SucceededItems)
	}
	firstAddonID := *findItem(items1, "111111").AddonID
	if len(addonRepo.addons) != 1 {
		t.Fatalf("expected 1 addon after first run, got %d", len(addonRepo.addons))
	}

	job2, items2, err := manager.BatchCreateAddons(ctx, 1, libraryID, entries, 0)
	if err != nil {
		t.Fatalf("second BatchCreateAddons call failed: %v", err)
	}
	if job2.Status != "completed" || job2.SucceededItems != 1 {
		t.Fatalf("expected second run to fully succeed, got status=%s succeeded=%d", job2.Status, job2.SucceededItems)
	}

	secondItem := findItem(items2, "111111")
	if secondItem == nil || secondItem.AddonID == nil {
		t.Fatal("expected the re-run item to resolve to an addon")
	}
	if *secondItem.AddonID != firstAddonID {
		t.Errorf("expected re-run to reuse addon %d, got a different addon %d", firstAddonID, *secondItem.AddonID)
	}
	if len(addonRepo.addons) != 1 {
		t.Errorf("expected no new addon rows after re-running the same paste, got %d total", len(addonRepo.addons))
	}
	if len(libraryRepo.attachments[libraryID]) != 2 {
		t.Errorf("expected the reused addon to still be (re-)attached to the library on re-run, got %d attachments", len(libraryRepo.attachments[libraryID]))
	}
}

// TestBatchCreateAddons_EmptyBlankInput covers empty/blank-only input:
// zero items are recorded and the RPC/job reports no error.
func TestBatchCreateAddons_EmptyBlankInput(t *testing.T) {
	ctx := context.Background()
	manager, addonRepo, _, _, _ := createTestBatchManager()

	job, items, err := manager.BatchCreateAddons(ctx, 1, 0, "   \n\n\t\n  ", 0)
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected error for blank input: %v", err)
	}
	if job.TotalItems != 0 {
		t.Errorf("expected 0 total items, got %d", job.TotalItems)
	}
	if len(items) != 0 {
		t.Errorf("expected 0 items, got %d", len(items))
	}
	if job.SucceededItems != 0 || job.FailedItems != 0 {
		t.Errorf("expected succeeded=0 failed=0, got succeeded=%d failed=%d", job.SucceededItems, job.FailedItems)
	}
	if len(addonRepo.addons) != 0 {
		t.Errorf("expected no addons created for blank input, got %d", len(addonRepo.addons))
	}
}
