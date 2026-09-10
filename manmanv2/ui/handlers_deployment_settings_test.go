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

// Guards task #2274's Deployment Settings blade -- environment half
// (FR11, NFR5, NFR7). NFR5/LB4 is the load-bearing constraint: every
// write this section makes must be a single-key set/remove call, never a
// batch -- these tests drive the blade's own hx-post wiring end-to-end
// (handleSGCEnvSet/handleSGCEnvRemove with HX-Request set, exactly as
// deployment_settings.templ's forms send it) and assert on the *outbound
// request shape* recorded by the fake API client, not merely on the
// resulting database rows (a bulk write would still produce
// correct-looking rows -- see handlers_sgc_env_test.go's existing
// coverage of that path, unchanged by this task).

// fakeDeploymentSettingsAPIClient is a standalone fixture (deliberately
// not a reuse of handlers_sgc_env_test.go's fakeEnvAPIClient, so that
// file's "pass unchanged" requirement from issue #2274 is trivially true
// -- nothing in it is touched here) covering the additional reads
// buildDeploymentSettingsData needs (GetServer, ListSessions) alongside
// the same env override CRUD surface.
type fakeDeploymentSettingsAPIClient struct {
	manmanpb.ManManAPIClient

	sgc        *manmanpb.ServerGameConfig
	gc         *manmanpb.GameConfig
	server     *manmanpb.Server
	sessions   []*manmanpb.Session
	strategies []*manmanpb.ConfigurationStrategy
	patches    []*manmanpb.ConfigurationPatch

	createStrategyReqs []*manmanpb.CreateConfigurationStrategyRequest
	createPatchReqs    []*manmanpb.CreateConfigurationPatchRequest
	updatePatchReqs    []*manmanpb.UpdateConfigurationPatchRequest
	deletePatchIDs     []int64
}

func (f *fakeDeploymentSettingsAPIClient) GetServerGameConfig(ctx context.Context, in *manmanpb.GetServerGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetServerGameConfigResponse, error) {
	return &manmanpb.GetServerGameConfigResponse{Config: f.sgc}, nil
}

func (f *fakeDeploymentSettingsAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.gc}, nil
}

func (f *fakeDeploymentSettingsAPIClient) GetServer(ctx context.Context, in *manmanpb.GetServerRequest, opts ...grpc.CallOption) (*manmanpb.GetServerResponse, error) {
	return &manmanpb.GetServerResponse{Server: f.server}, nil
}

func (f *fakeDeploymentSettingsAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *fakeDeploymentSettingsAPIClient) ListConfigurationStrategies(ctx context.Context, in *manmanpb.ListConfigurationStrategiesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationStrategiesResponse, error) {
	return &manmanpb.ListConfigurationStrategiesResponse{Strategies: f.strategies}, nil
}

func (f *fakeDeploymentSettingsAPIClient) CreateConfigurationStrategy(ctx context.Context, in *manmanpb.CreateConfigurationStrategyRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationStrategyResponse, error) {
	f.createStrategyReqs = append(f.createStrategyReqs, in)
	next := int64(100 + len(f.createStrategyReqs))
	strategy := &manmanpb.ConfigurationStrategy{StrategyId: next, GameId: in.GameId, StrategyType: in.StrategyType}
	f.strategies = append(f.strategies, strategy)
	return &manmanpb.CreateConfigurationStrategyResponse{Strategy: strategy}, nil
}

func (f *fakeDeploymentSettingsAPIClient) ListConfigurationPatches(ctx context.Context, in *manmanpb.ListConfigurationPatchesRequest, opts ...grpc.CallOption) (*manmanpb.ListConfigurationPatchesResponse, error) {
	return &manmanpb.ListConfigurationPatchesResponse{Patches: f.patches}, nil
}

func (f *fakeDeploymentSettingsAPIClient) CreateConfigurationPatch(ctx context.Context, in *manmanpb.CreateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.CreateConfigurationPatchResponse, error) {
	f.createPatchReqs = append(f.createPatchReqs, in)
	patch := &manmanpb.ConfigurationPatch{
		PatchId:      int64(200 + len(f.createPatchReqs)),
		StrategyId:   in.StrategyId,
		PatchLevel:   in.PatchLevel,
		EntityId:     in.EntityId,
		PatchContent: in.PatchContent,
		PatchFormat:  in.PatchFormat,
		UpdatedAt:    int64(1000 + len(f.createPatchReqs)),
	}
	f.patches = append(f.patches, patch)
	return &manmanpb.CreateConfigurationPatchResponse{Patch: patch}, nil
}

