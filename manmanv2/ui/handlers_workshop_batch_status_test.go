package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #2179's manual-reload batch-status view (FR4, plan
// #2175): a completed job and a completed_with_errors job must both render
// their per-item results -- including each failure's error message for the
// failed job -- a 0-item job must render without error, and the page must
// never carry an auto-refresh mechanism (FR4 scope boundary: manual reload
// only, via a plain link that calls window.location.reload()).
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// adding hx-trigger="every 5s" to workshop_batch_status.templ's header
// <div> made all three tests below fail on assertNoAutoRefresh with the
// expected "found marker" message; removing it restored green. This is
// the FR4 scope-boundary guard the task calls for.

// fakeBatchStatusWorkshopClient embeds the nil WorkshopServiceClient
// interface and overrides only GetBatchJob, the single RPC
// handleWorkshopBatchStatus's call graph reaches -- any other call panics
// on the nil embedded interface, which is deliberate (see
// handlers_sgc_test.go's fakeWorkshopServiceClient for the same pattern).
type fakeBatchStatusWorkshopClient struct {
	manmanpb.WorkshopServiceClient

	job   *manmanpb.WorkshopBatchJob
	items []*manmanpb.BatchItemResult
	err   error
}

func (f *fakeBatchStatusWorkshopClient) GetBatchJob(ctx context.Context, in *manmanpb.GetBatchJobRequest, opts ...grpc.CallOption) (*manmanpb.GetBatchJobResponse, error) {
	if f.err != nil {
		return nil, f.err
	}
	return &manmanpb.GetBatchJobResponse{Job: f.job, Items: f.items}, nil
}

func newBatchStatusTestApp(workshop *fakeBatchStatusWorkshopClient) *App {
	return &App{
		grpc: &ControlClient{api: &fakeManManAPIClient{}, workshop: workshop},
	}
}

func renderWorkshopBatchStatus(t *testing.T, app *App, batchJobID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/workshop/batch-status?batch_job_id="+batchJobID, nil)
	w := httptest.NewRecorder()
	app.handleWorkshopBatchStatus(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleWorkshopBatchStatus status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	return w.Body.String()
}

// assertNoAutoRefresh guards the FR4 scope boundary: no SSE, polling, or
// hx-trigger="every ..." auto-updating mechanism may appear on this page.
func assertNoAutoRefresh(t *testing.T, body string) {
	t.Helper()
	for _, marker := range []string{"hx-trigger=\"every", "hx-ext=\"sse\"", "sse-connect", "sse-swap"} {
		if strings.Contains(body, marker) {
			t.Errorf("expected no auto-refresh mechanism on the batch-status page, found marker %q", marker)
		}
	}
}

func TestHandleWorkshopBatchStatus_Completed_RendersSuccessItems(t *testing.T) {
	job := &manmanpb.WorkshopBatchJob{
		BatchJobId:     10,
		JobType:        "collection_add",
		GameId:         1,
		LibraryId:      2,
		Status:         "completed",
		TotalItems:     2,
		SucceededItems: 2,
		FailedItems:    0,
	}
	items := []*manmanpb.BatchItemResult{
		{RawInput: "111", WorkshopId: "111", AddonId: 501, Status: "succeeded"},
		{RawInput: "222", WorkshopId: "222", AddonId: 502, Status: "succeeded"},
	}
	app := newBatchStatusTestApp(&fakeBatchStatusWorkshopClient{job: job, items: items})

	body := renderWorkshopBatchStatus(t, app, "10")

	if !strings.Contains(body, "completed") {
		t.Errorf("expected job status 'completed' in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "/workshop/addon?addon_id=501") || !strings.Contains(body, "/workshop/addon?addon_id=502") {
		t.Errorf("expected succeeded items to link to their resulting addon, got: %s", body)
	}
	assertNoAutoRefresh(t, body)
}

func TestHandleWorkshopBatchStatus_CompletedWithErrors_ShowsFailureMessages(t *testing.T) {
	job := &manmanpb.WorkshopBatchJob{
		BatchJobId:     11,
		JobType:        "batch_create",
		GameId:         1,
		LibraryId:      3,
		Status:         "completed_with_errors",
		TotalItems:     3,
		SucceededItems: 1,
		FailedItems:    2,
	}
	items := []*manmanpb.BatchItemResult{
		{RawInput: "111", WorkshopId: "111", AddonId: 601, Status: "succeeded"},
		{RawInput: "bad-url", Status: "failed", ErrorMessage: "could not parse workshop id"},
		{RawInput: "999999999", WorkshopId: "999999999", Status: "failed", ErrorMessage: "workshop item not found"},
	}
	app := newBatchStatusTestApp(&fakeBatchStatusWorkshopClient{job: job, items: items})

	body := renderWorkshopBatchStatus(t, app, "11")

	if !strings.Contains(body, "completed_with_errors") {
		t.Errorf("expected job status 'completed_with_errors' in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "could not parse workshop id") {
		t.Errorf("expected the first failure's error message in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "workshop item not found") {
		t.Errorf("expected the second failure's error message in rendered body, got: %s", body)
	}
	assertNoAutoRefresh(t, body)
}

func TestHandleWorkshopBatchStatus_ZeroItems_RendersEmptyState(t *testing.T) {
	job := &manmanpb.WorkshopBatchJob{
		BatchJobId: 12,
		JobType:    "batch_create",
		GameId:     1,
		LibraryId:  4,
		Status:     "pending",
	}
	app := newBatchStatusTestApp(&fakeBatchStatusWorkshopClient{job: job, items: nil})

	body := renderWorkshopBatchStatus(t, app, "12")

	if !strings.Contains(body, "No items in this batch job") {
		t.Errorf("expected the empty-items message in rendered body, got: %s", body)
	}
	assertNoAutoRefresh(t, body)
}
