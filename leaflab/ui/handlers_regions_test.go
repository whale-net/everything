package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	leaflabapipb "github.com/whale-net/everything/leaflab/api/proto"
)

// --- region tree view + drill-down (#2317: FR6) -----------------------------

// regionNodeFixture builds one RegionTreeNode: region_id, name, both sensor
// counts ("here only" / "here or below"), and any children. Names are
// chosen so every fixture tree is already in alphabetical sibling order --
// the order the API guarantees (api.proto: "children are ordered
// alphabetically by name") and the regions page renders verbatim.
func regionNodeFixture(regionID int64, name string, sensorCount, inclusive int64, children ...*leaflabapipb.RegionTreeNode) *leaflabapipb.RegionTreeNode {
	return &leaflabapipb.RegionTreeNode{
		RegionId:             regionID,
		Name:                 name,
		SensorCount:          sensorCount,
		InclusiveSensorCount: inclusive,
		Children:             children,
	}
}

// regionForestFixture is the tree the regions tests render: two top-level
// regions, the first with two children. Counts are chosen so "here only"
// and "here or below" are distinguishable per node (e.g. Attic: 0 here but
// 2 here or below) and across levels.
func regionForestFixture() []*leaflabapipb.RegionTreeNode {
	return []*leaflabapipb.RegionTreeNode{
		regionNodeFixture(1, "Attic", 0, 2,
			regionNodeFixture(2, "Shelf A", 1, 1),
			regionNodeFixture(3, "Shelf B", 1, 1),
		),
		regionNodeFixture(4, "Basement", 3, 3),
	}
}

