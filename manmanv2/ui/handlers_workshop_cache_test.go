package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards #2185's Admin fleet-wide Workshop cache visibility view
// (FR10, plan #2175): every content-addressed cache entry for an addon,
// each entry's rendered staleness ("verified Xh ago" / "never verified",
// not a raw timestamp), and the hosts holding a copy of each entry -- from
// one place, without per-host queries. Same M4 manual-reload boundary as
// the batch-status view (no SSE/polling/hx-trigger).
//
// fakeWorkshopCacheClient embeds the nil WorkshopServiceClient interface
// and overrides only GetAddon and ListAddonCacheEntries -- the two RPCs
// handleWorkshopCache's call graph reaches -- per handlers_sgc_test.go's
// fakeWorkshopServiceClient / handlers_workshop_batch_status_test.go's
// fakeBatchStatusWorkshopClient pattern. Any other call panics on the nil
// embedded interface, which is deliberate.
type fakeWorkshopCacheClient struct {
	manmanpb.WorkshopServiceClient

	addon      *manmanpb.WorkshopAddon
	addonErr   error
	entries    []*manmanpb.WorkshopCacheEntry
	entriesErr error

	// verifyResp/verifyErr back VerifyCacheEntry (#2186, FR11) --
	// handleWorkshopCacheVerify's dispatch RPC. verifyCalls records every
	// request so tests can assert the exact cache_entry_id/server_id posted.
	verifyResp  *manmanpb.VerifyCacheEntryResponse
	verifyErr   error
	verifyCalls []*manmanpb.VerifyCacheEntryRequest
}

func (f *fakeWorkshopCacheClient) GetAddon(ctx context.Context, in *manmanpb.GetAddonRequest, opts ...grpc.CallOption) (*manmanpb.GetAddonResponse, error) {
	if f.addonErr != nil {
		return nil, f.addonErr
	}
	return &manmanpb.GetAddonResponse{Addon: f.addon}, nil
}

func (f *fakeWorkshopCacheClient) ListAddonCacheEntries(ctx context.Context, in *manmanpb.ListAddonCacheEntriesRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonCacheEntriesResponse, error) {
	if f.entriesErr != nil {
		return nil, f.entriesErr
	}
	return &manmanpb.ListAddonCacheEntriesResponse{Entries: f.entries}, nil
}

func (f *fakeWorkshopCacheClient) VerifyCacheEntry(ctx context.Context, in *manmanpb.VerifyCacheEntryRequest, opts ...grpc.CallOption) (*manmanpb.VerifyCacheEntryResponse, error) {
	f.verifyCalls = append(f.verifyCalls, in)
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return f.verifyResp, nil
}

func newWorkshopCacheTestApp(workshop *fakeWorkshopCacheClient) *App {
	return &App{
		grpc: &ControlClient{api: &fakeManManAPIClient{}, workshop: workshop},
	}
}

func renderWorkshopCache(t *testing.T, app *App, addonID string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/workshop/cache?addon_id="+addonID, nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCache(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleWorkshopCache status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	return w.Body.String()
}

func TestHandleWorkshopCache_MultipleVersions_RendersDistinctContentAddresses(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{
			CacheEntryId:   2,
			WorkshopId:     "123456",
			ContentVersion: "v2-content-address",
			CreatedAt:      1700000200,
			LastVerifiedAt: 1700000300,
			Hosts: []*manmanpb.WorkshopCacheHost{
				{ServerId: 10, ServerName: "host-b", LastSeenAt: 1700000300},
			},
		},
		{
			CacheEntryId:   1,
			WorkshopId:     "123456",
			ContentVersion: "v1-content-address",
			CreatedAt:      1700000100,
			LastVerifiedAt: 0,
			Hosts: []*manmanpb.WorkshopCacheHost{
				{ServerId: 11, ServerName: "host-a", LastSeenAt: 1700000150},
			},
		},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "v2-content-address") {
		t.Errorf("expected the first entry's content address in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "v1-content-address") {
		t.Errorf("expected the second entry's content address in rendered body, got: %s", body)
	}
	// Both are distinct content addresses -- the page must render each once,
	// not collapse them into a single row.
	if strings.Count(body, "content-address") != 2 {
		t.Errorf("expected exactly 2 distinct content addresses rendered, got %d occurrences in: %s", strings.Count(body, "content-address"), body)
	}
}

