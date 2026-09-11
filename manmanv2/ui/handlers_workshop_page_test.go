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

// This file guards task #2362 (root plan #2359, M6): the redesigned
// Workshop top-level page at "/workshop" -- library management,
// collection-add (C33/FR7), batch-addon-create (C34/FR7), and
// cache-backed install (C35/FR7) all reachable from one page -- plus
// NFR6 (the pre-existing "/workshop/library" page and its sub-routes stay
// reachable and functional) and NFR2 (no "SGC"/"server game config"
// terminology leaking into the page).
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// changing workshopCollectionAddForm's button data-open-blade-template
// attribute name so it no longer matched
// "workshop-collection-add-blade-template" made
// TestHandleWorkshopPage_ExposesControlsForEachFR7Endpoint's collection-add
// subcase fail on the form-action assertion; reverting restored green.
// Likewise, temporarily hardcoding a literal "SGC" string into
// WorkshopPage's hero subtitle made
// TestHandleWorkshopPage_NoSGCTerminology fail; reverting restored green.

// fakeWorkshopPageClient embeds the nil WorkshopServiceClient interface and
// overrides only the RPCs handleWorkshopPage's (and handleWorkshopLibrary's)
// call graphs reach: ListAddons, ListLibraries, ListBatchJobs, the
// per-library pair buildWorkshopLibraryPanels uses (GetLibraryAddons,
// GetChildLibraries), and ListLibraryMigrationConflicts (task #2368's
// UnresolvedConflictCount banner, reached by every handleWorkshopPage
// call). Any other call panics on the nil embedded interface, same pattern
// as handlers_workshop_bulk_test.go's fakeBulkWorkshopClient.
type fakeWorkshopPageClient struct {
	manmanpb.WorkshopServiceClient

	addons    []*manmanpb.WorkshopAddon
	libraries []*manmanpb.WorkshopLibrary
	jobs      []*manmanpb.WorkshopBatchJob

	// libraryAddons/childLibraries key by library_id, so
	// buildWorkshopLibraryPanels' per-library GetLibraryAddons/
	// GetChildLibraries calls can return different data per panel --
	// needed to exercise all four of workshopLibraryPanel's forms
	// (add/remove addon, add/remove reference) across the fixture's two
	// libraries (see buildFakeWorkshopPageData).
	libraryAddons  map[int64][]*manmanpb.WorkshopAddon
	childLibraries map[int64][]*manmanpb.WorkshopLibrary
}

func (f *fakeWorkshopPageClient) ListAddons(ctx context.Context, in *manmanpb.ListAddonsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonsResponse, error) {
	return &manmanpb.ListAddonsResponse{Addons: f.addons}, nil
}

func (f *fakeWorkshopPageClient) ListLibraries(ctx context.Context, in *manmanpb.ListLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListLibrariesResponse, error) {
	return &manmanpb.ListLibrariesResponse{Libraries: f.libraries}, nil
}

func (f *fakeWorkshopPageClient) ListBatchJobs(ctx context.Context, in *manmanpb.ListBatchJobsRequest, opts ...grpc.CallOption) (*manmanpb.ListBatchJobsResponse, error) {
	return &manmanpb.ListBatchJobsResponse{Jobs: f.jobs}, nil
}

func (f *fakeWorkshopPageClient) GetLibraryAddons(ctx context.Context, in *manmanpb.GetLibraryAddonsRequest, opts ...grpc.CallOption) (*manmanpb.GetLibraryAddonsResponse, error) {
	return &manmanpb.GetLibraryAddonsResponse{Addons: f.libraryAddons[in.LibraryId]}, nil
}

func (f *fakeWorkshopPageClient) GetChildLibraries(ctx context.Context, in *manmanpb.GetChildLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.GetChildLibrariesResponse, error) {
	return &manmanpb.GetChildLibrariesResponse{Libraries: f.childLibraries[in.LibraryId]}, nil
}

