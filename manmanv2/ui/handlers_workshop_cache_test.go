package main

import (
	"context"
	"errors"
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
// the batch-status view (no SSE/polling/hx-trigger). It also guards #2186's
// on-demand verify dispatch and #2187's manual single-entry eviction, both
// reached from this same view.
//
// fakeWorkshopCacheClient embeds the nil WorkshopServiceClient interface
// and overrides GetAddon, ListAddonCacheEntries, VerifyCacheEntry, and
// EvictCacheEntry -- the RPCs handleWorkshopCache's, handleWorkshopCacheVerify's,
// and handleWorkshopCacheEvict's call graphs reach -- per handlers_sgc_test.go's
// fakeWorkshopServiceClient / handlers_workshop_batch_status_test.go's
// fakeBatchStatusWorkshopClient pattern. Any other call panics on the nil
// embedded interface, which is deliberate.
//
// verifyCalls records every VerifyCacheEntry request so tests can assert the
// exact cache_entry_id/server_id posted. evictedIDs records every
// cache_entry_id EvictCacheEntry was called with, and ListAddonCacheEntries
// filters entries against it -- together these let
// TestHandleWorkshopCacheEvict_PostEvictRender_OmitsOnlyEvictedRow simulate
// the "reloaded view" the issue describes without a real backend: evict,
// then GET again, and see the row actually gone.
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

	evictErr   error
	evictCalls []int64
	evictedIDs map[int64]bool
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
	if f.evictedIDs == nil {
		return &manmanpb.ListAddonCacheEntriesResponse{Entries: f.entries}, nil
	}
	var remaining []*manmanpb.WorkshopCacheEntry
	for _, e := range f.entries {
		if !f.evictedIDs[e.CacheEntryId] {
			remaining = append(remaining, e)
		}
	}
	return &manmanpb.ListAddonCacheEntriesResponse{Entries: remaining}, nil
}

func (f *fakeWorkshopCacheClient) VerifyCacheEntry(ctx context.Context, in *manmanpb.VerifyCacheEntryRequest, opts ...grpc.CallOption) (*manmanpb.VerifyCacheEntryResponse, error) {
	f.verifyCalls = append(f.verifyCalls, in)
	if f.verifyErr != nil {
		return nil, f.verifyErr
	}
	return f.verifyResp, nil
}

func (f *fakeWorkshopCacheClient) EvictCacheEntry(ctx context.Context, in *manmanpb.EvictCacheEntryRequest, opts ...grpc.CallOption) (*manmanpb.EvictCacheEntryResponse, error) {
	f.evictCalls = append(f.evictCalls, in.CacheEntryId)
	if f.evictErr != nil {
		return nil, f.evictErr
	}
	if f.evictedIDs != nil {
		f.evictedIDs[in.CacheEntryId] = true
	}
	return &manmanpb.EvictCacheEntryResponse{Evicted: true}, nil
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
	// Both are distinct content addresses -- the page must render each in its
	// own row, not collapse them into a single row. Each version's exact
	// string now also appears a second time in that row's evict confirmation
	// (#2187, FR12: the confirmation names the specific content version), so
	// this checks the two versions render the same number of times as each
	// other -- proof neither is missing or duplicated relative to the
	// other -- rather than asserting a specific total.
	v1Count := strings.Count(body, "v1-content-address")
	v2Count := strings.Count(body, "v2-content-address")
	if v1Count == 0 || v2Count == 0 {
		t.Fatalf("expected both content addresses rendered at least once, got v1=%d v2=%d in: %s", v1Count, v2Count, body)
	}
	if v1Count != v2Count {
		t.Errorf("v1-content-address occurs %d times, v2-content-address occurs %d times, want them equal (neither collapsed nor duplicated relative to the other)", v1Count, v2Count)
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

func TestHandleWorkshopCache_RendersEvictConfirmationNamingContentVersion(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{CacheEntryId: 1, WorkshopId: "123456", ContentVersion: "v1-content-address", CreatedAt: 1700000100},
	}
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{addon: addon, entries: entries})

	body := renderWorkshopCache(t, app, "42")

	if !strings.Contains(body, "Evict v1-content-address") {
		t.Errorf("expected the confirmation to name the content version 'v1-content-address', got: %s", body)
	}
	if !strings.Contains(body, `action="/workshop/cache/evict"`) {
		t.Errorf("expected an evict form posting to /workshop/cache/evict, got: %s", body)
	}
	if !strings.Contains(body, `name="cache_entry_id" value="1"`) {
		t.Errorf("expected the evict form to carry the exact cache_entry_id, got: %s", body)
	}
}

