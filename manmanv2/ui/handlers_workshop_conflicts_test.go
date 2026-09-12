package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// This file guards task #2368 (root plan #2359, M6): the FR12/US6
// SGC->GameConfig library migration conflict resolution page and its
// "/workshop/conflicts/resolve" endpoint, covering
// handleResolveLibraryMigrationConflict's full documented decision table
// (handlers_workshop_conflicts.go's doc comment, lines 160-184) plus the
// read path (renderWorkshopLibraryConflicts/buildConflictViews/
// resolveConflictDeploymentName) that names each candidate's library and
// source deployment.

// fakeConflictsWorkshopClient embeds the nil WorkshopServiceClient
// interface and overrides only ListLibraryMigrationConflicts, GetLibrary,
// and ResolveLibraryMigrationConflict -- the calls
// handleWorkshopLibraryConflicts/handleResolveLibraryMigrationConflict's
// call graph reaches. Same pattern as handlers_workshop_page_test.go's
// fakeWorkshopPageClient.
//
// ResolveLibraryMigrationConflict is stateful: on success it removes the
// resolved conflict from the fixture's conflicts slice, so a test can
// assert "a resolved conflict no longer appears in the list" (issue
// #2368's Testing section) by POSTing a resolution and then re-rendering
// the list, rather than asserting on the RPC call alone.
type fakeConflictsWorkshopClient struct {
	manmanpb.WorkshopServiceClient

	conflicts []*manmanpb.WorkshopLibraryMigrationConflict
	libraries map[int64]*manmanpb.WorkshopLibrary

	resolveErr     error
	resolveCount   int
	lastResolveReq *manmanpb.ResolveLibraryMigrationConflictRequest
}

func (f *fakeConflictsWorkshopClient) ListLibraryMigrationConflicts(ctx context.Context, in *manmanpb.ListLibraryMigrationConflictsRequest, opts ...grpc.CallOption) (*manmanpb.ListLibraryMigrationConflictsResponse, error) {
	return &manmanpb.ListLibraryMigrationConflictsResponse{Conflicts: f.conflicts}, nil
}

func (f *fakeConflictsWorkshopClient) GetLibrary(ctx context.Context, in *manmanpb.GetLibraryRequest, opts ...grpc.CallOption) (*manmanpb.GetLibraryResponse, error) {
	lib := f.libraries[in.LibraryId]
	if lib == nil {
		return nil, status.Error(codes.NotFound, "library not found")
	}
	return &manmanpb.GetLibraryResponse{Library: lib}, nil
}

func (f *fakeConflictsWorkshopClient) ResolveLibraryMigrationConflict(ctx context.Context, in *manmanpb.ResolveLibraryMigrationConflictRequest, opts ...grpc.CallOption) (*manmanpb.ResolveLibraryMigrationConflictResponse, error) {
	f.resolveCount++
	f.lastResolveReq = in
	if f.resolveErr != nil {
		return nil, f.resolveErr
	}
	remaining := f.conflicts[:0]
	for _, c := range f.conflicts {
		if c.ConflictId != in.ConflictId {
			remaining = append(remaining, c)
		}
	}
	f.conflicts = remaining
	return &manmanpb.ResolveLibraryMigrationConflictResponse{}, nil
}

// fakeConflictsAPIClient embeds the nil ManManAPIClient interface and
// overrides ListServers (reached via buildTemplLayoutData on every
// full-page render), GetGameConfig/GetGame (buildConflictViews' config/
// game name lookups), and GetServerGameConfig (resolveConflictDeploymentName's
// direct app.grpc.GetAPI() call).
type fakeConflictsAPIClient struct {
	manmanpb.ManManAPIClient

	servers     []*manmanpb.Server
	gameConfigs map[int64]*manmanpb.GameConfig
	games       map[int64]*manmanpb.Game
	sgcs        map[int64]*manmanpb.ServerGameConfig
}

func (f *fakeConflictsAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeConflictsAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	cfg := f.gameConfigs[in.ConfigId]
	if cfg == nil {
		return nil, status.Error(codes.NotFound, "game config not found")
	}
	return &manmanpb.GetGameConfigResponse{Config: cfg}, nil
}