// newRegionsGetRequest builds a "GET /regions" (or "/regions/{id}") request;
// target may carry a query (e.g. the ?error= parameter a failed
// create/rename/re-parent POST redirects back with).
func newRegionsGetRequest(target string) *http.Request {
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// newCreateRegionRequest builds a "POST /regions/create" request the way
// createRegionForm submits it: form-encoded name/parent_region_id/next.
// parentRegionID "0" is the picker's Top-level default; an empty value
// (picker field absent) must parse the same way (parseOptionalRegionID).
func newCreateRegionRequest(name, parentRegionID, next string) *http.Request {
	form := url.Values{}
	form.Set("name", name)
	form.Set("parent_region_id", parentRegionID)
	form.Set("next", next)
	req := httptest.NewRequest(http.MethodPost, "/regions/create", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return req
}

// newRegionDetailGetRequest builds a "GET /regions/{region_id}" request the
// way the route mux would populate it -- handleRegionDetail parses
// r.PathValue("region_id"), which only exists when the path value is set
// (httptest.NewRequest alone does not populate it).
func newRegionDetailGetRequest(regionID string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/regions/"+regionID, nil)
	req.SetPathValue("region_id", regionID)
	return req
}

// newRenameRegionRequest builds a "POST /regions/{region_id}/rename" request
// the way regionRenameDetails submits it.
func newRenameRegionRequest(regionID, name, next string) *http.Request {
	form := url.Values{}
	form.Set("name", name)
	form.Set("next", next)
	req := httptest.NewRequest(http.MethodPost, "/regions/"+regionID+"/rename", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("region_id", regionID)
	return req
}

// newReparentRegionRequest builds a "POST /regions/{region_id}/reparent"
// request the way regionReparentDetails submits it (parent_region_id "0" =
// the picker's "Top-level" option).
func newReparentRegionRequest(regionID, parentRegionID, next string) *http.Request {
	form := url.Values{}
	form.Set("parent_region_id", parentRegionID)
	form.Set("next", next)
	req := httptest.NewRequest(http.MethodPost, "/regions/"+regionID+"/reparent", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("region_id", regionID)
	return req
}

// TestHandleRegions_RendersForestWithCountsAndControls is FR6's UI half:
// the top-level view renders the GetRegionTree result verbatim -- every
// region's name as a drill-down link, both per-region sensor counts ("N
// here" / "N here or below", current placement), children nested, and
// siblings in the order the API returned (alphabetical is the API's FR6
// guarantee -- the UI deliberately re-orders nothing, so the fixture is
// alphabetical and the test asserts the rendered order matches it). Each
// node also carries its per-region rename and re-parent form (FR2, FR3).
func TestHandleRegions_RendersForestWithCountsAndControls(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: regionForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()

	for _, want := range []string{"Attic", "Shelf A", "Shelf B", "Basement"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected region %q in the rendered tree, got %s", want, body)
		}
	}
	// Sibling order is rendered as the API returned it (Attic before
	// Basement at the top level, Shelf A before Shelf B below).
	if strings.Index(body, "Attic") > strings.Index(body, "Basement") {
		t.Errorf("expected Attic to render before Basement (API's alphabetical order), got %s", body)
	}
	if strings.Index(body, "Shelf A") > strings.Index(body, "Shelf B") {
		t.Errorf("expected Shelf A to render before Shelf B (API's alphabetical order), got %s", body)
	}

	// Both per-region sensor counts, verbatim ("N here</span>" pins the
	// here-only count so it cannot match the "N here or below" span).
	for _, want := range []string{
		"0 here</span>", "2 here or below</span>", // Attic
		"1 here</span>", "1 here or below</span>", // Shelf A / Shelf B
		"3 here</span>", "3 here or below</span>", // Basement
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected sensor count %q in the rendered tree, got %s", want, body)
		}
	}

	// Drill-down links per node (FR6).
	for _, want := range []string{`href="/regions/1"`, `href="/regions/2"`, `href="/regions/3"`, `href="/regions/4"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected drill-down link %q in the rendered tree, got %s", want, body)
		}
	}

	// Per-node rename (FR2) and re-parent (FR3) forms.
	if !strings.Contains(body, `action="/regions/4/rename"`) {
		t.Errorf("expected Basement's rename form in the rendered tree, got %s", body)
	}
	if !strings.Contains(body, `action="/regions/3/reparent"`) {
		t.Errorf("expected Shelf B's re-parent form in the rendered tree, got %s", body)
	}
	if strings.Contains(body, "Failed to load regions") {
		t.Errorf("expected no load-error banner on a successful tree fetch, got %s", body)
	}
}

// TestHandleRegions_PickersOfferEveryRegionInForest is FR3/FR5's deliberate
// posture made visible: the create parent-picker and the per-region
// re-parent pickers flatten the whole forest -- every region appears as an
// option, including children (a re-parent target may be any region) -- plus
// the "Top-level" option that makes "make top-level" (FR3) reachable. FR5's
// cycle rejection is then the API's FailedPrecondition, not a UI-side
// option filter.
func TestHandleRegions_PickersOfferEveryRegionInForest(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: regionForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()

	if !strings.Contains(body, `<option value="0">Top-level</option>`) {
		t.Errorf("expected the Top-level option in the pickers, got %s", body)
	}
	for _, want := range []string{
		`<option value="1">Attic</option>`,
		`<option value="2">Shelf A</option>`,
		`<option value="3">Shelf B</option>`,
		`<option value="4">Basement</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected picker option %q, got %s", want, body)
		}
	}
}

// TestHandleRegions_EmptyForest_RendersEmptyStateAndCreateForm covers a
// fresh owner's first visit: no regions yet renders the empty-state card
// (not an error) and the create form below it (FR1 needs no tree data).
func TestHandleRegions_EmptyForest_RendersEmptyStateAndCreateForm(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: nil},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, "No regions yet. Create your first one below.") {
		t.Errorf("expected the empty-state message, got %s", body)
	}
	if !strings.Contains(body, `action="/regions/create"`) {
		t.Errorf("expected the create form below the empty state, got %s", body)
	}
	if strings.Contains(body, "Failed to load regions") {
		t.Errorf("expected no load-error banner on an empty-but-successful fetch, got %s", body)
	}
}