func postWorkshopCacheEvict(t *testing.T, app *App, addonID, cacheEntryID string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"addon_id": {addonID}, "cache_entry_id": {cacheEntryID}}
	req := httptest.NewRequest(http.MethodPost, "/workshop/cache/evict", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleWorkshopCacheEvict(w, req)
	return w
}

// TestHandleWorkshopCacheEvict_PostsRightEntryID_RedirectsToCacheView proves
// the evict handler forwards the exact cache_entry_id from the form to the
// EvictCacheEntry RPC (never some other id from the request), and redirects
// back to the cache view for the same addon.
func TestHandleWorkshopCacheEvict_PostsRightEntryID_RedirectsToCacheView(t *testing.T) {
	client := &fakeWorkshopCacheClient{evictedIDs: map[int64]bool{}}
	app := newWorkshopCacheTestApp(client)

	w := postWorkshopCacheEvict(t, app, "42", "7")

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if got := w.Header().Get("Location"); got != "/workshop/cache?addon_id=42" {
		t.Errorf("Location = %q, want %q", got, "/workshop/cache?addon_id=42")
	}
	if len(client.evictCalls) != 1 || client.evictCalls[0] != 7 {
		t.Errorf("evictCalls = %v, want exactly [7]", client.evictCalls)
	}
}

// TestHandleWorkshopCacheEvict_PostEvictRender_OmitsOnlyEvictedRow is the
// end-to-end UI regression the issue names: after eviction, the row
// disappears on the reloaded view while every other version remains listed.
func TestHandleWorkshopCacheEvict_PostEvictRender_OmitsOnlyEvictedRow(t *testing.T) {
	addon := &manmanpb.WorkshopAddon{AddonId: 42, Name: "Test Addon"}
	entries := []*manmanpb.WorkshopCacheEntry{
		{CacheEntryId: 1, WorkshopId: "123456", ContentVersion: "v1-content-address", CreatedAt: 1700000100},
		{CacheEntryId: 2, WorkshopId: "123456", ContentVersion: "v2-content-address", CreatedAt: 1700000200},
	}
	client := &fakeWorkshopCacheClient{addon: addon, entries: entries, evictedIDs: map[int64]bool{}}
	app := newWorkshopCacheTestApp(client)

	before := renderWorkshopCache(t, app, "42")
	if !strings.Contains(before, "v1-content-address") || !strings.Contains(before, "v2-content-address") {
		t.Fatalf("expected both versions before eviction, got: %s", before)
	}

	w := postWorkshopCacheEvict(t, app, "42", "1")
	if w.Code != http.StatusSeeOther {
		t.Fatalf("evict status = %d, want %d", w.Code, http.StatusSeeOther)
	}

	after := renderWorkshopCache(t, app, "42")
	if strings.Contains(after, "v1-content-address") {
		t.Errorf("evicted version 'v1-content-address' still rendered after eviction: %s", after)
	}
	if !strings.Contains(after, "v2-content-address") {
		t.Errorf("sibling version 'v2-content-address' missing after evicting a different entry: %s", after)
	}
}

func TestHandleWorkshopCacheEvict_MethodNotAllowed(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	req := httptest.NewRequest(http.MethodGet, "/workshop/cache/evict", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopCacheEvict(w, req)
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d for a non-POST request", w.Code, http.StatusMethodNotAllowed)
	}
}

func TestHandleWorkshopCacheEvict_InvalidAddonID_BadRequest(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	w := postWorkshopCacheEvict(t, app, "not-a-number", "1")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for an invalid addon_id", w.Code, http.StatusBadRequest)
	}
}

func TestHandleWorkshopCacheEvict_InvalidCacheEntryID_BadRequest(t *testing.T) {
	app := newWorkshopCacheTestApp(&fakeWorkshopCacheClient{})
	w := postWorkshopCacheEvict(t, app, "42", "not-a-number")
	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d for an invalid cache_entry_id", w.Code, http.StatusBadRequest)
	}
}

func TestHandleWorkshopCacheEvict_RPCFailure_InternalServerError(t *testing.T) {
	client := &fakeWorkshopCacheClient{evictErr: errors.New("simulated RPC failure")}
	app := newWorkshopCacheTestApp(client)
	w := postWorkshopCacheEvict(t, app, "42", "1")
	if w.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d when the RPC fails", w.Code, http.StatusInternalServerError)
	}
}