func TestHandleWorkshopCache_NullLastVerifiedAt_RendersNeverVerified(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{
			CacheEntryId:   1,
			WorkshopId:     "123456",
			ContentVersion: "v1",
			CreatedAt:      1700000100,
			LastVerifiedAt: 0,
		},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "never verified") {
		t.Errorf("expected 'never verified' for a null last_verified_at, got: %s", body)
	}
}

func TestHandleWorkshopCache_VerifiedEntry_RendersRelativeStaleness(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{
			CacheEntryId:   1,
			WorkshopId:     "123456",
			ContentVersion: "v1",
			CreatedAt:      1700000100,
			// LastVerifiedAt in the past (non-zero) must render as a relative
			// "verified ... ago" label, never a raw timestamp or the
			// "never verified" placeholder used for the zero case.
			LastVerifiedAt: 1700000100,
		},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "verified") || strings.Contains(body, "never verified") {
		t.Errorf("expected a rendered relative staleness label (not 'never verified') for a verified entry, got: %s", body)
	}
}

func TestHandleWorkshopCache_RendersEachEntrysHosts(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{
			CacheEntryId:   1,
			WorkshopId:     "123456",
			ContentVersion: "v1",
			CreatedAt:      1700000100,
			Hosts: []*manmanpb.WorkshopCacheHost{
				{ServerId: 10, ServerName: "alpha-host", LastSeenAt: 1700000150},
				{ServerId: 11, ServerName: "bravo-host", LastSeenAt: 1700000160},
			},
		},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "alpha-host") {
		t.Errorf("expected host 'alpha-host' in rendered body, got: %s", body)
	}
	if !strings.Contains(body, "bravo-host") {
		t.Errorf("expected host 'bravo-host' in rendered body, got: %s", body)
	}
}

func TestHandleWorkshopCache_UncachedAddon_RendersEmptyState(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: nil})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "No cache entries for this addon") {
		t.Errorf("expected the empty-state message for an uncached addon, got: %s", body)
	}
}

func TestHandleWorkshopCache_MissingAddonID_BadRequest(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	req := httptest.NewRequest(http.MethodGet, "/workshop/cache", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCache(w, req)
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d when addon_id is missing", w.Code, http.StatusBadRequest)
	}
}

// TestHandleWorkshopCache_RendersVerifyNowFormPerEntry proves each cache entry row
// carries a "Verify now" action (the slot #2185 left, per the issue) that posts the
// exact addon_id/cache_entry_id pair as hidden form fields to /workshop/cache/verify.
func TestHandleWorkshopCache_RendersVerifyNowFormPerEntry(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{CacheEntryId: 7, WorkshopId: "123456", ContentVersion: "v1", CreatedAt: 1700000100},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, `action="/workshop/cache/verify"`) {
		t.Errorf("expected a form posting to /workshop/cache/verify, got: %s", body)
	}
	if !strings.Contains(body, `name="cache_entry_id" value="7"`) {
		t.Errorf("expected the verify form to carry cache_entry_id=7, got: %s", body)
	}
	if !strings.Contains(body, `name="addon_id" value="42"`) {
		t.Errorf("expected the verify form to carry addon_id=42, got: %s", body)
	}
	if !strings.Contains(body, "Verify now") {
		t.Errorf("expected a 'Verify now' button, got: %s", body)
	}
}

// TestHandleWorkshopCache_DispatchedVerifyState_RendersSuccessBanner proves the
// dispatched outcome (carried via the verify_status/verify_server_id query params
// handleWorkshopCacheVerify's redirect sets) renders an inline success banner naming
// the chosen host, not an error toast.
func TestHandleWorkshopCache_DispatchedVerifyState_RendersSuccessBanner(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon})

	req := httptest.NewRequest(http.MethodGet, "/workshop/cache?addon_id=42&verify_status=dispatched&verify_server_id=9", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCache(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "Verify dispatched to server") || !strings.Contains(body, "9") {
		t.Errorf("expected a dispatched banner naming server 9, got: %s", body)
	}
	if strings.Contains(body, "alert-warning") {
		t.Errorf("a successful dispatch must not render as an error/warning banner, got: %s", body)
	}
}