func (f *fakeConflictsAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	game := f.games[in.GameId]
	if game == nil {
		return nil, status.Error(codes.NotFound, "game not found")
	}
	return &manmanpb.GetGameResponse{Game: game}, nil
}

func (f *fakeConflictsAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	sgc := f.sgcs[in.ServerGameConfigId]
	if sgc == nil {
		return nil, status.Error(codes.NotFound, "sgc not found")
	}
	return &manmanpb.GetServerGameConfigResponse{Config: sgc}, nil
}

// buildFakeConflictsData fixtures one conflict (id 7) on GameConfig 100
// ("Config A", game "Example Game") with two candidates: library 10
// ("Lib A") attached via SGC 501 on server 1 ("Server One"), and library
// 11 ("Lib B") attached via SGC 502 on server 2 ("Server Two") -- enough
// to exercise both union (all candidates) and override (pick one)
// resolutions, and to prove each candidate's library and source
// deployment both render (issue #2368's Implementation note).
func buildFakeConflictsData() (*fakeConflictsWorkshopClient, *fakeConflictsAPIClient) {
	workshop := &fakeConflictsWorkshopClient{
		conflicts: []*manmanpb.WorkshopLibraryMigrationConflict{
			{
				ConflictId: 7,
				ConfigId:   100,
				Candidates: []*manmanpb.WorkshopLibraryMigrationConflictCandidate{
					{LibraryId: 10, SgcId: 501},
					{LibraryId: 11, SgcId: 502},
				},
			},
		},
		libraries: map[int64]*manmanpb.WorkshopLibrary{
			10: {LibraryId: 10, GameId: 1, Name: "Lib A"},
			11: {LibraryId: 11, GameId: 1, Name: "Lib B"},
		},
	}
	api := &fakeConflictsAPIClient{
		servers: []*manmanpb.Server{
			{ServerId: 1, Name: "Server One"},
			{ServerId: 2, Name: "Server Two"},
		},
		gameConfigs: map[int64]*manmanpb.GameConfig{
			100: {ConfigId: 100, GameId: 1, Name: "Config A"},
		},
		games: map[int64]*manmanpb.Game{
			1: {GameId: 1, Name: "Example Game"},
		},
		sgcs: map[int64]*manmanpb.ServerGameConfig{
			501: {ServerGameConfigId: 501, ServerId: 1, GameConfigId: 100},
			502: {ServerGameConfigId: 502, ServerId: 2, GameConfigId: 100},
		},
	}
	return workshop, api
}

func newConflictsTestApp(workshop *fakeConflictsWorkshopClient, api *fakeConflictsAPIClient) *App {
	return &App{
		grpc: &ControlClient{api: api, workshop: workshop},
	}
}

func getConflictsListHTTP(app *App) (int, string) {
	req := httptest.NewRequest(http.MethodGet, "/workshop/conflicts", nil)
	w := httptest.NewRecorder()
	app.handleWorkshopLibraryConflicts(w, req)
	return w.Code, w.Body.String()
}

func postResolveHTTP(app *App, form url.Values) (int, string, string) {
	req := httptest.NewRequest(http.MethodPost, "/workshop/conflicts/resolve", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleResolveLibraryMigrationConflict(w, req)
	return w.Code, w.Body.String(), w.Header().Get("Location")
}

// --- Read path: list renders candidates and their source deployments ------

func TestRenderWorkshopLibraryConflicts_NamesEachCandidateAndDeployment(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, body := getConflictsListHTTP(app)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{
		"Config A",
		"Example Game",
		"Lib A",
		"Lib B",
		"Config A on Server One",
		"Config A on Server Two",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("expected rendered conflict list to contain %q, got: %s", want, body)
		}
	}
}

func TestRenderWorkshopLibraryConflicts_NoConflicts(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	workshop.conflicts = nil
	app := newConflictsTestApp(workshop, api)

	code, body := getConflictsListHTTP(app)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "No unresolved conflicts") {
		t.Errorf("expected empty-state copy, got: %s", body)
	}
}

// --- No third resolution option is rendered anywhere -----------------------

func TestRenderWorkshopLibraryConflicts_ExactlyTwoResolutionOptions(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	_, body := getConflictsListHTTP(app)

	if got := strings.Count(body, `name="resolution"`); got != 2 {
		t.Errorf("expected exactly 2 resolution inputs (union + override), got %d in: %s", got, body)
	}
	if !strings.Contains(body, `value="union"`) {
		t.Errorf("expected a resolution=union control, got: %s", body)
	}
	if !strings.Contains(body, `value="override"`) {
		t.Errorf("expected a resolution=override control, got: %s", body)
	}
}