func (f *fakeWorkshopPageClient) ListLibraryMigrationConflicts(ctx context.Context, in *manmanpb.ListLibraryMigrationConflictsRequest, opts ...grpc.CallOption) (*manmanpb.ListLibraryMigrationConflictsResponse, error) {
	return &manmanpb.ListLibraryMigrationConflictsResponse{}, nil
}

// fakeWorkshopPageAPIClient embeds the nil ManManAPIClient interface and
// overrides only ListGames and ListServers -- ListGames is fetched
// directly by both handlers, and ListServers is reached indirectly via
// buildTemplLayoutData, which every full-page render goes through.
type fakeWorkshopPageAPIClient struct {
	manmanpb.ManManAPIClient

	games []*manmanpb.Game
}

func (f *fakeWorkshopPageAPIClient) ListGames(ctx context.Context, in *manmanpb.ListGamesRequest, opts ...grpc.CallOption) (*manmanpb.ListGamesResponse, error) {
	return &manmanpb.ListGamesResponse{Games: f.games}, nil
}

func (f *fakeWorkshopPageAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{}, nil
}

func newWorkshopPageTestApp(workshop *fakeWorkshopPageClient, api *fakeWorkshopPageAPIClient) *App {
	return &App{
		grpc: &ControlClient{api: api, workshop: workshop},
	}
}

// buildFakeWorkshopPageData fixtures two libraries in the same game so
// every one of workshopLibraryPanel's forms has something to render:
// library 2 ("Essential Maps") already contains addon 10 (so
// remove-addon-from-library renders) with addon 11 available to add (so
// add-addon-to-library renders) and includes library 3 as a child (so
// remove-library-reference renders); library 3 ("Bonus Maps") has no
// children of its own, so library 2 shows up as available for it to
// include (so add-library-reference renders).
func buildFakeWorkshopPageData() (*fakeWorkshopPageClient, *fakeWorkshopPageAPIClient) {
	api := &fakeWorkshopPageAPIClient{
		games: []*manmanpb.Game{{GameId: 1, Name: "Example Game"}},
	}
	lib2 := &manmanpb.WorkshopLibrary{LibraryId: 2, GameId: 1, Name: "Essential Maps"}
	lib3 := &manmanpb.WorkshopLibrary{LibraryId: 3, GameId: 1, Name: "Bonus Maps"}
	addon10 := &manmanpb.WorkshopAddon{AddonId: 10, GameId: 1, Name: "Essential Map"}
	addon11 := &manmanpb.WorkshopAddon{AddonId: 11, GameId: 1, Name: "Extra Map"}
	workshop := &fakeWorkshopPageClient{
		addons:    []*manmanpb.WorkshopAddon{addon10, addon11},
		libraries: []*manmanpb.WorkshopLibrary{lib2, lib3},
		jobs: []*manmanpb.WorkshopBatchJob{
			{BatchJobId: 55, GameId: 1, JobType: "collection_add", Status: "completed", TotalItems: 3, SucceededItems: 3},
		},
		libraryAddons: map[int64][]*manmanpb.WorkshopAddon{
			2: {addon10},
		},
		childLibraries: map[int64][]*manmanpb.WorkshopLibrary{
			2: {lib3},
		},
	}
	return workshop, api
}

func renderWorkshopPageHTTP(t *testing.T, workshop *fakeWorkshopPageClient, api *fakeWorkshopPageAPIClient) (int, string) {
	t.Helper()
	app := newWorkshopPageTestApp(workshop, api)
	req := httptest.NewRequest(http.MethodGet, "/workshop", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopPage(w, req)
	return w.Code, w.Body.String()
}

// --- 200 for an authed user (criterion 1) -----------------------------------

func TestHandleWorkshopPage_RendersOKForAuthedUser(t *testing.T) {
	workshop, api := buildFakeWorkshopPageData()
	code, body := renderWorkshopPageHTTP(t, workshop, api)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Essential Maps") {
		t.Errorf("expected the fetched library to appear in the rendered page, got: %s", body)
	}
}

