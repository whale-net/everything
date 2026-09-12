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

// This file guards task #2367 (root plan #2359): the Games page's Workshop
// Libraries panel becoming an editable GameConfig-level attach/detach
// surface (FR8/FR9/FR10), replacing M5's read-only SGC-scoped panel.
//
// fakeLibrariesAPIClient/fakeLibrariesWorkshopClient are scoped to this
// task's own call graph (ListGameConfigs on the api client;
// ListLibraries/ListGameConfigLibraries/GetGameConfigLibraryAttachments/
// ListAddonPathPresets/ListLibraryMigrationConflicts/
// AddLibraryToGameConfig/RemoveLibraryFromGameConfig on the workshop
// client) -- any call to an un-overridden method panics on the nil
// embedded interface, the same deliberate convention
// fakeGamesAPIClient/fakeWorkshopServiceClient already use elsewhere in
// this package.

type fakeLibrariesAPIClient struct {
	manmanpb.ManManAPIClient

	configs []*manmanpb.GameConfig
}

func (f *fakeLibrariesAPIClient) ListGameConfigs(ctx context.Context, in *manmanpb.ListGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigsResponse, error) {
	var out []*manmanpb.GameConfig
	for _, c := range f.configs {
		if in.GameId == 0 || c.GameId == in.GameId {
			out = append(out, c)
		}
	}
	return &manmanpb.ListGameConfigsResponse{Configs: out}, nil
}

type fakeLibrariesWorkshopClient struct {
	manmanpb.WorkshopServiceClient

	libraries          []*manmanpb.WorkshopLibrary
	attachmentsByConf  map[int64][]*manmanpb.GameConfigWorkshopLibrary
	attachedLibsByConf map[int64][]*manmanpb.WorkshopLibrary
	presets            []*manmanpb.GameAddonPathPreset
	conflicts          []*manmanpb.WorkshopLibraryMigrationConflict

	addCalls    []*manmanpb.AddLibraryToGameConfigRequest
	removeCalls []*manmanpb.RemoveLibraryFromGameConfigRequest
}

func (f *fakeLibrariesWorkshopClient) ListLibraries(ctx context.Context, in *manmanpb.ListLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListLibrariesResponse, error) {
	return &manmanpb.ListLibrariesResponse{Libraries: f.libraries}, nil
}

func (f *fakeLibrariesWorkshopClient) ListGameConfigLibraries(ctx context.Context, in *manmanpb.ListGameConfigLibrariesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigLibrariesResponse, error) {
	return &manmanpb.ListGameConfigLibrariesResponse{Libraries: f.attachedLibsByConf[in.ConfigId]}, nil
}

func (f *fakeLibrariesWorkshopClient) GetGameConfigLibraryAttachments(ctx context.Context, in *manmanpb.GetGameConfigLibraryAttachmentsRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigLibraryAttachmentsResponse, error) {
	return &manmanpb.GetGameConfigLibraryAttachmentsResponse{Attachments: f.attachmentsByConf[in.ConfigId]}, nil
}

func (f *fakeLibrariesWorkshopClient) ListAddonPathPresets(ctx context.Context, in *manmanpb.ListAddonPathPresetsRequest, opts ...grpc.CallOption) (*manmanpb.ListAddonPathPresetsResponse, error) {
	return &manmanpb.ListAddonPathPresetsResponse{Presets: f.presets}, nil
}

func (f *fakeLibrariesWorkshopClient) ListLibraryMigrationConflicts(ctx context.Context, in *manmanpb.ListLibraryMigrationConflictsRequest, opts ...grpc.CallOption) (*manmanpb.ListLibraryMigrationConflictsResponse, error) {
	return &manmanpb.ListLibraryMigrationConflictsResponse{Conflicts: f.conflicts}, nil
}

func (f *fakeLibrariesWorkshopClient) AddLibraryToGameConfig(ctx context.Context, in *manmanpb.AddLibraryToGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.AddLibraryToGameConfigResponse, error) {
	f.addCalls = append(f.addCalls, in)
	if f.attachmentsByConf == nil {
		f.attachmentsByConf = map[int64][]*manmanpb.GameConfigWorkshopLibrary{}
	}
	f.attachmentsByConf[in.ConfigId] = append(f.attachmentsByConf[in.ConfigId], &manmanpb.GameConfigWorkshopLibrary{
		ConfigId:  in.ConfigId,
		LibraryId: in.LibraryId,
		PresetId:  in.PresetId,
	})
	for _, lib := range f.libraries {
		if lib.LibraryId == in.LibraryId {
			if f.attachedLibsByConf == nil {
				f.attachedLibsByConf = map[int64][]*manmanpb.WorkshopLibrary{}
			}
			f.attachedLibsByConf[in.ConfigId] = append(f.attachedLibsByConf[in.ConfigId], lib)
		}
	}
	return &manmanpb.AddLibraryToGameConfigResponse{}, nil
}

