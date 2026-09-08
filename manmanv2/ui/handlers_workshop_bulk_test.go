package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #2180's UI entry points for the collection bulk-add
// (FR1) and mixed-format batch-create (FR2/FR3) flows: both forms must post
// the Server Manager's raw input through to the RPCs from #2177/#2178
// completely untouched (no client-side reformatting/filtering -- parsing is
// the server's job, #2176), a successful call (including a
// completed_with_errors partial failure, which is not an RPC error) must
// hand off to the batch-status view (#2179) for the returned batch_job_id
// rather than showing a spinner or toast, and a job-level RPC failure must
// re-render the library detail page inline with the input echoed back
// rather than losing it via a redirect.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// changing handleBatchCreateAddons' success path to redirect only when
// resp.FailedItems == 0 (treating a completed_with_errors result as an
// error) made TestHandleBatchCreateAddons_PartialFailure_StillRedirectsToBatchStatus
// fail on the redirect-location assertion; reverting restored green.

// fakeBulkWorkshopClient embeds the nil WorkshopServiceClient interface and
// overrides only the RPCs handleBulkAddCollection/handleBatchCreateAddons's
// call graphs reach: the two bulk RPCs themselves, plus the handful
// loadWorkshopLibraryDetailData needs to re-render the page on a job-level
// failure (GetLibrary, GetLibraryAddons, ListAddons, ListAddonPathPresets).
// Any other call panics on the nil embedded interface, same pattern as
// handlers_sgc_test.go's fakeWorkshopServiceClient.
type fakeBulkWorkshopClient struct {
	manmanpb.WorkshopServiceClient

	library *manmanpb.WorkshopLibrary

	addCollectionReq  *manmanpb.AddCollectionToLibraryRequest
	addCollectionResp *manmanpb.AddCollectionToLibraryResponse
	addCollectionErr  error

	batchCreateReq  *manmanpb.BatchCreateAddonsRequest
	batchCreateResp *manmanpb.BatchCreateAddonsResponse
	batchCreateErr  error
}

func (f *fakeBulkWorkshopClient) GetLibrary(ctx context.Context, in *manmanpb.GetLibraryRequest, opts ...grpc.CallOption) (*manmanpb.GetLibraryResponse, error) {
	return &manmanpb.GetLibraryResponse{Library: f.library}, nil
}

func (f *fakeBulkWorkshopClient) GetLibraryAddons(ctx context.Context, in *manmanpb.GetLibraryAddonsRequest, opts ...grpc.CallOption) (*manmanpb.GetLibraryAddonsResponse, error) {
	return &manmanpb.GetLibraryAddonsResponse{}, nil
}

func (f *fakeBulkWorkshopClient) ListAddons(ctx context.Context, in *manmanpb.ListAddonsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonsResponse, error) {
	return &manmanpb.ListAddonsResponse{}, nil
}

func (f *fakeBulkWorkshopClient) ListAddonPathPresets(ctx context.Context, in *manmanpb.ListAddonPathPresetsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonPathPresetsResponse, error) {
	return &manmanpb.ListAddonPathPresetsResponse{}, nil
}

func (f *fakeBulkWorkshopClient) AddCollectionToLibrary(ctx context.Context, in *manmanpb.AddCollectionToLibraryRequest, opts ...grpc.CallOption) (*manmanpb.AddCollectionToLibraryResponse, error) {
	f.addCollectionReq = in
	if f.addCollectionErr != nil {
		return nil, f.addCollectionErr
	}
	return f.addCollectionResp, nil
}

func (f *fakeBulkWorkshopClient) BatchCreateAddons(ctx context.Context, in *manmanpb.BatchCreateAddonsRequest, opts ...grpc.CallOption) (*manmanpb.BatchCreateAddonsResponse, error) {
	f.batchCreateReq = in
	if f.batchCreateErr != nil {
		return nil, f.batchCreateErr
	}
	return f.batchCreateResp, nil
}

// fakeBulkManManAPIClient embeds the nil ManManAPIClient interface and
// overrides only ListGames -- the one api-side RPC
// loadWorkshopLibraryDetailData's re-render path reaches.
type fakeBulkManManAPIClient struct {
	manmanpb.ManManAPIClient
}

func (f *fakeBulkManManAPIClient) ListGames(ctx context.Context, in *manmanpb.ListGamesRequest, opts ...grpc.CallOption) (*manmanpb.ListGamesResponse, error) {
	return &manmanpb.ListGamesResponse{}, nil
}

