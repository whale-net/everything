package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// --- Ports section (task #2275: FR12, the accumulating half of FR10,
// WD10) -----------------------------------------------------------------
//
// deploymentSettingsPortsTestApp wraps fakeGuidanceManManAPIClient
// (handlers_sgc_ports_guidance_test.go, task #2098) rather than standing
// up a fourth fixture in this file: FR12 requires reusing the shipped
// guidance surface unchanged, and that file's fixture is exactly what its
// own tests already exercise for ranges/allocated/sibling bindings.
// UpdateServerGameConfig was added to that fixture (see its own doc
// comment) specifically so these tests can share it instead of
// re-deriving the guidance data a second time.
func deploymentSettingsPortsTestApp(api *fakeGuidanceManManAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// postDeploymentSettingsPortsSave posts to the blade's one explicit Save
// (POST /deployment-settings/{id}/ports/save), exactly as
// deployment_settings.templ's ports form does: a single
// port_bindings_json field carrying the whole accumulated array.
func postDeploymentSettingsPortsSave(t *testing.T, app *App, sgcID, bindingsJSON string) *httptest.ResponseRecorder {
	t.Helper()
	form := url.Values{"port_bindings_json": {bindingsJSON}}
	req := httptest.NewRequest(http.MethodPost, "/deployment-settings/"+sgcID+"/ports/save", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleDeploymentSettingsPortsSave(w, req, sgcID)
	return w
}

// TestHandleDeploymentSettingsPortsSave_WritePath is the load-bearing
// write-path test: saving issues exactly one UpdateServerGameConfig call
// with update_paths == ["port_bindings"] -- asserted explicitly, since an
// empty update_paths takes a different branch server-side -- carrying the
// whole submitted array, and the response comes back clean (no
// data-blade-dirty="true"), so FR10 stops prompting on dismiss.
func TestHandleDeploymentSettingsPortsSave_WritePath(t *testing.T) {
	api := newGuidanceAPI()
	app := deploymentSettingsPortsTestApp(api)

	w := postDeploymentSettingsPortsSave(t, app, "7", `[{"container_port":25565,"host_port":25580,"protocol":"TCP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}

	if len(api.updateSGCReqs) != 1 {
		t.Fatalf("UpdateServerGameConfig calls = %d, want exactly 1", len(api.updateSGCReqs))
	}
	req := api.updateSGCReqs[0]
	if got := req.GetUpdatePaths(); len(got) != 1 || got[0] != "port_bindings" {
		t.Errorf(`update_paths = %v, want exactly ["port_bindings"]`, got)
	}
	if req.GetServerGameConfigId() != 7 {
		t.Errorf("ServerGameConfigId = %d, want 7", req.GetServerGameConfigId())
	}
	if got := req.GetPortBindings(); len(got) != 1 || got[0].GetHostPort() != 25580 || got[0].GetContainerPort() != 25565 || got[0].GetProtocol() != "TCP" {
		t.Errorf("PortBindings = %v, want exactly the submitted binding", got)
	}

	body := w.Body.String()
	if strings.Contains(body, `data-blade-dirty="true"`) {
		t.Errorf("expected the section to come back clean after a successful Save, got %q", body)
	}
	if !strings.Contains(body, `data-blade-section="ports" data-blade-dirty="false"`) {
		t.Errorf("expected the never-dirty-after-save marker, got %q", body)
	}
}

// TestHandleDeploymentSettingsPortsSave_ConnectAddressRecompute guards
// AC4: a successful Save swaps the recomputed connect address into the
// Games page row out of band, derived from the newly saved bindings (not
// the pre-save ones).
func TestHandleDeploymentSettingsPortsSave_ConnectAddressRecompute(t *testing.T) {
	api := newGuidanceAPI()
	api.server.HostPublicAddress = "play.example.com"
	app := deploymentSettingsPortsTestApp(api)

	w := postDeploymentSettingsPortsSave(t, app, "7", `[{"container_port":25565,"host_port":25599,"protocol":"TCP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `id="deployment-connect-7"`) {
		t.Fatalf("expected the connect-address OOB swap's target id, got %q", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("expected an out-of-band swap (AC4), got %q", body)
	}
	if !strings.Contains(body, "play.example.com:25599") {
		t.Errorf("expected the recomputed address from the newly saved binding, got %q", body)
	}
}

// TestHandleDeploymentSettingsPortsSave_Failure_PreservesBindingsAndDirty
// guards the failure path: a backend save-time rejection is surfaced as an
// error without losing the operator's entered bindings, and the section
// stays marked dirty (FR10 keeps prompting on dismiss).
func TestHandleDeploymentSettingsPortsSave_Failure_PreservesBindingsAndDirty(t *testing.T) {
	api := newGuidanceAPI()
	api.updateSGCErr = errors.New("port already allocated")
	app := deploymentSettingsPortsTestApp(api)

	w := postDeploymentSettingsPortsSave(t, app, "7", `[{"container_port":25565,"host_port":25599,"protocol":"TCP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (re-rendered fragment, not an error page); body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, `data-testid="deployment-settings-ports-error"`) {
		t.Errorf("expected an inline error, got %q", body)
	}
	if !strings.Contains(body, `data-blade-section="ports" data-blade-dirty="true"`) {
		t.Errorf("expected the section to stay dirty after a rejected Save (FR10 keeps prompting), got %q", body)
	}
	// The operator's just-submitted binding must survive verbatim in the
	// re-rendered fragment's data-port-bindings attribute -- never dropped.
	// templ.JSONString's output is HTML-attribute-escaped (&#34; not "),
	// so the assertion matches the escaped form actually served.
	if !strings.Contains(body, `&#34;host_port&#34;:25599`) {
		t.Errorf("expected the rejected binding to survive in the re-rendered fragment, got %q", body)
	}
}

// TestHandleDeploymentSettingsPortsSave_NoClientSideValidationAuthority
// guards FR12's "no new validation authority" rule end to end: a binding
// the guidance surface would flag (host port 25566 is already allocated
// per newGuidanceAPI's fixture) is still saved successfully when the
// backend accepts it -- this handler never rejects on its own.
func TestHandleDeploymentSettingsPortsSave_NoClientSideValidationAuthority(t *testing.T) {
	api := newGuidanceAPI()
	app := deploymentSettingsPortsTestApp(api)

	w := postDeploymentSettingsPortsSave(t, app, "7", `[{"container_port":25565,"host_port":25566,"protocol":"TCP"}]`)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.updateSGCReqs) != 1 {
		t.Fatalf("expected the backend-accepted save to go through, got %d update calls", len(api.updateSGCReqs))
	}
	body := w.Body.String()
	if strings.Contains(body, `data-testid="deployment-settings-ports-error"`) {
		t.Errorf("expected no client-side rejection of a guidance-flagged-but-backend-accepted binding, got %q", body)
	}
}

// TestHandleDeploymentSettingsPorts_GET_GuidanceSurface guards the lazy
// load's reuse of the shipped FR13/FR14 guidance (handlers_sgc_ports_
// guidance.go's buildSGCPortContext, unchanged here): allowed ranges,
// already-allocated ports, and sibling deployments' bindings on the same
// server all come through in the rendered context, sourced from
// newGuidanceAPI's fixture.
func TestHandleDeploymentSettingsPorts_GET_GuidanceSurface(t *testing.T) {
	api := newGuidanceAPI()
	app := deploymentSettingsPortsTestApp(api)

	req := httptest.NewRequest(http.MethodGet, "/deployment-settings/7/ports", nil)
	w := httptest.NewRecorder()
	app.handleDeploymentSettingsPorts(w, req, "7")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	// templ.JSONString's output is HTML-attribute-escaped (&#34; not "),
	// so these match the escaped form actually served, not raw JSON.
	// Allowed ranges (guidanceServer): TCP 25565-25570, UDP 27015-27020.
	for _, want := range []string{`&#34;start&#34;:25565`, `&#34;end&#34;:25570`, `&#34;start&#34;:27015`, `&#34;end&#34;:27020`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected allowed-range guidance %q, got %q", want, body)
		}
	}
	// Already-allocated (newGuidanceAPI.allocated): TCP 25566.
	if !strings.Contains(body, "25566") {
		t.Errorf("expected the already-allocated port to appear in guidance, got %q", body)
	}
	// Sibling deployment (SGC 8, same server) saved binding: TCP 30000.
	// SGC 7's own binding (25565) must be excluded from "in use" -- editing
	// a deployment's own bindings must not flag them against itself.
	if !strings.Contains(body, "30000") {
		t.Errorf("expected the sibling deployment's saved binding to appear in guidance, got %q", body)
	}
	// The deployment's own current bindings render too.
	if !strings.Contains(body, `&#34;host_port&#34;:25565`) {
		t.Errorf("expected the deployment's own current binding to render, got %q", body)
	}
}

// TestDeploymentSettingsPorts_FR2_NoRawSGCInDisplayText guards FR2 across
// both the lazy-load fragment and a post-Save fragment: no "SGC" or
// "server game config" (case-insensitive) anywhere the ports section
// renders as display text.
func TestDeploymentSettingsPorts_FR2_NoRawSGCInDisplayText(t *testing.T) {
	api := newGuidanceAPI()
	app := deploymentSettingsPortsTestApp(api)

	getW := httptest.NewRecorder()
	app.handleDeploymentSettingsPorts(getW, httptest.NewRequest(http.MethodGet, "/deployment-settings/7/ports", nil), "7")
	saveW := postDeploymentSettingsPortsSave(t, app, "7", `[{"container_port":25565,"host_port":25580,"protocol":"TCP"}]`)

	for name, w := range map[string]*httptest.ResponseRecorder{"GET": getW, "Save": saveW} {
		lower := strings.ToLower(w.Body.String())
		if strings.Contains(lower, "sgc") {
			t.Errorf("%s response: expected no raw SGC identifier in display text (FR2), got %q", name, w.Body.String())
		}
		if strings.Contains(lower, "server game config") {
			t.Errorf(`%s response: expected no "server game config" in display text (FR2), got %q`, name, w.Body.String())
		}
	}
}

// TestHandleDeploymentSettingsRoutes_PortsDispatch guards main.go's route
// wiring (NFR4: UI-only dispatch, no new API surface) for both the ports
// lazy-load and the ports Save branch -- the Save branch is checked before
// the bare "ports" branch in handleDeploymentSettingsRoutes since both
// share pathParts[2] == "ports", so this also guards that they don't
// shadow each other.
func TestHandleDeploymentSettingsRoutes_PortsDispatch(t *testing.T) {
	api := newGuidanceAPI()
	app := deploymentSettingsPortsTestApp(api)

	getReq := httptest.NewRequest(http.MethodGet, "/deployment-settings/7/ports", nil)
	getW := httptest.NewRecorder()
	app.handleDeploymentSettingsRoutes(getW, getReq)
	if getW.Code != http.StatusOK {
		t.Fatalf("GET dispatch: status = %d, want 200; body: %s", getW.Code, getW.Body.String())
	}
	if strings.Contains(getW.Body.String(), "Loading port bindings") {
		t.Errorf("GET dispatch: expected the real section, not the placeholder, got %q", getW.Body.String())
	}

	form := url.Values{"port_bindings_json": {`[{"container_port":25565,"host_port":25580,"protocol":"TCP"}]`}}
	saveReq := httptest.NewRequest(http.MethodPost, "/deployment-settings/7/ports/save", strings.NewReader(form.Encode()))
	saveReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	saveW := httptest.NewRecorder()
	app.handleDeploymentSettingsRoutes(saveW, saveReq)
	if saveW.Code != http.StatusOK {
		t.Fatalf("Save dispatch: status = %d, want 200; body: %s", saveW.Code, saveW.Body.String())
	}
	if len(api.updateSGCReqs) != 1 {
		t.Fatalf("Save dispatch: expected the Save branch to be reached (not the bare ports branch), got %d update calls", len(api.updateSGCReqs))
	}
}