// Unauthenticated/redirect behavior for "/workshop" is covered exhaustively
// by main_test.go's TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated
// route table (manmanv2RouteTable), which "/workshop" has been added to --
// that test exercises the real RequireAuthFunc/WithAccessToken session-check
// path this handler is registered behind (main.go's setupRoutes), rather
// than a stand-in reimplemented here.

// --- FR7: each fleet-scale action is reachable/executable from the page
// (criterion 2) ---------------------------------------------------------

func TestHandleWorkshopPage_ExposesControlsForEachFR7Endpoint(t *testing.T) {
	workshop, api := buildFakeWorkshopPageData()
	code, body := renderWorkshopPageHTTP(t, workshop, api)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	// Collection-add (C33): form posts straight to the existing
	// bulk-add-collection handler, opened via the pre-rendered Blade
	// template rather than a navigation.
	if !strings.Contains(body, `action="/workshop/bulk-add-collection"`) {
		t.Errorf("expected a form targeting /workshop/bulk-add-collection, got: %s", body)
	}
	if !strings.Contains(body, `data-open-blade-template="workshop-collection-add-blade-template"`) {
		t.Errorf("expected a control opening the collection-add Blade, got: %s", body)
	}

	// Batch-addon-create (C34): same shape, batch-create-addons handler.
	if !strings.Contains(body, `action="/workshop/batch-create-addons"`) {
		t.Errorf("expected a form targeting /workshop/batch-create-addons, got: %s", body)
	}
	if !strings.Contains(body, `data-open-blade-template="workshop-batch-create-blade-template"`) {
		t.Errorf("expected a control opening the batch-create Blade, got: %s", body)
	}

	// Cache-backed install (C35): View Cache control hx-gets the cache
	// Blade in place, no navigation to a separate page.
	if !strings.Contains(body, `hx-get="/workshop/cache-blade"`) {
		t.Errorf("expected a control targeting /workshop/cache-blade, got: %s", body)
	}
}

// --- Library management reachable from the page (criterion 3) --------------

func TestHandleWorkshopPage_ExposesLibraryManagementControls(t *testing.T) {
	workshop, api := buildFakeWorkshopPageData()
	code, body := renderWorkshopPageHTTP(t, workshop, api)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, action := range []string{
		"/workshop/create-library",
		"/workshop/update-library",
		"/workshop/delete-library",
		"/workshop/add-addon-to-library",
		"/workshop/remove-addon-from-library",
		"/workshop/add-library-reference",
		"/workshop/remove-library-reference",
	} {
		if !strings.Contains(body, `action="`+action+`"`) {
			t.Errorf("expected a form targeting %s, got: %s", action, body)
		}
	}
}

// --- NFR2: no "SGC"/"server game config" terminology (criterion 4) ---------
//
// Same assertion shape as handlers_games_test.go's
// TestHandleGames_FR2_NoSGCTerminology (M5's precedent for this check).
func TestHandleWorkshopPage_NoSGCTerminology(t *testing.T) {
	workshop, api := buildFakeWorkshopPageData()
	code, body := renderWorkshopPageHTTP(t, workshop, api)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("rendered Workshop page contains %q (NFR2 forbids SGC terminology in display text)", "sgc")
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf("rendered Workshop page contains %q (NFR2 forbids raw entity terminology in display text)", "server game config")
	}
}

// Note: "/workshop/library" retired to a redirect onto "/workshop" by task
// #2372 (M6 navigation/disposition, FR16) -- see handlers_redirects_test.go
// for that coverage. The NFR6 "still works" guard this file previously
// carried (TestHandleWorkshopLibrary_StillRendersOK) no longer applies now
// that this task deliberately changed that behavior.