func (f *fakeDeploymentSettingsAPIClient) UpdateConfigurationPatch(ctx context.Context, in *manmanpb.UpdateConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.UpdateConfigurationPatchResponse, error) {
	f.updatePatchReqs = append(f.updatePatchReqs, in)
	for _, p := range f.patches {
		if p.PatchId == in.PatchId {
			p.PatchContent = in.PatchContent
			p.PatchFormat = in.PatchFormat
			p.PatchOrder = in.PatchOrder
			p.UpdatedAt = int64(1000 + len(f.createPatchReqs) + len(f.updatePatchReqs))
		}
	}
	return &manmanpb.UpdateConfigurationPatchResponse{Patch: &manmanpb.ConfigurationPatch{PatchId: in.PatchId, PatchContent: in.PatchContent, PatchFormat: in.PatchFormat, PatchOrder: in.PatchOrder}}, nil
}

func (f *fakeDeploymentSettingsAPIClient) DeleteConfigurationPatch(ctx context.Context, in *manmanpb.DeleteConfigurationPatchRequest, opts ...grpc.CallOption) (*manmanpb.DeleteConfigurationPatchResponse, error) {
	f.deletePatchIDs = append(f.deletePatchIDs, in.PatchId)
	kept := f.patches[:0]
	for _, p := range f.patches {
		if p.PatchId != in.PatchId {
			kept = append(kept, p)
		}
	}
	f.patches = kept
	return &manmanpb.DeleteConfigurationPatchResponse{}, nil
}