// TestHandleRegions_LoadError_BannerNot500_CreateFormStillPresent proves a
// failed GetRegionTree renders the loadErr banner (boards.templ's shape)
// instead of the tree -- a 200 with a real error state, never a 500 -- and
// that the create form survives (top-level creation needs no tree data).
func TestHandleRegions_LoadError_BannerNot500_CreateFormStillPresent(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeErrByRoot: map[int64]error{
			0: status.Error(codes.Internal, "db connection refused"),
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (error banner, not a 500)", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "Failed to load regions from leaflab-api") {
		t.Errorf("expected the load-error banner, got %s", body)
	}
	if !strings.Contains(body, "db connection refused") {
		t.Errorf("expected the underlying error surfaced in the banner, got %s", body)
	}
	if !strings.Contains(body, `action="/regions/create"`) {
		t.Errorf("expected the create form to still render during a load failure, got %s", body)
	}
	if strings.Contains(body, "No regions yet") {
		t.Errorf("expected the load error to suppress the empty-state card, got %s", body)
	}
}

// TestHandleRegions_ActionErrorBanner_ShowsApiMessage_TreeStillRenders is
// the POST-redirect-GET landing a rejected write arrives on (FR5): the
// ?error= parameter renders as the action-error banner with the API's own
// message, and the re-fetched tree below it renders the still-valid
// structure -- a refused re-parent cannot corrupt the view.
func TestHandleRegions_ActionErrorBanner_ShowsApiMessage_TreeStillRenders(t *testing.T) {
	cycleMsg := "region 1 cannot be re-parented under region 3: the new parent is the region itself or one of its descendants"
	fake := &fakeLeafLabAPIClient{
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			0: {Regions: regionForestFixture()},
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions?error="+url.QueryEscape(cycleMsg)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, cycleMsg) {
		t.Errorf("expected the action-error banner to show the API's cycle message, got %s", body)
	}
	if !strings.Contains(body, `href="/regions/1"`) || !strings.Contains(body, "Attic") {
		t.Errorf("expected the tree to still render below the error banner, got %s", body)
	}
	if strings.Contains(body, "Failed to load regions") {
		t.Errorf("a write failure must not be rendered as a load failure, got %s", body)
	}
}