// --- Union: posts resolution=union with no keep_library_id, attaches all --

func TestHandleResolveLibraryMigrationConflict_Union(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	form := url.Values{"conflict_id": {"7"}, "resolution": {"union"}}
	code, body, location := postResolveHTTP(app, form)

	if code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusSeeOther, body)
	}
	if location != "/workshop/conflicts" {
		t.Errorf("Location = %q, want /workshop/conflicts", location)
	}
	if workshop.resolveCount != 1 {
		t.Fatalf("resolveCount = %d, want 1", workshop.resolveCount)
	}
	if workshop.lastResolveReq.Resolution != "union" {
		t.Errorf("Resolution = %q, want %q", workshop.lastResolveReq.Resolution, "union")
	}
	if workshop.lastResolveReq.KeepLibraryId != 0 {
		t.Errorf("KeepLibraryId = %d, want 0 for union", workshop.lastResolveReq.KeepLibraryId)
	}

	// The conflict disappears from the unresolved list once resolved.
	_, listBody := getConflictsListHTTP(app)
	if strings.Contains(listBody, "Config A") {
		t.Errorf("expected resolved conflict to no longer appear in the list, got: %s", listBody)
	}
}

// --- Override: posts resolution=override with the selected keep_library_id

func TestHandleResolveLibraryMigrationConflict_Override(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	form := url.Values{"conflict_id": {"7"}, "resolution": {"override"}, "keep_library_id": {"11"}}
	code, _, location := postResolveHTTP(app, form)

	if code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", code, http.StatusSeeOther)
	}
	if location != "/workshop/conflicts" {
		t.Errorf("Location = %q, want /workshop/conflicts", location)
	}
	if workshop.lastResolveReq.Resolution != "override" {
		t.Errorf("Resolution = %q, want %q", workshop.lastResolveReq.Resolution, "override")
	}
	if workshop.lastResolveReq.KeepLibraryId != 11 {
		t.Errorf("KeepLibraryId = %d, want 11", workshop.lastResolveReq.KeepLibraryId)
	}
}

// --- Malformed requests: 400, not rendered inline ---------------------------

func TestHandleResolveLibraryMigrationConflict_MissingConflictID(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, _, _ := postResolveHTTP(app, url.Values{"resolution": {"union"}})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
	if workshop.resolveCount != 0 {
		t.Errorf("expected ResolveLibraryMigrationConflict not to be called, got %d calls", workshop.resolveCount)
	}
}

func TestHandleResolveLibraryMigrationConflict_NonNumericConflictID(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, _, _ := postResolveHTTP(app, url.Values{"conflict_id": {"not-a-number"}, "resolution": {"union"}})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
}

func TestHandleResolveLibraryMigrationConflict_UnrecognizedResolution(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, body, _ := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"merge"}})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d; body: %s", code, http.StatusBadRequest, body)
	}
	if workshop.resolveCount != 0 {
		t.Errorf("expected ResolveLibraryMigrationConflict not to be called, got %d calls", workshop.resolveCount)
	}
}

func TestHandleResolveLibraryMigrationConflict_NonNumericKeepLibraryID(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, _, _ := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"override"}, "keep_library_id": {"nope"}})
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
	if workshop.resolveCount != 0 {
		t.Errorf("expected ResolveLibraryMigrationConflict not to be called, got %d calls", workshop.resolveCount)
	}
}

func TestHandleResolveLibraryMigrationConflict_MethodNotAllowed(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	req := httptest.NewRequest(http.MethodGet, "/workshop/conflicts/resolve", nil)
	w := httptest.NewRecorder()
	app.handleResolveLibraryMigrationConflict(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", w.Code, http.StatusMethodNotAllowed)
	}
}

// --- Override with no selection: re-rendered inline, never a bare 400 ------
//
// A client that bypassed the radio group's `required` attribute (no JS,
// hand-crafted POST) reaches the handler with resolution=override and no
// keep_library_id -- the handler's doc comment says this is "never
// silently submitted" but also never a raw 400: it re-renders
// "/workshop/conflicts" with an inline banner, same as a FailedPrecondition.