func deploymentSettingsTestApp(api *fakeDeploymentSettingsAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// newDeploymentSettingsFixtures mirrors handlers_sgc_env_test.go's
// newEnvTestFixtures shape: SGC(7) on GameConfig(3, game 1), server 2,
// one env_vars strategy, no patches yet.
func newDeploymentSettingsFixtures() *fakeDeploymentSettingsAPIClient {
	return &fakeDeploymentSettingsAPIClient{
		sgc: &manmanpb.ServerGameConfig{ServerGameConfigId: 7, ServerId: 2, GameConfigId: 3},
		gc:  &manmanpb.GameConfig{ConfigId: 3, GameId: 1, Name: "vanilla", EnvTemplate: map[string]string{"MAX_PLAYERS": "10"}},
		server: &manmanpb.Server{
			ServerId: 2, Name: "host-01",
		},
		strategies: []*manmanpb.ConfigurationStrategy{
			{StrategyId: 55, GameId: 1, StrategyType: "env_vars", Name: "Environment Variables"},
		},
	}
}

// postDeploymentSettingsEnv posts form values to a POST
// /sgc/{id}/{action} handler with an HX-Request header set, exactly as
// deployment_settings.templ's forms do.
func postDeploymentSettingsEnv(t *testing.T, app *App, handler func(http.ResponseWriter, *http.Request, string), sgcID, action string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/sgc/"+sgcID+"/"+action, strings.NewReader(formEncode(form)))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	handler(w, req, sgcID)
	return w
}

// TestDeploymentSettingsBlade_NFR5_PerKeyRequestGranularity is the
// load-bearing NFR5/LB4 test: driving the blade through several key
// edits must always be N single-key set/remove calls, never one call
// carrying a map/array of keys, and the section stays a fragment
// response (200), never a redirect (which would navigate the underlying
// Games page away, breaking the blade -- FR10).
func TestDeploymentSettingsBlade_NFR5_PerKeyRequestGranularity(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	edits := []struct {
		action string
		form   map[string]string
	}{
		{"env/set", map[string]string{"key": "DIFFICULTY", "value": "hard"}},
		{"env/set", map[string]string{"key": "MOTD", "value": "welcome"}},
		{"env/set", map[string]string{"key": "MAX_PLAYERS", "value": "30"}},
		{"env/remove", map[string]string{"key": "MOTD"}},
	}

	for i, edit := range edits {
		var w *httptest.ResponseRecorder
		if edit.action == "env/set" {
			w = postDeploymentSettingsEnv(t, app, app.handleSGCEnvSet, "7", edit.action, edit.form)
		} else {
			w = postDeploymentSettingsEnv(t, app, app.handleSGCEnvRemove, "7", edit.action, edit.form)
		}
		if w.Code != http.StatusOK {
			t.Fatalf("edit %d (%s %+v): status = %d, want 200 (fragment, not a redirect); body: %s", i, edit.action, edit.form, w.Code, w.Body.String())
		}
		if loc := w.Header().Get("Location"); loc != "" {
			t.Errorf("edit %d: unexpected redirect Location %q -- the blade must never navigate away (FR10)", i, loc)
		}
	}

	// Four requests in, exactly four write calls total -- each carrying
	// exactly one key, never a batch. (1 create + 2 updates for the sets
	// after the first, +1 update for the remove that leaves keys behind.)
	totalWrites := len(api.createPatchReqs) + len(api.updatePatchReqs)
	if totalWrites != len(edits) {
		t.Fatalf("total patch write calls = %d, want %d (one per edit, no batching)", totalWrites, len(edits))
	}
	for i, req := range api.createPatchReqs {
		if got := len(parsePropertiesContent(req.PatchContent)); got != 1 {
			t.Errorf("createPatchReqs[%d] carried %d keys (%q), want exactly 1 -- a bulk write would look correct in storage but must never happen at the wire", i, got, req.PatchContent)
		}
	}
	// Each update call's content reflects incremental single-key merges,
	// not a single request asserting the whole map at once: this is
	// exercised precisely by handlers_sgc_env_test.go's existing
	// TestHandleSGCEnvSet_UpdatesExistingPatch (unchanged by this task) --
	// this test's job is only to confirm the same per-key contract holds
	// when driven through the blade's HX-Request path, not to re-derive it.

	if len(api.deletePatchIDs) != 0 {
		t.Errorf("no override list emptied in this sequence; unexpected deletes = %v", api.deletePatchIDs)
	}
}

// TestDeploymentSettingsBlade_OnePatchInvariant guards FR11's exactly-one-
// patch rule: editing several keys in sequence through the blade never
// creates a second ConfigurationPatch at patch_level=server_game_config.
func TestDeploymentSettingsBlade_OnePatchInvariant(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	for _, kv := range []struct{ key, value string }{
		{"DIFFICULTY", "hard"},
		{"MOTD", "welcome"},
		{"MAX_PLAYERS", "30"},
	} {
		postDeploymentSettingsEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": kv.key, "value": kv.value})
	}

	if len(api.createPatchReqs) != 1 {
		t.Fatalf("CreateConfigurationPatch calls = %d, want exactly 1 (first key only)", len(api.createPatchReqs))
	}
	if len(api.patches) != 1 {
		t.Fatalf("patches on the fake = %d, want exactly 1", len(api.patches))
	}
}

// TestDeploymentSettingsBlade_AbsentVsEmptyOverride_RoundTrips guards
// FR11's absent-vs-empty distinction end to end: overriding to the empty
// string persists and renders as overridden (not inherited); removing it
// then reverts to inherited.
func TestDeploymentSettingsBlade_AbsentVsEmptyOverride_RoundTrips(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	w := postDeploymentSettingsEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "MOTD", "value": ""})
	if w.Code != http.StatusOK {
		t.Fatalf("set-to-empty: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `name="key" value="MOTD"`) {
		t.Fatalf("expected MOTD's row in the response fragment, got %q", body)
	}
	if !strings.Contains(body, "overridden") {
		t.Errorf("override-to-empty must render as overridden, not inherited: %q", body)
	}
	if got := strings.Count(body, "/sgc/7/env/remove"); got != 1 {
		t.Errorf("expected a Remove form for the empty-string override, got %d in %q", got, body)
	}
	if len(api.patches) != 1 || api.patches[0].PatchContent != "MOTD=" {
		t.Fatalf("persisted patch content = %+v, want exactly one patch with content \"MOTD=\"", api.patches)
	}

	w = postDeploymentSettingsEnv(t, app, app.handleSGCEnvRemove, "7", "env/remove", map[string]string{"key": "MOTD"})
	if w.Code != http.StatusOK {
		t.Fatalf("remove: status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body = w.Body.String()
	if strings.Contains(body, "/sgc/7/env/remove") {
		t.Errorf("expected no Remove form once MOTD reverts to inherited, got %q", body)
	}
	if len(api.deletePatchIDs) != 1 {
		t.Fatalf("removing the last override must delete the patch; deletes = %v", api.deletePatchIDs)
	}
}

// TestDeploymentSettingsBlade_WhitespaceHonesty guards FR11's whitespace
// clause: an override saved with leading/trailing whitespace does not
// render back as if the untrimmed value had been saved (properties
// parsing trims what it writes raw -- this section must surface that
// truth, not paper over it).
func TestDeploymentSettingsBlade_WhitespaceHonesty(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	w := postDeploymentSettingsEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "MOTD", "value": "  padded  "})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if strings.Contains(body, `value="  padded  "`) {
		t.Errorf("rendered the untrimmed value as if it round-tripped verbatim -- must surface the parser's actual (trimmed) persisted value: %q", body)
	}
	if !strings.Contains(body, `value="padded"`) {
		t.Errorf("expected the trimmed persisted value to render (parsePropertiesContent trims), got %q", body)
	}
}