func (f *fakeLibrariesWorkshopClient) RemoveLibraryFromGameConfig(ctx context.Context, in *manmanpb.RemoveLibraryFromGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.RemoveLibraryFromGameConfigResponse, error) {
	f.removeCalls = append(f.removeCalls, in)
	var kept []*manmanpb.GameConfigWorkshopLibrary
	for _, a := range f.attachmentsByConf[in.ConfigId] {
		if a.LibraryId != in.LibraryId {
			kept = append(kept, a)
		}
	}
	f.attachmentsByConf[in.ConfigId] = kept

	var keptLibs []*manmanpb.WorkshopLibrary
	for _, l := range f.attachedLibsByConf[in.ConfigId] {
		if l.LibraryId != in.LibraryId {
			keptLibs = append(keptLibs, l)
		}
	}
	f.attachedLibsByConf[in.ConfigId] = keptLibs
	return &manmanpb.RemoveLibraryFromGameConfigResponse{}, nil
}

func newLibrariesTestApp(api *fakeLibrariesAPIClient, workshop *fakeLibrariesWorkshopClient) *App {
	return &App{grpc: &ControlClient{api: api, workshop: workshop}}
}

// TestWorkshopPanel_FR10_NoM5ComingSoonNote guards FR10: the M5 "coming
// soon: shared across configs" note must be gone from the panel.
func TestWorkshopPanel_FR10_NoM5ComingSoonNote(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodGet, "/games/1/workshop-panel", nil)
	w := httptest.NewRecorder()
	app.handleGameWorkshopPanel(w, req, "1")

	body := w.Body.String()
	if strings.Contains(body, "shared across configs and editable here in M6") {
		t.Errorf("workshop panel must not contain the retired M5 note, got: %s", body)
	}
	if strings.Contains(body, "coming soon") {
		t.Errorf("workshop panel must not contain any 'coming soon' text, got: %s", body)
	}
}

// TestWorkshopPanel_RendersAttachAndDetachControls guards that the panel
// is no longer read-only: an attach control (+ Add Library) and, once a
// library is attached, a detach control (Remove) both render.
func TestWorkshopPanel_RendersAttachAndDetachControls(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{
		attachedLibsByConf: map[int64][]*manmanpb.WorkshopLibrary{
			10: {{LibraryId: 100, GameId: 1, Name: "Better Maps"}},
		},
		attachmentsByConf: map[int64][]*manmanpb.GameConfigWorkshopLibrary{
			10: {{ConfigId: 10, LibraryId: 100}},
		},
	}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodGet, "/games/1/workshop-panel", nil)
	w := httptest.NewRecorder()
	app.handleGameWorkshopPanel(w, req, "1")

	body := w.Body.String()
	if !strings.Contains(body, "+ Add Library") {
		t.Errorf("expected an attach control, got: %s", body)
	}
	if !strings.Contains(body, "Better Maps") {
		t.Errorf("expected the attached library to be listed, got: %s", body)
	}
	if !strings.Contains(body, "Remove") {
		t.Errorf("expected a detach control, got: %s", body)
	}
	if !strings.Contains(body, "/games/1/configs/10/libraries/100/remove") {
		t.Errorf("expected the detach form to post to the GC-scoped remove route, got: %s", body)
	}
}

// TestWorkshopPanel_FR8FR9_InheritanceCopy guards that the panel states
// the inheritance the underlying attach/detach carries (US5): attaching
// applies to every deployment of the configuration, detaching removes it
// from all of them.
func TestWorkshopPanel_FR8FR9_InheritanceCopy(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodGet, "/games/1/workshop-panel", nil)
	w := httptest.NewRecorder()
	app.handleGameWorkshopPanel(w, req, "1")

	body := w.Body.String()
	if !strings.Contains(body, "every deployment of this configuration") {
		t.Errorf("expected copy stating attach/detach applies to every deployment of the configuration, got: %s", body)
	}
}

// TestHandleGameConfigLibraryAdd_PostsGameConfigID guards that attach
// posts AddLibraryToGameConfig with the GameConfig id -- never an SGC id
// -- and that the panel re-renders with the library listed afterward.
func TestHandleGameConfigLibraryAdd_PostsGameConfigID(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{
		libraries: []*manmanpb.WorkshopLibrary{{LibraryId: 100, GameId: 1, Name: "Better Maps"}},
	}
	app := newLibrariesTestApp(api, workshop)

	form := strings.NewReader("library_id=100")
	req := httptest.NewRequest(http.MethodPost, "/games/1/configs/10/libraries/add", form)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleGameConfigLibraryAdd(w, req, "1", "10")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if len(workshop.addCalls) != 1 {
		t.Fatalf("expected exactly one AddLibraryToGameConfig call, got %d", len(workshop.addCalls))
	}
	got := workshop.addCalls[0]
	if got.ConfigId != 10 {
		t.Errorf("AddLibraryToGameConfig ConfigId = %d, want 10 (the GameConfig id, not an SGC id)", got.ConfigId)
	}
	if got.LibraryId != 100 {
		t.Errorf("AddLibraryToGameConfig LibraryId = %d, want 100", got.LibraryId)
	}

	body := w.Body.String()
	if !strings.Contains(body, "Better Maps") {
		t.Errorf("expected the panel to re-render with the newly attached library listed, got: %s", body)
	}
}