// ListServers is reached indirectly via buildTemplLayoutData, which every
// full-page render (including the inline error re-render) goes through.
func (f *fakeBulkManManAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{}, nil
}

func newBulkTestApp(workshop *fakeBulkWorkshopClient) *App {
	return &App{
		grpc: &ControlClient{api: &fakeBulkManManAPIClient{}, workshop: workshop},
	}
}

var (
	errUnresolvableCollection = errors.New("could not resolve collection: workshop item not found")
	errMissingGame            = errors.New("game not found")
)

// --- handleBulkAddCollection (FR1) -----------------------------------------

func TestHandleBulkAddCollection_PostsCollectionInputVerbatim(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"raw ID", "2947880477"},
		{"workshop URL", "https://steamcommunity.com/sharedfiles/filedetails/?id=2947880477"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			workshop := &fakeBulkWorkshopClient{
				addCollectionResp: &manmanpb.AddCollectionToLibraryResponse{BatchJobId: 55},
			}
			app := newBulkTestApp(workshop)

			form := url.Values{
				"game_id":          {"1"},
				"library_id":       {"2"},
				"collection_input": {tc.input},
			}
			req := httptest.NewRequest(http.MethodPost, "/workshop/bulk-add-collection", strings.NewReader(form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			app.handleBulkAddCollection(w, req)

			if workshop.addCollectionReq == nil {
				t.Fatalf("expected AddCollectionToLibrary to be called")
			}
			if workshop.addCollectionReq.CollectionInput != tc.input {
				t.Errorf("CollectionInput = %q, want %q (verbatim, no client-side reformatting)", workshop.addCollectionReq.CollectionInput, tc.input)
			}
		})
	}
}

func TestHandleBulkAddCollection_Success_RedirectsToBatchStatus(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		addCollectionResp: &manmanpb.AddCollectionToLibraryResponse{BatchJobId: 55, TotalItems: 3, SucceededItems: 3},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":          {"1"},
		"library_id":       {"2"},
		"collection_input": {"2947880477"},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/bulk-add-collection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBulkAddCollection(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	wantLocation := "/workshop/batch-status?batch_job_id=" + strconv.FormatInt(55, 10)
	if got := w.Header().Get("Location"); got != wantLocation {
		t.Errorf("Location = %q, want %q", got, wantLocation)
	}
}

func TestHandleBulkAddCollection_JobLevelError_RendersInlineErrorAndEchoesInput(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		library:          &manmanpb.WorkshopLibrary{LibraryId: 2, GameId: 1, Name: "Essential Maps"},
		addCollectionErr: errUnresolvableCollection,
	}
	app := newBulkTestApp(workshop)

	const input = "not-a-real-collection"
	form := url.Values{
		"game_id":          {"1"},
		"library_id":       {"2"},
		"collection_input": {input},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/bulk-add-collection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBulkAddCollection(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, errUnresolvableCollection.Error()) {
		t.Errorf("expected the job-level error message in rendered body, got: %s", body)
	}
	if !strings.Contains(body, input) {
		t.Errorf("expected the submitted collection_input to be echoed back into the form, got: %s", body)
	}
}

func TestHandleBulkAddCollection_MissingCollectionInput_RejectedWithoutRPCCall(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		library: &manmanpb.WorkshopLibrary{LibraryId: 2, GameId: 1, Name: "Essential Maps"},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":          {"1"},
		"library_id":       {"2"},
		"collection_input": {""},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/bulk-add-collection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBulkAddCollection(w, req)

	if workshop.addCollectionReq != nil {
		t.Errorf("expected no AddCollectionToLibrary call for a missing collection_input, got a call")
	}
	if !strings.Contains(w.Body.String(), "required") {
		t.Errorf("expected a required-field message, got: %s", w.Body.String())
	}
}

func TestHandleBulkAddCollection_MissingLibraryID_RejectedWithoutRPCCall(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":          {"1"},
		"collection_input": {"2947880477"},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/bulk-add-collection", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBulkAddCollection(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", w.Code, http.StatusBadRequest)
	}
	if workshop.addCollectionReq != nil {
		t.Errorf("expected no AddCollectionToLibrary call for a missing library_id, got a call")
	}
}

