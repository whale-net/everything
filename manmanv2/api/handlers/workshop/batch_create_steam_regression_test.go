package workshop

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/whale-net/everything/manmanv2/api/steam"
	"github.com/whale-net/everything/manmanv2/api/workshop"
	"github.com/whale-net/everything/manmanv2/models"
	pb "github.com/whale-net/everything/manmanv2/protos"
)

// fakeBatchJobRepo is a minimal in-memory repository.WorkshopBatchJobRepository
// test double for TestBatchCreateAddons_MixedBatchSteamNotFoundRegression.
// It mirrors mockBatchJobRepo in manmanv2/api/workshop/batch_test.go (an
// unexported type in a different package, so it can't be reused directly):
// a real in-memory store is simpler here than a testify mock, since
// BatchCreateAddons relies on CreateBatchJob/CreateBatchJobItems assigning
// real IDs that later calls in the same run reference.
type fakeBatchJobRepo struct {
	jobs       map[int64]*manman.WorkshopBatchJob
	items      map[int64]*manman.WorkshopBatchJobItem
	nextJobID  int64
	nextItemID int64
}

func newFakeBatchJobRepo() *fakeBatchJobRepo {
	return &fakeBatchJobRepo{
		jobs:  make(map[int64]*manman.WorkshopBatchJob),
		items: make(map[int64]*manman.WorkshopBatchJobItem),
	}
}

func (f *fakeBatchJobRepo) CreateBatchJob(ctx context.Context, job *manman.WorkshopBatchJob) (*manman.WorkshopBatchJob, error) {
	f.nextJobID++
	job.BatchJobID = f.nextJobID
	f.jobs[job.BatchJobID] = job
	return job, nil
}

func (f *fakeBatchJobRepo) CreateBatchJobItems(ctx context.Context, batchJobID int64, items []*manman.WorkshopBatchJobItem) error {
	for _, item := range items {
		f.nextItemID++
		item.BatchJobItemID = f.nextItemID
		item.BatchJobID = batchJobID
		f.items[item.BatchJobItemID] = item
	}
	return nil
}

func (f *fakeBatchJobRepo) UpdateBatchJobItemResult(ctx context.Context, batchJobItemID int64, status string, addonID *int64, errorMessage *string) error {
	item, ok := f.items[batchJobItemID]
	if !ok {
		return fmt.Errorf("batch job item %d not found", batchJobItemID)
	}
	item.Status = status
	item.AddonID = addonID
	item.ErrorMessage = errorMessage
	return nil
}

func (f *fakeBatchJobRepo) UpdateBatchJobStatus(ctx context.Context, batchJobID int64, status string, succeeded, failed int) error {
	job, ok := f.jobs[batchJobID]
	if !ok {
		return fmt.Errorf("batch job %d not found", batchJobID)
	}
	job.Status = status
	job.SucceededItems = succeeded
	job.FailedItems = failed
	return nil
}

func (f *fakeBatchJobRepo) GetBatchJob(ctx context.Context, batchJobID int64) (*manman.WorkshopBatchJob, error) {
	job, ok := f.jobs[batchJobID]
	if !ok {
		return nil, fmt.Errorf("batch job %d not found", batchJobID)
	}
	return job, nil
}

func (f *fakeBatchJobRepo) ListBatchJobItems(ctx context.Context, batchJobID int64) ([]*manman.WorkshopBatchJobItem, error) {
	var out []*manman.WorkshopBatchJobItem
	for _, item := range f.items {
		if item.BatchJobID == batchJobID {
			out = append(out, item)
		}
	}
	return out, nil
}

func (f *fakeBatchJobRepo) ListBatchJobs(ctx context.Context, gameID int64, limit int) ([]*manman.WorkshopBatchJob, error) {
	return nil, fmt.Errorf("not implemented")
}

