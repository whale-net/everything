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

// Guards task #2090's deployment-level env override CRUD (FR2/FR4):
// overrides persist as ConfigurationPatch rows at patch_level =
// server_game_config on the game's env_vars strategy, written only via
// the public API, and removing the last override deletes the patch.

// fakeEnvAPIClient implements only the RPCs the env override handlers
// reach. Any un-overridden call panics on the nil embedded interface,
// loudly failing a scenario whose call graph grows unexpectedly.
type fakeEnvAPIClient struct {
	manmanpb.ManManAPIClient

	sgc        *manmanpb.ServerGameConfig
	gc         *manmanpb.GameConfig
	strategies []*manmanpb.ConfigurationStrategy
	patches    []*manmanpb.ConfigurationPatch

	createStrategyReqs []*manmanpb.CreateConfigurationStrategyRequest
	createPatchReqs    []*manmanpb.CreateConfigurationPatchRequest
	updatePatchReqs    []*manmanpb.UpdateConfigurationPatchRequest
	deletePatchIDs     []int64
}

func (f *fakeEnvAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeEnvAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.gc}, nil
}

func (f *fakeEnvAPIClient) ListConfigurationStrategies(ctx context.Context, in *manmanpb.ListConfigurationStrategiesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationStrategiesResponse, error) {
	return &manmanpb.ListConfigurationStrategiesResponse{Strategies: f.strategies}, nil
}

func (f *fakeEnvAPIClient) CreateConfigurationStrategy(ctx context.Context, in *manmanpb.CreateConfigurationStrategyRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationStrategyResponse, error) {
	f.createStrategyReqs = append(f.createStrategyReqs, in)
	next := int64(100 + len(f.createStrategyReqs))
	return &manmanpb.CreateConfigurationStrategyResponse{
		Strategy: &manmanpb.ConfigurationStrategy{StrategyId: next, GameId: in.GameId, StrategyType: in.StrategyType},
	}, nil
}

func (f *fakeEnvAPIClient) ListConfigurationPatches(ctx context.Context, in *manmanpb.ListConfigurationPatchesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationPatchesResponse, error) {
	return &manmanpb.ListConfigurationPatchesResponse{Patches: f.patches}, nil
}

func (f *fakeEnvAPIClient) CreateConfigurationPatch(ctx context.Context, in *manmanpb.CreateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationPatchResponse, error) {
	f.createPatchReqs = append(f.createPatchReqs, in)
	return &manmanpb.CreateConfigurationPatchResponse{
		Patch: &manmanpb.ConfigurationPatch{PatchId: int64(200 + len(f.createPatchReqs)), StrategyId: in.StrategyId, PatchLevel: in.PatchLevel, EntityId: in.EntityId, PatchContent: in.PatchContent, PatchFormat: in.PatchFormat},
	}, nil
}

func (f *fakeEnvAPIClient) UpdateConfigurationPatch(ctx context.Context, in *manmanpb.UpdateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.UpdateConfigurationPatchResponse, error) {
	f.updatePatchReqs = append(f.updatePatchReqs, in)
	return &manmanpb.UpdateConfigurationPatchResponse{Patch: &manmanpb.ConfigurationPatch{PatchId: in.PatchId, PatchContent: in.PatchContent, PatchFormat: in.PatchFormat, PatchOrder: in.PatchOrder}}, nil
}

func (f *fakeEnvAPIClient) DeleteConfigurationPatch(ctx context.Context, in *manmanpb.DeleteConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.DeleteConfigurationPatchResponse, error) {
	f.deletePatchIDs = append(f.deletePatchIDs, in.PatchId)
	return &manmanpb.DeleteConfigurationPatchResponse{}, nil
}