// --- handleBatchCreateAddons (FR2, FR3) -------------------------------------

func TestHandleBatchCreateAddons_PostsEntriesVerbatimPreservingLineBreaks(t *testing.T) {
	const entries = "2947880477\nhttps://steamcommunity.com/sharedfiles/filedetails/?id=3100488121\nnot-a-valid-line\n"
	workshop := &fakeBulkWorkshopClient{
		batchCreateResp: &manmanpb.BatchCreateAddonsResponse{BatchJobId: 77},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":    {"1"},
		"library_id": {"2"},
		"entries":    {entries},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/batch-create-addons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBatchCreateAddons(w, req)

	if workshop.batchCreateReq == nil {
		t.Fatalf("expected BatchCreateAddons to be called")
	}
	if workshop.batchCreateReq.Entries != entries {
		t.Errorf("Entries = %q, want %q verbatim (line breaks preserved, no client-side filtering of bad lines)", workshop.batchCreateReq.Entries, entries)
	}
}

func TestHandleBatchCreateAddons_Success_RedirectsToBatchStatus(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		batchCreateResp: &manmanpb.BatchCreateAddonsResponse{BatchJobId: 77, TotalItems: 2, SucceededItems: 2},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":    {"1"},
		"library_id": {"2"},
		"entries":    {"2947880477\n2901733208"},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/batch-create-addons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBatchCreateAddons(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	wantLocation := "/workshop/batch-status?batch_job_id=" + strconv.FormatInt(77, 10)
	if got := w.Header().Get("Location"); got != wantLocation {
		t.Errorf("Location = %q, want %q", got, wantLocation)
	}
}

// FR3: a partial-failure result (completed_with_errors) is not an RPC error
// -- it must still route to the batch-status view rather than being shown
// as a UI-level error, so the Server Manager sees per-item results instead
// of an error banner burying the items that did succeed.
func TestHandleBatchCreateAddons_PartialFailure_StillRedirectsToBatchStatus(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		batchCreateResp: &manmanpb.BatchCreateAddonsResponse{
			BatchJobId:     77,
			TotalItems:     3,
			SucceededItems: 1,
			FailedItems:    2,
		},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":    {"1"},
		"library_id": {"2"},
		"entries":    {"2947880477\nbad-line\nalso-bad"},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/batch-create-addons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBatchCreateAddons(w, req)

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (completed_with_errors must not be treated as a UI-level error); body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	wantLocation := "/workshop/batch-status?batch_job_id=" + strconv.FormatInt(77, 10)
	if got := w.Header().Get("Location"); got != wantLocation {
		t.Errorf("Location = %q, want %q", got, wantLocation)
	}
}

func TestHandleBatchCreateAddons_JobLevelError_RendersInlineErrorAndEchoesEntries(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		library:        &manmanpb.WorkshopLibrary{LibraryId: 2, GameId: 1, Name: "Essential Maps"},
		batchCreateErr: errMissingGame,
	}
	app := newBulkTestApp(workshop)

	const entries = "2947880477\nhttps://steamcommunity.com/sharedfiles/filedetails/?id=3100488121"
	form := url.Values{
		"game_id":    {"999"},
		"library_id": {"2"},
		"entries":    {entries},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/batch-create-addons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBatchCreateAddons(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, errMissingGame.Error()) {
		t.Errorf("expected the job-level error message in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "2947880477") || !strings.Contains(body, "3100488121") {
		t.Errorf("expected the submitted entries to be echoed back into the form, got: %s", body)
	}
}

func TestHandleBatchCreateAddons_MissingEntries_RejectedWithoutRPCCall(t *testing.T) {
	workshop := &fakeBulkWorkshopClient{
		library: &manmanpb.WorkshopLibrary{LibraryId: 2, GameId: 1, Name: "Essential Maps"},
	}
	app := newBulkTestApp(workshop)

	form := url.Values{
		"game_id":    {"1"},
		"library_id": {"2"},
		"entries":    {"   "},
	}
	req := httptest.NewRequest(http.MethodPost, "/workshop/batch-create-addons", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBatchCreateAddons(w, req)

	if workshop.batchCreateReq != nil {
		t.Errorf("expected no BatchCreateAddons call for a blank entries block, got a call")
	}
	if !strings.Contains(w.Body.String(), "required") {
		t.Errorf("expected a required-field message, got: %s", w.Body.String())
	}
}
