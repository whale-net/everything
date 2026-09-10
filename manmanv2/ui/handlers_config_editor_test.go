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

// Guards task #2276's Config Editor blade (FR13, the Config-Editor half of
// FR10, NFR5, WD4, WD10): these tests drive
// handleGameConfigEditor/handleGameConfigEditorSave end to end and assert
// on the *outbound UpdateGameConfigRequest shape* the fake API client
// records, not merely on resulting field values -- FR13's whole reasoning
// is about which request branch the API handler takes, so the request
// shape itself is what these tests must pin.

// fakeConfigEditorAPIClient is a standalone fixture (mirrors
// fakeDeploymentSettingsAPIClient's shape) covering exactly the reads/
// writes the Config Editor blade needs: GetGameConfig,
// ListGameConfigVolumes, UpdateGameConfig. Deliberately does not embed any
// ConfigurationPatch/strategy methods -- NFR5 requires this section never
// touch that surface, and a call to an unimplemented method here would
// panic via the embedded manmanpb.ManManAPIClient nil interface, which is
// exactly the failure mode the NFR5 test below wants for free.
type fakeConfigEditorAPIClient struct {
	manmanpb.ManManAPIClient

	config  *manmanpb.GameConfig
	volumes []*manmanpb.GameConfigVolume

	updateReqs []*manmanpb.UpdateGameConfigRequest
}

func (f *fakeConfigEditorAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.config}, nil
}

func (f *fakeConfigEditorAPIClient) ListGameConfigVolumes(ctx context.Context, in *manmanpb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigVolumesResponse, error) {
	return &manmanpb.ListGameConfigVolumesResponse{Volumes: f.volumes}, nil
}

func (f *fakeConfigEditorAPIClient) UpdateGameConfig(ctx context.Context, in *manmanpb.UpdateGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateGameConfigResponse, error) {
	f.updateReqs = append(f.updateReqs, in)
	f.config.Name = in.Name
	f.config.Image = in.Image
	f.config.ArgsTemplate = in.ArgsTemplate
	f.config.EnvTemplate = in.EnvTemplate
	return &manmanpb.UpdateGameConfigResponse{Config: f.config}, nil
}