// envTestApp wires an App to a fakeEnvAPIClient (package-main white-box
// construction of ControlClient's unexported api field, same approach as
// handlers_sgc_test.go's newTestApp).
func envTestApp(api *fakeEnvAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// postEnv posts form values to a POST /sgc/{id}/env/{action} handler.
func postEnv(t *testing.T, app *App, handler func(http.ResponseWriter, *http.Request, string), sgcID, action string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sgc/"+sgcID+"/"+action, strings.NewReader(formEncode(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	handler(w, req, sgcID)
	return w
}

func formEncode(form map[string]string) string {
	parts := make([]string, 0, len(form))
	for k, v := range form {
		parts = append(parts, k+"="+strings.ReplaceAll(v, "&", "%26"))
	}
	return strings.Join(parts, "&")
}

// newEnvTestFixtures returns an SGC(7) on GameConfig(3, game 1) with one
// env_vars strategy and no patches yet.
func newEnvTestFixtures() *fakeEnvAPIClient {
	return &fakeEnvAPIClient{
		sgc: &manmanpb.ServerGameConfig{ServerGameConfigId: 7, ServerId: 2, GameConfigId: 3},
		gc:  &manmanpb.GameConfig{ConfigId: 3, GameId: 1, EnvTemplate: map[string]string{"MAX_PLAYERS": "10"}},
		strategies: []*manmanpb.ConfigurationStrategy{
			{StrategyId: 55, GameId: 1, StrategyType: "env_vars", Name: "Environment Variables"},
			{StrategyId: 56, GameId: 1, StrategyType: "file_properties", Name: "Server Properties"},
		},
	}
}

func TestHandleSGCEnvSet_CreatesOverridePatch(t *testing.T) {
	api := newEnvTestFixtures()
	app := envTestApp(api)

	w := postEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "DIFFICULTY", "value": "hard"})

	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d (body: %s)", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if loc := w.Header().Get("Location"); loc != "/sgc/7" {
		t.Errorf("redirect = %q, want /sgc/7", loc)
	}
	if len(api.createPatchReqs) != 1 {
		t.Fatalf("CreateConfigurationPatch calls = %d, want 1", len(api.createPatchReqs))
	}
	req := api.createPatchReqs[0]
	if req.StrategyId != 55 {
		t.Errorf("StrategyId = %d, want 55 (the env_vars strategy)", req.StrategyId)
	}
	if req.PatchLevel != "server_game_config" {
		t.Errorf("PatchLevel = %q, want server_game_config", req.PatchLevel)
	}
	if req.EntityId != 7 {
		t.Errorf("EntityId = %d, want 7", req.EntityId)
	}
	if req.PatchContent != "DIFFICULTY=hard" {
		t.Errorf("PatchContent = %q, want DIFFICULTY=hard", req.PatchContent)
	}
	if req.PatchFormat != "properties" {
		t.Errorf("PatchFormat = %q, want properties", req.PatchFormat)
	}
	if len(api.createStrategyReqs) != 0 {
		t.Errorf("strategy already existed; unexpected CreateConfigurationStrategy calls = %d", len(api.createStrategyReqs))
	}
}

func TestHandleSGCEnvSet_UpdatesExistingPatch(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MOTD=hello", PatchFormat: "properties", PatchOrder: 0},
	}
	app := envTestApp(api)

	w := postEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "MAX_PLAYERS", "value": "30"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.updatePatchReqs) != 1 {
		t.Fatalf("UpdateConfigurationPatch calls = %d, want 1", len(api.updatePatchReqs))
	}
	req := api.updatePatchReqs[0]
	if req.PatchId != 9 {
		t.Errorf("PatchId = %d, want 9", req.PatchId)
	}
	if req.PatchContent != "MAX_PLAYERS=30\nMOTD=hello" {
		t.Errorf("PatchContent = %q, want sorted merged overrides (MAX_PLAYERS=30\\nMOTD=hello)", req.PatchContent)
	}
	if len(api.createPatchReqs) != 0 {
		t.Errorf("patch already existed; unexpected CreateConfigurationPatch calls = %d", len(api.createPatchReqs))
	}
}

func TestHandleSGCEnvSet_LazilyCreatesEnvVarsStrategy(t *testing.T) {
	api := newEnvTestFixtures()
	api.strategies = nil
	app := envTestApp(api)

	w := postEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "EULA", "value": "true"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.createStrategyReqs) != 1 {
		t.Fatalf("CreateConfigurationStrategy calls = %d, want 1", len(api.createStrategyReqs))
	}
	req := api.createStrategyReqs[0]
	if req.StrategyType != "env_vars" {
		t.Errorf("StrategyType = %q, want env_vars", req.StrategyType)
	}
	if req.GameId != 1 {
		t.Errorf("GameId = %d, want 1", req.GameId)
	}
	if len(api.createPatchReqs) != 1 || api.createPatchReqs[0].StrategyId == 0 {
		t.Fatalf("expected override patch created on the new strategy, got %+v", api.createPatchReqs)
	}
}

func TestHandleSGCEnvSet_RejectsInvalidKey(t *testing.T) {
	api := newEnvTestFixtures()
	app := envTestApp(api)

	for _, key := range []string{"", "BAD KEY", "A=B", "LINE\nBREAK"} {
		w := postEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": key, "value": "v"})
		if w.Code != http.StatusBadRequest {
			t.Errorf("key %q: status = %d, want 400", key, w.Code)
		}
	}
	if len(api.createPatchReqs)+len(api.updatePatchReqs)+len(api.createStrategyReqs) != 0 {
		t.Errorf("invalid key reached the API; recorded writes = %d", len(api.createPatchReqs)+len(api.updatePatchReqs)+len(api.createStrategyReqs))
	}
}

func TestHandleSGCEnvRemove_DeletesLastOverride(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MAX_PLAYERS=30", PatchFormat: "properties"},
	}
	app := envTestApp(api)

	w := postEnv(t, app, app.handleSGCEnvRemove, "7", "env/remove", map[string]string{"key": "MAX_PLAYERS"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.deletePatchIDs) != 1 || api.deletePatchIDs[0] != 9 {
		t.Errorf("delete calls = %v, want [9]", api.deletePatchIDs)
	}
}