// TestHandleWorkshopCache_NoHostAvailableVerifyState_RendersInlineMessageNotError
// proves the issue's explicit "no_host_available renders as a clear inline message,
// not an error toast" contract.
func TestHandleWorkshopCache_NoHostAvailableVerifyState_RendersInlineMessageNotError(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon})

	req := httptest.NewRequest(http.MethodGet, "/workshop/cache?addon_id=42&verify_status=no_host_available", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCache(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()

	if !strings.Contains(body, "No host currently holds a copy") {
		t.Errorf("expected the no_host_available inline message, got: %s", body)
	}
	if strings.Contains(body, "alert-warning") || strings.Contains(body, "alert-error") {
		t.Errorf("no_host_available must render as an info message, not an error/warning toast, got: %s", body)
	}
}

// --- handleWorkshopCacheVerify -----------------------------------------------------

func postWorkshopCacheVerify(t *testing.T, app *App, addonID, cacheEntryID string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"addon_id": {addonID}, "cache_entry_id": {cacheEntryID}}
	req := httptest.NewRequest(http.MethodPost, "/workshop/cache/verify", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleWorkshopCacheVerify(w, req)
	return w
}

// TestHandleWorkshopCacheVerify_PostsExactCacheEntryID proves the verify action posts
// exactly the cache_entry_id the form carried (not the addon_id, and not some other
// value), with server_id == 0 so control-api picks a host that holds a copy (FR11).
func TestHandleWorkshopCacheVerify_PostsExactCacheEntryID(t *testing.T) {
	workshop := &fakeWorkshopCacheClient{
		verifyResp: &manmanpb.VerifyCacheEntryResponse{Dispatched: true, ServerId: 5, Status: "dispatched"},
	}
	app := newWorkshopCacheTestApp(workshop)

	w := postWorkshopCacheVerify(t, app, "42", "7")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	require.Len(t, workshop.verifyCalls, 1)
	if workshop.verifyCalls[0].CacheEntryId != 7 {
		t.Errorf("CacheEntryId = %d, want 7", workshop.verifyCalls[0].CacheEntryId)
	}
	if workshop.verifyCalls[0].ServerId != 0 {
		t.Errorf("ServerId = %d, want 0 (let control-api choose)", workshop.verifyCalls[0].ServerId)
	}

	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "addon_id=42") {
		t.Errorf("redirect Location = %q, want it to carry addon_id=42", loc)
	}
	if !strings.Contains(loc, "verify_status=dispatched") || !strings.Contains(loc, "verify_server_id=5") {
		t.Errorf("redirect Location = %q, want verify_status=dispatched&verify_server_id=5", loc)
	}
}

// TestHandleWorkshopCacheVerify_NoHostAvailable_RedirectsWithStatus proves the
// no_host_available outcome redirects back with that exact status, never as an error.
func TestHandleWorkshopCacheVerify_NoHostAvailable_RedirectsWithStatus(t *testing.T) {
	workshop := &fakeWorkshopCacheClient{
		verifyResp: &manmanpb.VerifyCacheEntryResponse{Dispatched: false, Status: "no_host_available"},
	}
	app := newWorkshopCacheTestApp(workshop)

	w := postWorkshopCacheVerify(t, app, "42", "7")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "verify_status=no_host_available") {
		t.Errorf("redirect Location = %q, want verify_status=no_host_available", loc)
	}
}

// TestHandleWorkshopCacheVerify_RPCError_RedirectsWithErrorStatus proves an RPC-level
// failure redirects with verify_status=error rather than failing the request outright.
func TestHandleWorkshopCacheVerify_RPCError_RedirectsWithErrorStatus(t *testing.T) {
	workshop := &fakeWorkshopCacheClient{verifyErr: assert.AnError}
	app := newWorkshopCacheTestApp(workshop)

	w := postWorkshopCacheVerify(t, app, "42", "7")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusSeeOther, w.Body.String())
	}
	loc := w.Result().Header.Get("Location")
	if !strings.Contains(loc, "verify_status=error") {
		t.Errorf("redirect Location = %q, want verify_status=error", loc)
	}
}

// TestHandleWorkshopCacheVerify_InvalidCacheEntryID_BadRequest guards the request
// validation half of the handler.
func TestHandleWorkshopCacheVerify_InvalidCacheEntryID_BadRequest(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	w := postWorkshopCacheVerify(t, app, "42", "not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for an invalid cache_entry_id", w.Code, http.StatusBadRequest)
	}
}

// TestHandleWorkshopCacheVerify_GetMethod_MethodNotAllowed proves the action is
// POST-only, matching every other mutating form action in this UI.
func TestHandleWorkshopCacheVerify_GetMethod_MethodNotAllowed(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	req := httptest.NewRequest(http.MethodGet, "/workshop/cache/verify", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCacheVerify(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d for a GET request", w.Code, http.StatusMethodNotAllowed)
	}
}