// TestBatchCreateAddons_MixedBatchSteamNotFoundRegression is the
// handler-level FR3 regression test for issue #2212 / finding #2209: it
// mirrors the validator's live grpcurl repro of a batch containing one real
// Workshop ID (160250458) and one definitely-nonexistent one
// (999999999999999).
//
// Unlike the other BatchCreateAddons handler tests in workshop_test.go,
// this wires the handler to a *real* workshop.WorkshopManager backed by a
// *real* steam.SteamWorkshopClient pointed at an httptest server (via the
// client's BaseURL test seam), rather than mocking
// WorkshopManagerInterface. That's deliberate: the bug this guards against
// lived in SteamWorkshopClient.GetWorkshopItemDetails's response decoding
// (manmanv2/api/steam/client.go), a layer a WorkshopManagerInterface mock
// bypasses entirely. Only by going through the real client against a
// fixture shaped like Steam's actual response (a "result" field of 9 --
// not found -- on the sole entry, since Steam always returns exactly one
// entry per requested ID) does this test actually re-detect the original
// defect: a bogus ID silently decoding into an all-empty-fields
// "succeeded" addon instead of a per-item failure.
func TestBatchCreateAddons_MixedBatchSteamNotFoundRegression(t *testing.T) {
	const realID = "160250458"
	const fakeID = "999999999999999"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Fatalf("failed to parse form: %v", err)
		}
		requestedID := r.FormValue("publishedfileids[0]")

		var detail map[string]interface{}
		switch requestedID {
		case realID:
			detail = map[string]interface{}{
				"publishedfileid":  realID,
				"result":           1,
				"title":            "A Real Workshop Item",
				"file_description": "Resolvable via Steam",
				"file_size":        "2048",
				"time_updated":     1609459200,
				"file_type":        0,
			}
		case fakeID:
			// Mirrors Steam's real behavior for a nonexistent Workshop ID:
			// exactly one entry, all other fields blank, "result": 9.
			detail = map[string]interface{}{
				"publishedfileid": fakeID,
				"result":          9,
			}
		default:
			t.Fatalf("unexpected workshop ID requested: %s", requestedID)
		}

		response := map[string]interface{}{
			"response": map[string]interface{}{
				"result":               1,
				"resultcount":          1,
				"publishedfiledetails": []map[string]interface{}{detail},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(response)
	}))
	defer server.Close()

	steamClient := steam.NewSteamWorkshopClient("test-api-key", 5*time.Second)
	steamClient.BaseURL = server.URL

	addonRepo := new(MockWorkshopAddonRepository)
	// Neither ID has an existing addon row -- both lookups miss, so
	// makeBatchCreateAddonFunc proceeds to fetch metadata from Steam for
	// each.
	addonRepo.On("GetByWorkshopID", mock.Anything, int64(1), realID, manman.PlatformTypeSteamWorkshop).
		Return(nil, assert.AnError)
	addonRepo.On("GetByWorkshopID", mock.Anything, int64(1), fakeID, manman.PlatformTypeSteamWorkshop).
		Return(nil, assert.AnError)
	// Create must never be invoked for fakeID: a correctly-behaving
	// GetWorkshopItemDetails reports it as an error before addon creation
	// is ever attempted. This is asserted explicitly (via t.Errorf) rather
	// than relying on the mock's own "unexpected call" panic, since that
	// panic is caught and recorded as a per-item failure by runBatchJob's
	// panic recovery (NFR5) -- which would otherwise let this regression
	// test pass for the wrong reason (a mock-call panic, not a Steam
	// not-found error) if the fix under test were ever reverted.
	addonRepo.On("Create", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			addon := args.Get(1).(*manman.WorkshopAddon)
			if addon.WorkshopID == fakeID {
				t.Errorf("addonRepo.Create must never be called for the nonexistent workshop ID %s -- GetWorkshopItemDetails should have reported it as not-found before reaching addon creation", fakeID)
			}
		}).
		Return(&manman.WorkshopAddon{AddonID: 42, WorkshopID: realID, GameID: 1}, nil)

	batchJobRepo := newFakeBatchJobRepo()

	manager := workshop.NewWorkshopManager(
		addonRepo,
		nil, // installationRepo not needed for BatchCreateAddons
		nil, // libraryRepo not needed: request has no library_id
		nil, // sgcRepo not needed
		nil, // gameRepo not needed
		nil, // gameConfigRepo not needed
		nil, // gcLibraryRepo not needed
		nil, // volumeRepo not needed
		nil, // presetRepo not needed
		nil, // sessionRepo not needed
		batchJobRepo,
		steamClient,
		nil, // rmqPublisher not needed
	)

	handler := &WorkshopServiceHandler{
		workshopManager: manager,
	}

	resp, err := handler.BatchCreateAddons(context.Background(), &pb.BatchCreateAddonsRequest{
		GameId:  1,
		Entries: realID + "\n" + fakeID,
	})
	if err != nil {
		t.Fatalf("BatchCreateAddons returned unexpected RPC-level error: %v", err)
	}

	assert.Equal(t, int32(2), resp.TotalItems)
	assert.Equal(t, int32(1), resp.SucceededItems, "the real Workshop ID must succeed")
	assert.Equal(t, int32(1), resp.FailedItems, "the nonexistent Workshop ID must be reported as failed, not succeeded")

	if !assert.Len(t, resp.Results, 2) {
		return
	}

	var realResult, fakeResult *pb.BatchItemResult
	for _, r := range resp.Results {
		switch r.RawInput {
		case realID:
			realResult = r
		case fakeID:
			fakeResult = r
		}
	}

	if assert.NotNil(t, realResult, "expected a result for the real Workshop ID") {
		assert.Equal(t, "succeeded", realResult.Status)
		assert.Equal(t, int64(42), realResult.AddonId)
		assert.Empty(t, realResult.ErrorMessage)
	}
	if assert.NotNil(t, fakeResult, "expected a result for the fake Workshop ID") {
		assert.Equal(t, "failed", fakeResult.Status,
			"a nonexistent Workshop ID must render as an individually-failed item, not a blank-titled succeeded addon")
		assert.Zero(t, fakeResult.AddonId)
		assert.NotEmpty(t, fakeResult.ErrorMessage, "expected a readable per-item error message")
		assert.Contains(t, fakeResult.ErrorMessage, fakeID)
	}

	addonRepo.AssertExpectations(t)
}