func TestHandleSGCEnvRemove_KeepsRemainingOverrides(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "A=1\nB=2", PatchFormat: "properties"},
	}
	app := envTestApp(api)

	w := postEnv(t, app, app.handleSGCEnvRemove, "7", "env/remove", map[string]string{"key": "A"})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusSeeOther)
	}
	if len(api.deletePatchIDs) != 0 {
		t.Errorf("patch with remaining overrides must not be deleted; deleted = %v", api.deletePatchIDs)
	}
	if len(api.updatePatchReqs) != 1 || api.updatePatchReqs[0].PatchContent != "B=2" {
		t.Errorf("update calls = %+v, want one update with content B=2", api.updatePatchReqs)
	}
}

func TestHandleSGCEnvEdit_RendersPrefilledForm(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MAX_PLAYERS=30", PatchFormat: "properties"},
	}
	app := envTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/sgc/7/env/edit?key=MAX_PLAYERS", nil)
	w := httptest.NewRecorder()
	app.handleSGCEnvEdit(w, req, "7")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"/sgc/7/env/set", `name="key" value="MAX_PLAYERS"`, `value="30"`} {
		if !strings.Contains(body, want) {
			t.Errorf("edit fragment missing %q", want)
		}
	}
}

func TestBuildSGCEnvOverridesData_LayeredView(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MAX_PLAYERS=30\nEXTRA=1", PatchFormat: "properties"},
	}
	app := envTestApp(api)

	data, envPatch, err := app.buildSGCEnvOverridesData(context.Background(), api.sgc, api.gc)
	if err != nil {
		t.Fatalf("buildSGCEnvOverridesData failed: %v", err)
	}
	if envPatch == nil || envPatch.PatchId != 9 {
		t.Fatalf("envPatch = %+v, want patch 9", envPatch)
	}
	if !data.HasTemplateKeys {
		t.Errorf("HasTemplateKeys = false, want true")
	}
	if len(data.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(data.Layers))
	}
	if data.Layers[0].Key != "EXTRA" || data.Layers[0].OverrideValue == nil || data.Layers[0].EffectiveValue != "1" {
		t.Errorf("EXTRA layer = %+v, want override-only with effective 1", data.Layers[0])
	}
	if data.Layers[0].TemplateValue != "" {
		t.Errorf("EXTRA template value = %q, want empty", data.Layers[0].TemplateValue)
	}
	if data.Layers[1].Key != "MAX_PLAYERS" || data.Layers[1].TemplateValue != "10" || data.Layers[1].EffectiveValue != "30" {
		t.Errorf("MAX_PLAYERS layer = %+v, want template 10 / effective 30", data.Layers[1])
	}
	if data.Layers[1].OverrideValue == nil || *data.Layers[1].OverrideValue != "30" {
		t.Errorf("MAX_PLAYERS OverrideValue = %v, want 30", data.Layers[1].OverrideValue)
	}
}

func TestParseAndSerializePropertiesContent(t *testing.T) {
	parsed := parsePropertiesContent("# comment\n\nA=1\nB = 2\nbroken-line\n")
	if len(parsed) != 2 || parsed["A"] != "1" || parsed["B"] != "2" {
		t.Errorf("parsed = %+v, want A=1 B=2 (comment, blank, and malformed lines skipped)", parsed)
	}
	if got := serializePropertiesContent(map[string]string{"B": "2", "A": "1"}); got != "A=1\nB=2" {
		t.Errorf("serialized = %q, want sorted A=1\\nB=2", got)
	}
}

func TestValidateEnvOverrideKeyAndValue(t *testing.T) {
	if err := validateEnvOverrideKey("GOOD_KEY"); err != nil {
		t.Errorf("GOOD_KEY rejected: %v", err)
	}
	for _, bad := range []string{"", "HAS SPACE", "EQ=UALS", "HASH#TAG"} {
		if err := validateEnvOverrideKey(bad); err == nil {
			t.Errorf("key %q accepted, want rejected", bad)
		}
	}
	if err := validateEnvOverrideValue("plain"); err != nil {
		t.Errorf("plain value rejected: %v", err)
	}
	if err := validateEnvOverrideValue("line\nbreak"); err == nil {
		t.Errorf("multiline value accepted, want rejected")
	}
}

// TestHandleSGCRoutes_EnvDispatch guards main.go's route wiring: the env
// paths route to the override handlers rather than falling through to
// handleSGCDetail.
func TestHandleSGCRoutes_EnvDispatch(t *testing.T) {
	api := newEnvTestFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "A=1", PatchFormat: "properties"},
	}
	app := envTestApp(api)

	req := httptest.NewRequest(http.MethodPost, "/sgc/7/env/remove", strings.NewReader("key=A"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleSGCRoutes(w, req)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("POST /sgc/7/env/remove status = %d, want %d (body: %s)", w.Code, http.StatusSeeOther, w.Body.String())
	}
	if len(api.deletePatchIDs) != 1 {
		t.Errorf("remove handler not dispatched; deletes = %v", api.deletePatchIDs)
	}
}