// TestHandleGameConfigLibraryRemove_PostsGameConfigID guards that detach
// posts RemoveLibraryFromGameConfig with the GameConfig id, and the
// library disappears from the re-rendered panel.
func TestHandleGameConfigLibraryRemove_PostsGameConfigID(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{
		attachedLibsByConf: map[int64][]*manmanpb.WorkshopLibrary{
			10: {{LibraryId: 100, GameId: 1, Name: "Better Maps"}},
		},
		attachmentsByConf: map[int64][]*manmanpb.GameConfigWorkshopLibrary{
			10: {{ConfigId: 10, LibraryId: 100}},
		},
	}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodPost, "/games/1/configs/10/libraries/100/remove", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigLibraryRemove(w, req, "1", "10", "100")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200, body: %s", w.Code, w.Body.String())
	}
	if len(workshop.removeCalls) != 1 {
		t.Fatalf("expected exactly one RemoveLibraryFromGameConfig call, got %d", len(workshop.removeCalls))
	}
	got := workshop.removeCalls[0]
	if got.ConfigId != 10 || got.LibraryId != 100 {
		t.Errorf("RemoveLibraryFromGameConfig = %+v, want ConfigId=10 LibraryId=100", got)
	}

	body := w.Body.String()
	if strings.Contains(body, "Better Maps") {
		t.Errorf("expected the removed library to no longer be listed, got: %s", body)
	}
	if !strings.Contains(body, "No workshop libraries attached.") {
		t.Errorf("expected the empty state after removing the only attachment, got: %s", body)
	}
}

// TestWorkshopPanel_FR12_ConflictPointerNotEmptyList guards the conflict
// guard: a GameConfig with an unresolved migration conflict must render a
// pointer to the conflict-resolution surface, never a silently-empty
// attached-library list.
func TestWorkshopPanel_FR12_ConflictPointerNotEmptyList(t *testing.T) {
	api := &fakeLibrariesAPIClient{configs: []*manmanpb.GameConfig{{ConfigId: 10, GameId: 1, Name: "Survival"}}}
	workshop := &fakeLibrariesWorkshopClient{
		conflicts: []*manmanpb.WorkshopLibraryMigrationConflict{
			{ConflictId: 1, ConfigId: 10},
		},
	}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodGet, "/games/1/workshop-panel", nil)
	w := httptest.NewRecorder()
	app.handleGameWorkshopPanel(w, req, "1")

	body := w.Body.String()
	if strings.Contains(body, "No workshop libraries attached.") {
		t.Errorf("a conflicted config must never render the bare empty-list state, got: %s", body)
	}
	if !strings.Contains(body, "unresolved") {
		t.Errorf("expected a pointer naming the unresolved conflict, got: %s", body)
	}
	// A conflicted config must not offer to attach either -- there is
	// nothing sensible to attach onto until the conflict resolves.
	if strings.Contains(body, "+ Add Library") {
		t.Errorf("a conflicted config must not render an attach control, got: %s", body)
	}
}

// TestHandleGameConfigAvailableLibraries_ExcludesAttached guards the
// attach picker (FR8): it must list a game's libraries minus whatever is
// already attached to this GameConfig.
func TestHandleGameConfigAvailableLibraries_ExcludesAttached(t *testing.T) {
	api := &fakeLibrariesAPIClient{}
	workshop := &fakeLibrariesWorkshopClient{
		libraries: []*manmanpb.WorkshopLibrary{
			{LibraryId: 100, GameId: 1, Name: "Better Maps"},
			{LibraryId: 200, GameId: 1, Name: "Extra Mobs"},
		},
		attachedLibsByConf: map[int64][]*manmanpb.WorkshopLibrary{
			10: {{LibraryId: 100, GameId: 1, Name: "Better Maps"}},
		},
	}
	app := newLibrariesTestApp(api, workshop)

	req := httptest.NewRequest(http.MethodGet, "/games/1/configs/10/libraries/available", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigAvailableLibraries(w, req, "1", "10")

	body := w.Body.String()
	if strings.Contains(body, "Better Maps") {
		t.Errorf("already-attached library must not appear in the available picker, got: %s", body)
	}
	if !strings.Contains(body, "Extra Mobs") {
		t.Errorf("expected the unattached library to appear in the available picker, got: %s", body)
	}
}

// TestHandleGameConfigLibraryAdd_RequiresPOST guards that GET is rejected
// (405), never silently treated as a submission.
func TestHandleGameConfigLibraryAdd_RequiresPOST(t *testing.T) {
	app := newLibrariesTestApp(&fakeLibrariesAPIClient{}, &fakeLibrariesWorkshopClient{})
	req := httptest.NewRequest(http.MethodGet, "/games/1/configs/10/libraries/add", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigLibraryAdd(w, req, "1", "10")
	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", w.Code)
	}
}
