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