func TestHandleResolveLibraryMigrationConflict_OverrideMissingKeepLibraryID(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	app := newConflictsTestApp(workshop, api)

	code, body, location := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"override"}})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (inline re-render, not a bare error); body: %s", code, http.StatusOK, body)
	}
	if location != "" {
		t.Errorf("expected no redirect for a rejected submission, got Location %q", location)
	}
	if !strings.Contains(body, "Choose a library to keep before submitting an override.") {
		t.Errorf("expected inline banner prompting a selection, got: %s", body)
	}
	// Still on the conflicts page with the conflict itself re-rendered.
	if !strings.Contains(body, "Config A") {
		t.Errorf("expected the conflict to still be rendered after a rejected submission, got: %s", body)
	}
	if workshop.resolveCount != 0 {
		t.Errorf("expected ResolveLibraryMigrationConflict not to be called, got %d calls", workshop.resolveCount)
	}
}

// --- FailedPrecondition/InvalidArgument from the API: inline banner, not 500

func TestHandleResolveLibraryMigrationConflict_FailedPreconditionRerendersInline(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	workshop.resolveErr = status.Error(codes.FailedPrecondition, "conflict 7 was already resolved")
	app := newConflictsTestApp(workshop, api)

	code, body, location := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"union"}})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if location != "" {
		t.Errorf("expected no redirect on a rejected submission, got Location %q", location)
	}
	if !strings.Contains(body, "conflict 7 was already resolved") {
		t.Errorf("expected the API's message inline, got: %s", body)
	}
}

func TestHandleResolveLibraryMigrationConflict_InvalidArgumentRerendersInline(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	workshop.resolveErr = status.Error(codes.InvalidArgument, "keep_library_id is not a candidate of this conflict")
	app := newConflictsTestApp(workshop, api)

	code, body, location := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"override"}, "keep_library_id": {"999"}})

	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if location != "" {
		t.Errorf("expected no redirect on a rejected submission, got Location %q", location)
	}
	if !strings.Contains(body, "keep_library_id is not a candidate of this conflict") {
		t.Errorf("expected the API's message inline, got: %s", body)
	}
}

// --- Any other error is a genuine failure: 500 ------------------------------

func TestHandleResolveLibraryMigrationConflict_OtherErrorIs500(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	workshop.resolveErr = errors.New("boom: database is on fire")
	app := newConflictsTestApp(workshop, api)

	code, body, _ := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"union"}})
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusInternalServerError, body)
	}
}

// A gRPC status error with an unhandled code (e.g. Internal) is also a
// genuine failure, not expected control flow -- only FailedPrecondition
// and InvalidArgument get the inline-banner treatment.
func TestHandleResolveLibraryMigrationConflict_UnhandledGRPCCodeIs500(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	workshop.resolveErr = status.Error(codes.Internal, "unexpected backend failure")
	app := newConflictsTestApp(workshop, api)

	code, body, _ := postResolveHTTP(app, url.Values{"conflict_id": {"7"}, "resolution": {"union"}})
	if code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusInternalServerError, body)
	}
}

// --- resolveConflictDeploymentName / buildConflictViews degrade gracefully -

// A lookup failure (a candidate's underlying SGC has since been deleted)
// degrades to a numbered fallback rather than failing the whole page --
// buildConflictViews' doc comment.
func TestBuildConflictViews_DegradesOnLookupFailure(t *testing.T) {
	workshop, api := buildFakeConflictsData()
	// Candidate library 10 no longer resolves, and its SGC (501) no
	// longer exists either.
	delete(workshop.libraries, 10)
	delete(api.sgcs, 501)
	app := newConflictsTestApp(workshop, api)

	code, body := getConflictsListHTTP(app)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "library 10") {
		t.Errorf("expected numbered fallback for the missing library, got: %s", body)
	}
	if !strings.Contains(body, "deployment 501") {
		t.Errorf("expected numbered fallback for the missing deployment, got: %s", body)
	}
	// The other, still-resolvable candidate is unaffected.
	if !strings.Contains(body, "Lib B") {
		t.Errorf("expected the still-resolvable candidate to render normally, got: %s", body)
	}
}