// TestDeploymentSettingsBlade_PendingHint reuses the exact fixtures shape
// from handlers_sgc_env_pending_test.go (task #2096's FR3 clearing
// rule): the blade shows the same pendingEnvOverrideHint signal the SGC
// page shows, never a second drift concept.
func TestDeploymentSettingsBlade_PendingHint(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MAX_PLAYERS=30", PatchFormat: "properties", UpdatedAt: 1000},
	}
	// A running session whose start predates the save: the hint stays visible.
	api.sessions = []*manmanpb.Session{{SessionId: 1, ServerGameConfigId: 7, Status: "running", StartedAt: 500}}
	app := deploymentSettingsTestApp(api)

	w := postDeploymentSettingsEnv(t, app, app.handleSGCEnvRemove, "7", "env/remove", map[string]string{"key": "DOES_NOT_EXIST"})
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "pending override edit applies on next session start") {
		t.Errorf("expected the pending hint while a running session predates the save, got %q", w.Body.String())
	}
}

// TestDeploymentSettingsBlade_FR10_NoUnsavedChangesPrompt guards FR10's
// interaction with FR11: after a commit, the fragment's env section still
// carries the never-dirty marker, so dismissing the blade shows no
// unsaved-changes prompt (#2269's Blade only prompts on
// data-blade-dirty="true").
func TestDeploymentSettingsBlade_FR10_NoUnsavedChangesPrompt(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	w := postDeploymentSettingsEnv(t, app, app.handleSGCEnvSet, "7", "env/set", map[string]string{"key": "DIFFICULTY", "value": "hard"})
	body := w.Body.String()
	if !strings.Contains(body, `data-blade-section="env" data-blade-dirty="false"`) {
		t.Errorf("expected the post-commit fragment to stay marked never-dirty, got %q", body)
	}
	if strings.Contains(body, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty marker after a commit (WD9: nothing accumulates), got %q", body)
	}
}

// TestHandleDeploymentSettingsEnv_GET guards the blade's lazy initial
// load (NFR7): GET /deployments/{id}/env renders the FR2-named, layered
// environment section.
func TestHandleDeploymentSettingsEnv_GET(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	api.patches = []*manmanpb.ConfigurationPatch{
		{PatchId: 9, StrategyId: 55, PatchLevel: "server_game_config", EntityId: 7, PatchContent: "MAX_PLAYERS=30", PatchFormat: "properties"},
	}
	app := deploymentSettingsTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/deployment-settings/7/env", nil)
	w := httptest.NewRecorder()
	app.handleDeploymentSettingsEnv(w, req, "7")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	for _, want := range []string{"MAX_PLAYERS", "30", "overridden"} {
		if !strings.Contains(body, want) {
			t.Errorf("fragment missing %q, got %q", want, body)
		}
	}
}

// TestHandleDeploymentSettingsRoutes_EnvDispatch guards main.go's route
// wiring for the new UI-only /deployments/ prefix (NFR4: no new
// API/gRPC surface, this is purely a mux dispatch addition -- and
// deliberately a separate prefix from /sgc/*, see
// handleDeploymentSettingsRoutes's doc comment for why).
func TestHandleDeploymentSettingsRoutes_EnvDispatch(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/deployment-settings/7/env", nil)
	w := httptest.NewRecorder()
	app.handleDeploymentSettingsRoutes(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (dispatched to handleDeploymentSettingsEnv); body: %s", w.Code, w.Body.String())
	}
}

// TestBuildDeploymentSettingsData_FR2_DisplayName guards FR2's naming:
// "<config> on <server>", never a raw SGC identifier, with sane fallbacks
// when a name is missing.
func TestBuildDeploymentSettingsData_FR2_DisplayName(t *testing.T) {
	api := newDeploymentSettingsFixtures()
	app := deploymentSettingsTestApp(api)

	data, err := app.buildDeploymentSettingsData(context.Background(), api.sgc, api.gc)
	if err != nil {
		t.Fatalf("buildDeploymentSettingsData failed: %v", err)
	}
	if data.DisplayName != "vanilla on host-01" {
		t.Errorf("DisplayName = %q, want %q", data.DisplayName, "vanilla on host-01")
	}
	if data.SGCID != 7 {
		t.Errorf("SGCID = %d, want 7", data.SGCID)
	}

	// Fallback naming when the config/server have no name set.
	api2 := newDeploymentSettingsFixtures()
	api2.gc.Name = ""
	api2.server.Name = ""
	app2 := deploymentSettingsTestApp(api2)
	data2, err := app2.buildDeploymentSettingsData(context.Background(), api2.sgc, api2.gc)
	if err != nil {
		t.Fatalf("buildDeploymentSettingsData failed: %v", err)
	}
	if data2.DisplayName != "config 3 on server 2" {
		t.Errorf("fallback DisplayName = %q, want %q", data2.DisplayName, "config 3 on server 2")
	}
}