// TestHandleRegions_Unauthenticated_RedirectsToLogin mirrors
// TestHandleBoards_Unauthenticated_RedirectsToLogin for the regions screen:
// a revoked token is a re-authenticate flow (303 to /auth/login with the
// request URI as next), not an error banner. The shared
// redirectToLoginOnUnauthenticated escapes the next value, so the assertion
// pins the escaped form.
func TestHandleRegions_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeErrByRoot: map[int64]error{
			0: status.Error(codes.Unauthenticated, "token revoked"),
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegions(rec, newRegionsGetRequest("/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fregions"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// TestHandleRegionDetail_RendersSubtreeWithCountsAndForestPickers is FR6's
// drill-down: GetRegionTree with the drilled region as root renders that
// subtree alone (its descendants' names, links, and both counts) while the
// pickers still offer every region from the second, full-forest fetch --
// including regions entirely outside the drilled subtree.
func TestHandleRegionDetail_RendersSubtreeWithCountsAndForestPickers(t *testing.T) {
	forest := regionForestFixture()
	fake := &fakeLeafLabAPIClient{
		regionTreeByRoot: map[int64]*leaflabapipb.GetRegionTreeResponse{
			1: {Regions: forest[:1]}, // drilled root 1: the Attic subtree alone
			0: {Regions: forest},     // full forest, for the pickers
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegionDetail(rec, newRegionDetailGetRequest("1"))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", rec.Code, http.StatusOK, rec.Body.String())
	}
	body := rec.Body.String()

	// Subtree rendered with names, drill links, and both counts.
	for _, want := range []string{
		"Attic", "Shelf A", "Shelf B",
		`href="/regions/2"`,
		"0 here</span>", "2 here or below</span>", // Attic
		"1 here</span>", // Shelf A
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected %q in the drilled subtree view, got %s", want, body)
		}
	}
	// Basement is outside the subtree: no tree link and no counts for it
	// (it may only appear as a picker option from the forest fetch).
	if strings.Contains(body, `href="/regions/4"`) {
		t.Errorf("expected Basement to be absent from the subtree view, got %s", body)
	}
	if strings.Contains(body, "3 here") {
		t.Errorf("expected Basement's counts to be absent from the subtree view, got %s", body)
	}
	// Pickers carry every region, including the out-of-subtree one.
	if !strings.Contains(body, `<option value="4">Basement</option>`) {
		t.Errorf("expected the re-parent/create pickers to offer Basement (outside the subtree), got %s", body)
	}
	// The drill-down affordance: the back link to the top-level view.
	if !strings.Contains(body, "← All regions") {
		t.Errorf("expected the back-to-all-regions link on a drill-down view, got %s", body)
	}
	// The handler fetched the drilled subtree first, then the full forest
	// for the pickers -- in that order.
	if got := fake.regionTreeRoots; len(got) != 2 || got[0] != 1 || got[1] != 0 {
		t.Errorf("GetRegionTree roots = %v, want [1 0] (subtree, then forest for pickers)", got)
	}
}

// TestHandleRegionDetail_UnknownRegionID_NotFound proves an unknown drilled
// region_id gets a real HTTP 404 (the distinguishable-from-empty treatment
// handleBoardDetail uses), not an error banner on a 200.
func TestHandleRegionDetail_UnknownRegionID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeErrByRoot: map[int64]error{
			99: status.Error(codes.NotFound, "region 99 not found"),
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegionDetail(rec, newRegionDetailGetRequest("99"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

// TestHandleRegionDetail_MalformedRegionID_NotFound proves a non-numeric
// region_id path segment short-circuits to a 404 before any RPC.
func TestHandleRegionDetail_MalformedRegionID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegionDetail(rec, newRegionDetailGetRequest("not-a-number"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if len(fake.regionTreeRoots) != 0 {
		t.Errorf("expected no GetRegionTree calls for a malformed region_id, got roots %v", fake.regionTreeRoots)
	}
}

// TestHandleRegionDetail_Unauthenticated_RedirectsToLogin covers the
// detail view's Unauthenticated branch (same re-authenticate flow as the
// top-level view).
func TestHandleRegionDetail_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		regionTreeErrByRoot: map[int64]error{
			7: status.Error(codes.Unauthenticated, "token revoked"),
		},
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRegionDetail(rec, newRegionDetailGetRequest("7"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fregions%2F7"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// --- create region (#2317: FR1) ---------------------------------------------

// TestHandleCreateRegion_Success_TopLevel_RedirectsWithoutError is FR1's
// happy path: the form's name reaches CreateRegion (parent 0 = top-level,
// the picker's default) and the redirect lands on the submitted-from page
// with no error parameter -- the fresh tree there shows the new region.
func TestHandleCreateRegion_Success_TopLevel_RedirectsWithoutError(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Garage", "0", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions"; got != want {
		t.Fatalf("Location = %q, want %q (no ?error= on success)", got, want)
	}
	if fake.createdRegionReq == nil {
		t.Fatal("expected CreateRegion to be called")
	}
	if got, want := fake.createdRegionReq.Name, "Garage"; got != want {
		t.Errorf("CreateRegion name = %q, want %q", got, want)
	}
	if got, want := fake.createdRegionReq.ParentRegionId, int64(0); got != want {
		t.Errorf("CreateRegion parent_region_id = %d, want %d (top-level)", got, want)
	}
}

// TestHandleCreateRegion_Nested_PassesParentAndNextThrough proves a
// picker-chosen parent reaches CreateRegion and the redirect returns to the
// drill-down page the form was submitted from.
func TestHandleCreateRegion_Nested_PassesParentAndNextThrough(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Seed Tray", "4", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/4"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if fake.createdRegionReq == nil {
		t.Fatal("expected CreateRegion to be called")
	}
	if got, want := fake.createdRegionReq.Name, "Seed Tray"; got != want {
		t.Errorf("CreateRegion name = %q, want %q", got, want)
	}
	if got, want := fake.createdRegionReq.ParentRegionId, int64(4); got != want {
		t.Errorf("CreateRegion parent_region_id = %d, want %d", got, want)
	}
}

// TestHandleCreateRegion_EmptyParentFormValue_CreatesTopLevel covers the
// parseOptionalRegionID empty branch: a form submitted without a
// parent_region_id value (e.g. a hand-rolled client omitting the picker)
// means top-level, same as the picker's "0" option -- not a 400.
func TestHandleCreateRegion_EmptyParentFormValue_CreatesTopLevel(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Garage", "", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if fake.createdRegionReq == nil {
		t.Fatal("expected CreateRegion to be called")
	}
	if got, want := fake.createdRegionReq.ParentRegionId, int64(0); got != want {
		t.Errorf("CreateRegion parent_region_id = %d, want %d (empty picker value = top-level)", got, want)
	}
}

// TestHandleCreateRegion_InvalidArgument_SurfacesApiMessageVerbatim is
// FR1's rejection path: an empty-name rejection (leaflab-api's real
// "name must not be empty" message, server.go) redirects back with the
// API's own message as ?error= -- never a 500, never a silent no-op.
func TestHandleCreateRegion_InvalidArgument_SurfacesApiMessageVerbatim(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		createRegionErr: status.Error(codes.InvalidArgument, "name must not be empty"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("", "0", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions?error=name+must+not+be+empty"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestHandleCreateRegion_UnknownParent_SurfacesApiMessageVerbatim covers
// the second create rejection (unknown parent region_id): the API's
// NotFound message surfaces verbatim on the redirect, same as
// InvalidArgument -- no rejection kind is hidden.
func TestHandleCreateRegion_UnknownParent_SurfacesApiMessageVerbatim(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		createRegionErr: status.Error(codes.NotFound, "region_id must identify an existing region"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Seed Tray", "4", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/4?error=region_id+must+identify+an+existing+region"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestHandleCreateRegion_NonNumericParent_BadRequest proves a tampered
// parent_region_id is a 400 before any RPC, not a create attempt with a
// garbage parent.
func TestHandleCreateRegion_NonNumericParent_BadRequest(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Seed Tray", "not-a-number", "/regions"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if fake.createdRegionReq != nil {
		t.Error("expected no CreateRegion call for a non-numeric parent_region_id")
	}
}

// TestHandleCreateRegion_TamperedNext_FallsBackToRegions proves a tampered
// "next" redirect target cannot bounce the user off-site: only /regions
// paths are honored, anything else falls back to the top-level view.
func TestHandleCreateRegion_TamperedNext_FallsBackToRegions(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Garage", "0", "https://evil.example/steal"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions"; got != want {
		t.Fatalf("Location = %q, want %q (tampered next falls back)", got, want)
	}
}

// TestHandleCreateRegion_Unauthenticated_RedirectsToLogin mirrors the other
// write paths: a revoked token is the re-authenticate flow.
func TestHandleCreateRegion_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		createRegionErr: status.Error(codes.Unauthenticated, "token revoked"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleCreateRegion(rec, newCreateRegionRequest("Garage", "0", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fregions%2Fcreate"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// --- rename region (#2317: FR2) ---------------------------------------------

// TestHandleRenameRegion_Success_RedirectsWithoutError is FR2's happy path
// (forward-looking only): the new name reaches RenameRegion for the right
// region and the redirect lands back on the submitted-from page with no
// error parameter.
func TestHandleRenameRegion_Success_RedirectsWithoutError(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameRegion(rec, newRenameRegionRequest("4", "Utility Room", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/4"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if fake.renamedRegionReq == nil {
		t.Fatal("expected RenameRegion to be called")
	}
	if got, want := fake.renamedRegionReq.RegionId, int64(4); got != want {
		t.Errorf("RenameRegion region_id = %d, want %d", got, want)
	}
	if got, want := fake.renamedRegionReq.Name, "Utility Room"; got != want {
		t.Errorf("RenameRegion name = %q, want %q", got, want)
	}
}

// TestHandleRenameRegion_InvalidArgument_SurfacesApiMessageVerbatim is
// FR2's empty-name rejection: the API's own message rides back as ?error=
// (the pre-filled rename form then re-shows the unchanged current name on
// the re-fetched page).
func TestHandleRenameRegion_InvalidArgument_SurfacesApiMessageVerbatim(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameRegionErr: status.Error(codes.InvalidArgument, "name must not be empty"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameRegion(rec, newRenameRegionRequest("4", "", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/4?error=name+must+not+be+empty"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestHandleRenameRegion_PermissionDenied_SurfacesNonOwnerMessage is NFR2's
// UI half: a non-owner rename is rejected server-side by the API; the UI
// shows its purpose-built message (the raw status names internal user IDs)
// rather than a 500 or the API's internal wording.
func TestHandleRenameRegion_PermissionDenied_SurfacesNonOwnerMessage(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameRegionErr: status.Error(codes.PermissionDenied, "caller does not own region 4"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameRegion(rec, newRenameRegionRequest("4", "Utility Room", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/4?error=You+do+not+own+this+region."; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestHandleRenameRegion_MalformedRegionID_NotFound proves a non-numeric
// region_id path segment short-circuits to a 404 before any RPC.
func TestHandleRenameRegion_MalformedRegionID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameRegion(rec, newRenameRegionRequest("not-a-number", "Utility Room", "/regions"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if fake.renamedRegionReq != nil {
		t.Error("expected no RenameRegion call for a malformed region_id")
	}
}

// TestHandleRenameRegion_Unauthenticated_RedirectsToLogin mirrors the other
// write paths: a revoked token is the re-authenticate flow.
func TestHandleRenameRegion_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		renameRegionErr: status.Error(codes.Unauthenticated, "token revoked"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleRenameRegion(rec, newRenameRegionRequest("4", "Utility Room", "/regions/4"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fregions%2F4%2Frename"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// --- re-parent region (#2317: FR3, FR5) -------------------------------------

// TestHandleReparentRegion_Success_RedirectsWithoutError is FR3's happy
// path: the picker's parent reaches ReparentRegion (the whole subtree moves
// with the region by the API's construction -- the UI touches nothing but
// the redirect) and the redirect lands back on the submitted-from page with
// no error parameter.
func TestHandleReparentRegion_Success_RedirectsWithoutError(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("3", "2", "/regions/1"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions/1"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
	if fake.reparentedRegionReq == nil {
		t.Fatal("expected ReparentRegion to be called")
	}
	if got, want := fake.reparentedRegionReq.RegionId, int64(3); got != want {
		t.Errorf("ReparentRegion region_id = %d, want %d", got, want)
	}
	if got, want := fake.reparentedRegionReq.ParentRegionId, int64(2); got != want {
		t.Errorf("ReparentRegion parent_region_id = %d, want %d", got, want)
	}
}

// TestHandleReparentRegion_ToTopLevel_PassesZeroParent is FR3's "make
// top-level" affordance: the picker's "Top-level" option (value "0")
// reaches ReparentRegion as parent 0, the documented make-top-level value.
func TestHandleReparentRegion_ToTopLevel_PassesZeroParent(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("3", "0", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if fake.reparentedRegionReq == nil {
		t.Fatal("expected ReparentRegion to be called")
	}
	if got, want := fake.reparentedRegionReq.ParentRegionId, int64(0); got != want {
		t.Errorf("ReparentRegion parent_region_id = %d, want %d (top-level)", got, want)
	}
}

// TestHandleReparentRegion_CycleRejected_SurfacesApiMessageVerbatim is FR5's
// core UI requirement: re-parenting into self/descendant is rejected by the
// API as codes.FailedPrecondition, and the UI surfaces the API's own
// explanation verbatim on the redirect -- never a 500, never a
// purpose-built substitute, and the write itself is refused so the
// re-fetched tree this redirect lands on stays consistent (proven from the
// rendering side by TestHandleRegions_ActionErrorBanner_...).
func TestHandleReparentRegion_CycleRejected_SurfacesApiMessageVerbatim(t *testing.T) {
	apiMsg := "region 1 cannot be re-parented under region 3: the new parent is the region itself or one of its descendants"
	fake := &fakeLeafLabAPIClient{
		reparentRegionErr: status.Error(codes.FailedPrecondition, apiMsg),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("1", "3", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	want := "/regions?error=" + url.QueryEscape(apiMsg)
	if got := rec.Header().Get("Location"); got != want {
		t.Fatalf("Location = %q, want %q (the API's cycle message verbatim)", got, want)
	}
	if !strings.Contains(rec.Header().Get("Location"), url.QueryEscape("the new parent is the region itself or one of its descendants")) {
		t.Errorf("expected the API's cycle explanation to survive verbatim, got %q", rec.Header().Get("Location"))
	}
}

// TestHandleReparentRegion_AlreadyTopLevel_SurfacesApiMessage covers the
// API's other no-op refusal the issue's FR5 posture lumps with cycle
// rejection ("region is already top-level", a FailedPrecondition): surfaced
// verbatim, not hidden, not a 500.
func TestHandleReparentRegion_AlreadyTopLevel_SurfacesApiMessage(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		reparentRegionErr: status.Error(codes.FailedPrecondition, "region is already top-level"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("4", "0", "/regions"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect with error, not a 500)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/regions?error=region+is+already+top-level"; got != want {
		t.Fatalf("Location = %q, want %q", got, want)
	}
}

// TestHandleReparentRegion_NonNumericParent_BadRequest proves a tampered
// parent_region_id is a 400 before any RPC.
func TestHandleReparentRegion_NonNumericParent_BadRequest(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("3", "not-a-number", "/regions"))

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	if fake.reparentedRegionReq != nil {
		t.Error("expected no ReparentRegion call for a non-numeric parent_region_id")
	}
}

// TestHandleReparentRegion_MalformedRegionID_NotFound proves a non-numeric
// region_id path segment short-circuits to a 404 before any RPC.
func TestHandleReparentRegion_MalformedRegionID_NotFound(t *testing.T) {
	fake := &fakeLeafLabAPIClient{}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("not-a-number", "2", "/regions"))

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if fake.reparentedRegionReq != nil {
		t.Error("expected no ReparentRegion call for a malformed region_id")
	}
}

// TestHandleReparentRegion_Unauthenticated_RedirectsToLogin mirrors the
// other write paths: a revoked token is the re-authenticate flow.
func TestHandleReparentRegion_Unauthenticated_RedirectsToLogin(t *testing.T) {
	fake := &fakeLeafLabAPIClient{
		reparentRegionErr: status.Error(codes.Unauthenticated, "token revoked"),
	}
	app := &App{api: &LeafLabClient{api: fake}}

	rec := httptest.NewRecorder()
	app.handleReparentRegion(rec, newReparentRegionRequest("3", "2", "/regions/1"))

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (redirect)", rec.Code, http.StatusSeeOther)
	}
	if got, want := rec.Header().Get("Location"), "/auth/login?next=%2Fregions%2F3%2Freparent"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}