func configEditorTestApp(api *fakeConfigEditorAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// newConfigEditorFixtures mirrors newDeploymentSettingsFixtures' shape:
// GameConfig(3, game 1) with a non-empty args template and one env var,
// plus one volume.
func newConfigEditorFixtures() *fakeConfigEditorAPIClient {
	return &fakeConfigEditorAPIClient{
		config: &manmanpb.GameConfig{
			ConfigId:     3,
			GameId:       1,
			Name:         "vanilla",
			Image:        "itzg/minecraft-server",
			ArgsTemplate: "--nogui",
			EnvTemplate:  map[string]string{"MAX_PLAYERS": "10"},
		},
		volumes: []*manmanpb.GameConfigVolume{
			{VolumeId: 9, ConfigId: 3, Name: "world", Description: "World data", ContainerPath: "/data", HostSubpath: "world", ReadOnly: false, VolumeType: "bind"},
		},
	}
}

// postConfigEditorSave posts Basics/Environment form values to POST
// /games/{gameID}/configs/{configID}/editor, exactly as
// config_editor.templ's form does.
func postConfigEditorSave(t *testing.T, app *App, gameID, configID string, form map[string][]string) *httptest.ResponseRecorder {
	t.Helper()
	values := make([]string, 0)
	for k, vs := range form {
		for _, v := range vs {
			values = append(values, k+"="+v)
		}
	}
	req := httptest.NewRequest(http.MethodPost, "/games/"+gameID+"/configs/"+configID+"/editor", strings.NewReader(strings.Join(values, "&")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleGameConfigEditor(w, req, gameID, configID)
	return w
}

// TestConfigEditor_FR13_ClearsArgsTemplate is the load-bearing FR13 test:
// saving with an emptied args template must actually clear it, since the
// live risk is the API handler's `if req.ArgsTemplate != ""` guard on the
// empty-update_paths branch (see buildConfigEditorUpdateRequest's doc
// comment).
func TestConfigEditor_FR13_ClearsArgsTemplate(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	w := postConfigEditorSave(t, app, "1", "3", map[string][]string{
		"name":          {"vanilla"},
		"image":         {"itzg/minecraft-server"},
		"args_template": {""},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (HX-Redirect); body: %s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("HX-Redirect"); got == "" {
		t.Errorf("expected an HX-Redirect header on successful save, got none")
	}

	if len(api.updateReqs) != 1 {
		t.Fatalf("UpdateGameConfig calls = %d, want exactly 1", len(api.updateReqs))
	}
	req := api.updateReqs[0]
	if req.ArgsTemplate != "" {
		t.Errorf("request ArgsTemplate = %q, want empty", req.ArgsTemplate)
	}
	if api.config.ArgsTemplate != "" {
		t.Errorf("persisted ArgsTemplate = %q, want cleared to empty -- FR13's whole point", api.config.ArgsTemplate)
	}
}

// TestConfigEditor_FR13_ExplicitUpdatePaths guards the explicit-paths
// assertion: update_paths is always exactly
// ["name","image","args_template","env_template"], never empty --
// symmetric with FR12's update_paths=["port_bindings"].
func TestConfigEditor_FR13_ExplicitUpdatePaths(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	postConfigEditorSave(t, app, "1", "3", map[string][]string{
		"name":          {"vanilla"},
		"image":         {"itzg/minecraft-server"},
		"args_template": {"--nogui"},
		"env_key":       {"MAX_PLAYERS"},
		"env_value":     {"20"},
	})

	if len(api.updateReqs) != 1 {
		t.Fatalf("UpdateGameConfig calls = %d, want exactly 1", len(api.updateReqs))
	}
	want := []string{"name", "image", "args_template", "env_template"}
	got := api.updateReqs[0].UpdatePaths
	if len(got) != len(want) {
		t.Fatalf("UpdatePaths = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("UpdatePaths = %v, want %v", got, want)
		}
	}
}

// TestConfigEditor_Atomicity guards FR13/FR10: Basics and Environment
// edits made in the same session produce exactly one UpdateGameConfig
// call, not two -- there is no separate second write.
func TestConfigEditor_Atomicity(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	postConfigEditorSave(t, app, "1", "3", map[string][]string{
		"name":          {"vanilla-renamed"},
		"image":         {"itzg/minecraft-server"},
		"args_template": {"--nogui --forceUpgrade"},
		"env_key":       {"MAX_PLAYERS", "DIFFICULTY"},
		"env_value":     {"20", "hard"},
	})

	if len(api.updateReqs) != 1 {
		t.Fatalf("UpdateGameConfig calls = %d, want exactly 1 (atomic Basics+Environment save)", len(api.updateReqs))
	}
	req := api.updateReqs[0]
	if req.Name != "vanilla-renamed" || req.ArgsTemplate != "--nogui --forceUpgrade" {
		t.Errorf("expected the single request to carry both Basics fields, got Name=%q ArgsTemplate=%q", req.Name, req.ArgsTemplate)
	}
	if req.EnvTemplate["MAX_PLAYERS"] != "20" || req.EnvTemplate["DIFFICULTY"] != "hard" {
		t.Errorf("expected the single request to also carry Environment edits, got %v", req.EnvTemplate)
	}
}

// TestConfigEditor_NFR5_EnvTemplateOnly guards NFR5: the Environment tab
// writes GameConfig.env_template and nothing else -- the fake client above
// has no ConfigurationPatch/strategy methods at all, so any attempt to
// write through that surface would panic on the embedded nil
// manmanpb.ManManAPIClient. This test's job is to prove the save path
// completes successfully without ever needing that surface.
func TestConfigEditor_NFR5_EnvTemplateOnly(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	w := postConfigEditorSave(t, app, "1", "3", map[string][]string{
		"name":          {"vanilla"},
		"image":         {"itzg/minecraft-server"},
		"args_template": {"--nogui"},
		"env_key":       {"MAX_PLAYERS"},
		"env_value":     {"25"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.updateReqs) != 1 {
		t.Fatalf("UpdateGameConfig calls = %d, want exactly 1", len(api.updateReqs))
	}
	if api.updateReqs[0].EnvTemplate["MAX_PLAYERS"] != "25" {
		t.Errorf("expected env_template to carry the edit, got %v", api.updateReqs[0].EnvTemplate)
	}
}

// TestConfigEditor_ValidationErrors_BlockSaveAndPreserveValues guards
// FR10's per-field validation contract: a blank required field blocks the
// save (no UpdateGameConfig call), reports the error against that field,
// and re-renders with every entered value intact, including an
// Environment-tab value even though Basics (where the error surfaces) is
// the active tab on re-render.
func TestConfigEditor_ValidationErrors_BlockSaveAndPreserveValues(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	w := postConfigEditorSave(t, app, "1", "3", map[string][]string{
		"name":          {""},
		"image":         {"itzg/minecraft-server"},
		"args_template": {"--nogui"},
		"env_key":       {"MAX_PLAYERS"},
		"env_value":     {"33"},
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered fragment); body: %s", w.Code, w.Body.String())
	}
	if len(api.updateReqs) != 0 {
		t.Fatalf("UpdateGameConfig calls = %d, want 0 (validation must block the save)", len(api.updateReqs))
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-field-error="name"`) {
		t.Errorf("expected the Name field's error to be reported against that field, got %q", body)
	}
	if !strings.Contains(body, "Name is required") {
		t.Errorf("expected a Name-required error message, got %q", body)
	}
	if !strings.Contains(body, `value="itzg/minecraft-server"`) {
		t.Errorf("expected the entered Image value to survive re-render, got %q", body)
	}
	if !strings.Contains(body, `value="--nogui"`) {
		t.Errorf("expected the entered Args Template value to survive re-render, got %q", body)
	}
	if !strings.Contains(body, `value="MAX_PLAYERS"`) || !strings.Contains(body, `value="33"`) {
		t.Errorf("expected the entered Environment-tab row to survive re-render even though Basics is active, got %q", body)
	}
}

// TestConfigEditorGet_WD10_NoPortsTab guards WD10's negative assertion:
// the blade renders exactly three tabs -- Basics, Environment, Volumes --
// and no Ports tab; no port value appears anywhere.
func TestConfigEditorGet_WD10_NoPortsTab(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/games/1/configs/3/editor", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigEditor(w, req, "1", "3")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// The trailing quote excludes the blade's own script, which also
	// references the bare attribute name in querySelector/getAttribute
	// calls without a trailing "=".
	if got := strings.Count(body, `data-blade-tab-id="`); got != 3 {
		t.Errorf("expected exactly 3 tabs, got %d in %q", got, body)
	}
	if strings.Contains(body, "Ports") {
		t.Errorf("expected no Ports tab (WD10: port_bindings live on the deployment, not the config), got %q", body)
	}
	if strings.Contains(body, "Actions") {
		t.Errorf("expected no Actions tab (decision 8), got %q", body)
	}
}

// TestConfigEditorGet_FR2_NoRawSGCInDisplayText guards FR2: no "SGC" or
// "server game config" (case-insensitive) anywhere in the rendered blade.
func TestConfigEditorGet_FR2_NoRawSGCInDisplayText(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/games/1/configs/3/editor", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigEditor(w, req, "1", "3")

	lower := strings.ToLower(w.Body.String())
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no raw SGC identifier in display text (FR2), got %q", w.Body.String())
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf(`expected no "server game config" in display text (FR2), got %q`, w.Body.String())
	}
}

// TestConfigEditorGet_VolumesReadOnly guards WD4's negative assertion end
// to end through the handler: all six GameConfigVolume fields render,
// there is no add/edit/remove control, and the backup link-out plus M6
// note are present.
func TestConfigEditorGet_VolumesReadOnly(t *testing.T) {
	api := newConfigEditorFixtures()
	app := configEditorTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/games/1/configs/3/editor", nil)
	w := httptest.NewRecorder()
	app.handleGameConfigEditor(w, req, "1", "3")

	body := w.Body.String()
	for _, want := range []string{"world", "World data", "/data", "world", "No", "bind"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected volume field %q to render, got %q", want, body)
		}
	}
	if strings.Contains(body, "editable here in M6") == false {
		t.Errorf("expected the M6 note, got %q", body)
	}
	if !strings.Contains(body, "Manage backups") {
		t.Errorf("expected the backup link-out, got %q", body)
	}
	if strings.Contains(body, "data-volume-add") || strings.Contains(body, "data-volume-edit") || strings.Contains(body, "data-volume-remove") || strings.Contains(body, "data-volume-backup-schedule") {
		t.Errorf("expected no add/edit/remove control and no inline backup-schedule control in Volumes (WD4), got %q", body)
	}
}
