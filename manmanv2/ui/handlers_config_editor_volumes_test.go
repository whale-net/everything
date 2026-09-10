package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// Guards task #2363's Volumes tab assign/edit/remove routes
// (handlers_config_editor_volumes.go, FR14/FR15) end to end: the fake API
// client below is stateful (tracks BackupConfig rows by ID, mirroring
// CreateBackupConfig/UpdateBackupConfig/DeleteBackupConfig's real
// semantics in manmanv2/api/handlers/backup_config.go), unlike
// fakeConfigEditorAPIClient (handlers_config_editor_test.go), which never
// assigns a backup config to its fixture volume and always returns an
// empty ListBackupConfigs.

// fakeVolumeBackupAPIClient tracks every Create/Update/Delete call plus the
// resulting BackupConfig rows, so a test can assert both on the outbound
// RPC shape (mirrors fakeConfigEditorAPIClient's own convention in
// handlers_config_editor_test.go) and on what a subsequent
// ListBackupConfigs/re-render actually reflects.
type fakeVolumeBackupAPIClient struct {
	manmanpb.ManManAPIClient

	config  *manmanpb.GameConfig
	volumes []*manmanpb.GameConfigVolume
	configs []*manmanpb.BackupConfig
	nextID  int64

	createCalls []*manmanpb.CreateBackupConfigRequest
	updateCalls []*manmanpb.UpdateBackupConfigRequest
	deleteCalls []*manmanpb.DeleteBackupConfigRequest
}

func (f *fakeVolumeBackupAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.config}, nil
}

func (f *fakeVolumeBackupAPIClient) ListGameConfigVolumes(ctx context.Context, in *manmanpb.ListGameConfigVolumesRequest, opts ...grpc.CallOption) (*manmanpb.ListGameConfigVolumesResponse, error) {
	return &manmanpb.ListGameConfigVolumesResponse{Volumes: f.volumes}, nil
}

func (f *fakeVolumeBackupAPIClient) ListBackupConfigs(ctx context.Context, in *manmanpb.ListBackupConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupConfigsResponse, error) {
	var cfgs []*manmanpb.BackupConfig
	for _, c := range f.configs {
		if c.VolumeId == in.VolumeId {
			cfgs = append(cfgs, c)
		}
	}
	return &manmanpb.ListBackupConfigsResponse{Configs: cfgs}, nil
}

func (f *fakeVolumeBackupAPIClient) CreateBackupConfig(ctx context.Context, in *manmanpb.CreateBackupConfigRequest, opts ...grpc.CallOption) (*manmanpb.CreateBackupConfigResponse, error) {
	f.createCalls = append(f.createCalls, in)
	f.nextID++
	cfg := &manmanpb.BackupConfig{
		BackupConfigId: f.nextID,
		VolumeId:       in.VolumeId,
		CadenceMinutes: in.CadenceMinutes,
		BackupPath:     in.BackupPath,
		Enabled:        in.Enabled,
	}
	f.configs = append(f.configs, cfg)
	return &manmanpb.CreateBackupConfigResponse{Config: cfg}, nil
}

func (f *fakeVolumeBackupAPIClient) UpdateBackupConfig(ctx context.Context, in *manmanpb.UpdateBackupConfigRequest, opts ...grpc.CallOption) (*manmanpb.UpdateBackupConfigResponse, error) {
	f.updateCalls = append(f.updateCalls, in)
	for _, c := range f.configs {
		if c.BackupConfigId == in.BackupConfigId {
			c.CadenceMinutes = in.CadenceMinutes
			c.BackupPath = in.BackupPath
			c.Enabled = in.Enabled
			return &manmanpb.UpdateBackupConfigResponse{Config: c}, nil
		}
	}
	return nil, fmt.Errorf("fakeVolumeBackupAPIClient: backup config %d not found", in.BackupConfigId)
}

func (f *fakeVolumeBackupAPIClient) DeleteBackupConfig(ctx context.Context, in *manmanpb.DeleteBackupConfigRequest, opts ...grpc.CallOption) (*manmanpb.DeleteBackupConfigResponse, error) {
	f.deleteCalls = append(f.deleteCalls, in)
	kept := f.configs[:0:0]
	for _, c := range f.configs {
		if c.BackupConfigId != in.BackupConfigId {
			kept = append(kept, c)
		}
	}
	f.configs = kept
	return &manmanpb.DeleteBackupConfigResponse{}, nil
}

func volumeBackupTestApp(api *fakeVolumeBackupAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// newVolumeBackupFixtures seeds GameConfig(3, game 1) with two volumes --
// "world" (9) and "mods" (10) -- so remove/isolation assertions have a
// second volume's row to prove untouched.
func newVolumeBackupFixtures() *fakeVolumeBackupAPIClient {
	return &fakeVolumeBackupAPIClient{
		config: &manmanpb.GameConfig{
			ConfigId: 3,
			GameId:   1,
			Name:     "vanilla",
			Image:    "itzg/minecraft-server",
		},
		volumes: []*manmanpb.GameConfigVolume{
			{VolumeId: 9, ConfigId: 3, Name: "world", Description: "World data", ContainerPath: "/data", HostSubpath: "world", ReadOnly: false, VolumeType: "bind"},
			{VolumeId: 10, ConfigId: 3, Name: "mods", Description: "Mod jars", ContainerPath: "/mods", HostSubpath: "mods", ReadOnly: false, VolumeType: "bind"},
		},
	}
}

// seedBackupConfig directly appends a BackupConfig row to the fake's state
// (bypassing CreateBackupConfig/createCalls) so a test can start from an
// already-assigned volume.
func (f *fakeVolumeBackupAPIClient) seedBackupConfig(volumeID int64, cadenceMinutes int32, backupPath string, enabled bool) *manmanpb.BackupConfig {
	f.nextID++
	cfg := &manmanpb.BackupConfig{
		BackupConfigId: f.nextID,
		VolumeId:       volumeID,
		CadenceMinutes: cadenceMinutes,
		BackupPath:     backupPath,
		Enabled:        enabled,
	}
	f.configs = append(f.configs, cfg)
	return cfg
}

// postVolumeBackupAction posts to
// /games/{gameID}/configs/{configID}/volumes/{volumeID}/backup-config/{action}
// exactly as config_editor.templ's per-row assign/edit/remove forms do,
// calling the handler directly -- same convention as
// handlers_config_editor_test.go's postConfigEditorSave.
func postVolumeBackupAction(t *testing.T, app *App, gameID, configID, volumeID, action string, form map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	values := make([]string, 0, len(form))
	for k, v := range form {
		values = append(values, k+"="+v)
	}
	path := fmt.Sprintf("/games/%s/configs/%s/volumes/%s/backup-config/%s", gameID, configID, volumeID, action)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(strings.Join(values, "&")))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	app.handleConfigEditorVolumeBackupConfig(w, req, gameID, configID, volumeID, action)
	return w
}

// TestConfigEditorVolumes_Assign_PostsCreateAndRendersAssignment guards
// FR14's acceptance criterion 1: Assign posts CreateBackupConfig bound to
// the row's own volume and the re-rendered tab shows an Edit/Remove pair
// for that volume in place of the Assign control.
func TestConfigEditorVolumes_Assign_PostsCreateAndRendersAssignment(t *testing.T) {
	api := newVolumeBackupFixtures()
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "assign", map[string]string{
		"cadence_minutes": "1440",
		"backup_path":     "saves",
		"enabled":         "true",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.createCalls) != 1 {
		t.Fatalf("CreateBackupConfig calls = %d, want exactly 1", len(api.createCalls))
	}
	req := api.createCalls[0]
	if req.VolumeId != 9 {
		t.Errorf("CreateBackupConfig VolumeId = %d, want 9 (this row's own volume)", req.VolumeId)
	}
	if req.CadenceMinutes != 1440 || req.BackupPath != "saves" || !req.Enabled {
		t.Errorf("CreateBackupConfig = %+v, want cadence=1440 path=saves enabled=true", req)
	}

	body := w.Body.String()
	if !strings.Contains(body, "data-volume-backup-edit-form") {
		t.Errorf("expected the re-rendered tab to show an edit control for the now-assigned volume, got %q", body)
	}
	if !strings.Contains(body, "data-volume-backup-remove-form") {
		t.Errorf("expected the re-rendered tab to show a remove control for the now-assigned volume, got %q", body)
	}
	if !strings.Contains(body, `value="saves"`) {
		t.Errorf("expected the assigned backup path to render, got %q", body)
	}
	if got := strings.Count(body, "data-volume-backup-assign-form"); got != 1 {
		// The other fixture volume (10, "mods") is still unassigned, so
		// exactly one Assign form should remain -- volume 10's, not 9's.
		t.Errorf("expected exactly 1 remaining assign form (volume 10, still unassigned), got %d in %q", got, body)
	}
}

// TestConfigEditorVolumes_Assign_RejectsNonPositiveCadence guards the
// shared form-validation helper (parseConfigEditorBackupForm): a
// validation rejection writes 400 and issues no RPC -- this is normal
// control flow (a rejected form submission), never a WARNING/ERROR-level
// log per AGENTS.md's logging-levels convention.
func TestConfigEditorVolumes_Assign_RejectsNonPositiveCadence(t *testing.T) {
	api := newVolumeBackupFixtures()
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "assign", map[string]string{
		"cadence_minutes": "0",
		"backup_path":     "saves",
		"enabled":         "true",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a non-positive cadence", w.Code)
	}
	if len(api.createCalls) != 0 {
		t.Errorf("CreateBackupConfig calls = %d, want 0 (validation must block the assign)", len(api.createCalls))
	}
}

// TestConfigEditorVolumes_Edit_PersistsCadencePathEnabled guards FR14's
// acceptance criterion 2: editing an existing assignment's volume-level
// settings inline persists cadence/path/enabled via UpdateBackupConfig and
// the re-render reflects the new values, with no navigation out of the
// Blade.
func TestConfigEditorVolumes_Edit_PersistsCadencePathEnabled(t *testing.T) {
	api := newVolumeBackupFixtures()
	seeded := api.seedBackupConfig(9, 1440, ".", true)
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "edit", map[string]string{
		"backup_config_id": fmt.Sprintf("%d", seeded.BackupConfigId),
		"cadence_minutes":  "60",
		"backup_path":      "world",
		"enabled":          "false",
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.updateCalls) != 1 {
		t.Fatalf("UpdateBackupConfig calls = %d, want exactly 1", len(api.updateCalls))
	}
	req := api.updateCalls[0]
	if req.BackupConfigId != seeded.BackupConfigId {
		t.Errorf("UpdateBackupConfig BackupConfigId = %d, want %d", req.BackupConfigId, seeded.BackupConfigId)
	}
	if req.CadenceMinutes != 60 || req.BackupPath != "world" || req.Enabled {
		t.Errorf("UpdateBackupConfig = %+v, want cadence=60 path=world enabled=false", req)
	}

	body := w.Body.String()
	if !strings.Contains(body, `value="world"`) {
		t.Errorf("expected the updated backup path to render, got %q", body)
	}
	if !strings.Contains(body, `value="60"`) {
		t.Errorf("expected the updated cadence to render, got %q", body)
	}
	if !strings.Contains(body, "data-volume-backup-edit-form") {
		t.Errorf("expected the volume to stay in the edit/remove state after an edit, got %q", body)
	}
}

// TestConfigEditorVolumes_Edit_RejectsMissingBackupConfigID guards the
// edit/remove forms' shared backup_config_id parsing (parseConfigEditor
// BackupConfigID): an invalid/missing id writes 400 and issues no RPC.
func TestConfigEditorVolumes_Edit_RejectsMissingBackupConfigID(t *testing.T) {
	api := newVolumeBackupFixtures()
	api.seedBackupConfig(9, 1440, ".", true)
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "edit", map[string]string{
		"backup_config_id": "not-a-number",
		"cadence_minutes":  "60",
		"backup_path":      "world",
		"enabled":          "false",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for an invalid backup_config_id", w.Code)
	}
	if len(api.updateCalls) != 0 {
		t.Errorf("UpdateBackupConfig calls = %d, want 0", len(api.updateCalls))
	}
}

// TestConfigEditorVolumes_Remove_ClearsAssignmentAndScopesToThisVolume
// guards FR15's acceptance criterion 1 (the assignment is gone from the
// Volumes tab -- the row goes back to showing an Assign control) and the
// isolation half of NFR4/FR15 (removal never touches another volume's row
// or another config's row).
//
// It deliberately does NOT assert that DeleteBackupConfig goes uncalled:
// under this schema (BackupConfig.VolumeID is a required FK set once at
// Create with no update path -- see ConfigEditorVolume's doc comment,
// manmanv2/ui/pages/config_editor.templ, and
// manmanv2/migrate/migrations/029_backup_configs.up.sql), a BackupConfig
// row *is* the volume's assignment; there is no separate, cross-volume
// reusable "template" row a removal could detach from while leaving
// intact. "Remove" can therefore only mean deleting this volume's own row
// -- which is what handleConfigEditorVolumeBackupConfig's "remove" branch
// does (handlers_config_editor_volumes.go). This is a genuine conflict
// with FR15's literal acceptance criterion 3 ("leaves the underlying
// BackupConfig template intact"), which cannot be satisfied without a
// schema change; flagged via scope note per the issue's own contingency
// ("if you conclude it cannot, stop and file a scope note ... rather than
// absorbing a schema change silently") rather than weakened or silently
// dropped here.
func TestConfigEditorVolumes_Remove_ClearsAssignmentAndScopesToThisVolume(t *testing.T) {
	api := newVolumeBackupFixtures()
	target := api.seedBackupConfig(9, 1440, ".", true)
	other := api.seedBackupConfig(10, 60, "mods", true)
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "remove", map[string]string{
		"backup_config_id": fmt.Sprintf("%d", target.BackupConfigId),
	})

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", w.Code, w.Body.String())
	}
	if len(api.deleteCalls) != 1 {
		t.Fatalf("DeleteBackupConfig calls = %d, want exactly 1", len(api.deleteCalls))
	}
	if api.deleteCalls[0].BackupConfigId != target.BackupConfigId {
		t.Errorf("DeleteBackupConfig BackupConfigId = %d, want %d (only this volume's row)", api.deleteCalls[0].BackupConfigId, target.BackupConfigId)
	}

	// The other volume's own assignment must survive untouched.
	stillPresent := false
	for _, c := range api.configs {
		if c.BackupConfigId == other.BackupConfigId {
			stillPresent = true
		}
	}
	if !stillPresent {
		t.Errorf("expected volume 10's own backup config (%d) to survive volume 9's remove, but it was deleted", other.BackupConfigId)
	}

	// Volume 10 keeps its own assignment (its edit/remove routes still
	// target volume 10), so the negative assertion must be scoped to
	// volume 9's own routes rather than a body-wide search.
	body := w.Body.String()
	if strings.Contains(body, "/games/1/configs/3/volumes/9/backup-config/edit") || strings.Contains(body, "/games/1/configs/3/volumes/9/backup-config/remove") {
		t.Errorf("expected volume 9 to show no edit/remove control after its assignment was removed, got %q", body)
	}
	if !strings.Contains(body, "/games/1/configs/3/volumes/9/backup-config/assign") {
		t.Errorf("expected volume 9 to show an assign control again after removal, got %q", body)
	}
	if !strings.Contains(body, "/games/1/configs/3/volumes/10/backup-config/edit") || !strings.Contains(body, "/games/1/configs/3/volumes/10/backup-config/remove") {
		t.Errorf("expected volume 10's own assignment to still render edit/remove controls, untouched by volume 9's removal, got %q", body)
	}
}

// TestConfigEditorVolumes_Remove_RejectsMissingBackupConfigID mirrors
// TestConfigEditorVolumes_Edit_RejectsMissingBackupConfigID for the remove
// branch.
func TestConfigEditorVolumes_Remove_RejectsMissingBackupConfigID(t *testing.T) {
	api := newVolumeBackupFixtures()
	api.seedBackupConfig(9, 1440, ".", true)
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "remove", map[string]string{
		"backup_config_id": "",
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for a missing backup_config_id", w.Code)
	}
	if len(api.deleteCalls) != 0 {
		t.Errorf("DeleteBackupConfig calls = %d, want 0", len(api.deleteCalls))
	}
}

// TestConfigEditorVolumes_UnknownAction guards the dispatcher's default
// branch (handleConfigEditorVolumeBackupConfig): an action other than
// assign/edit/remove 404s and issues no RPC.
func TestConfigEditorVolumes_UnknownAction(t *testing.T) {
	api := newVolumeBackupFixtures()
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "delete-forever", map[string]string{})

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unknown action", w.Code)
	}
	if len(api.createCalls) != 0 || len(api.updateCalls) != 0 || len(api.deleteCalls) != 0 {
		t.Errorf("expected no RPC for an unknown action, got creates=%d updates=%d deletes=%d", len(api.createCalls), len(api.updateCalls), len(api.deleteCalls))
	}
}

// TestConfigEditorVolumes_DirtyGuardWiring guards FR10: the Volumes tab is
// its own dirty-tracked Blade section (data-blade-section="volumes"),
// independent of "basics-environment", on every re-render this file's
// routes produce -- mirrors config_editor_test.go's (pages package)
// TestConfigEditor_FR10_DirtyMarkerReflectsData assertion style for the
// Basics/Environment section. The section always renders
// data-blade-dirty="false" server-side (config_editor.templ) -- an
// in-progress, not-yet-submitted edit is what marks it dirty client-side
// (the markDirty listener in config_editor.templ's script) -- so this pins
// the wiring the script depends on, not the client-side toggle itself.
func TestConfigEditorVolumes_DirtyGuardWiring(t *testing.T) {
	api := newVolumeBackupFixtures()
	app := volumeBackupTestApp(api)

	w := postVolumeBackupAction(t, app, "1", "3", "9", "assign", map[string]string{
		"cadence_minutes": "1440",
		"backup_path":     "saves",
		"enabled":         "true",
	})

	body := w.Body.String()
	if !strings.Contains(body, `data-blade-section="volumes" data-blade-dirty="false"`) {
		t.Errorf("expected the Volumes section wired as its own dirty-tracked Blade section, got %q", body)
	}
}

// TestBackupConfigStandaloneRoutes_UnchangedByVolumesTab guards NFR4/NFR6:
// the pre-existing standalone BackupConfig routes
// (handleBackupConfigCreate at POST /backup-configs/create,
// handleBackupConfigDelete at POST /backup-configs/{id}/delete,
// manmanv2/ui/main.go) behave identically after #2363 -- neither handler
// was touched by this task, but the ControlClient/fake API surface they
// depend on was extended (UpdateBackupConfig added), so this pins that the
// two routes this task must not reproduce or diverge from (NFR4) still
// work end to end through the same grpc client.
func TestBackupConfigStandaloneRoutes_UnchangedByVolumesTab(t *testing.T) {
	api := newVolumeBackupFixtures()
	existing := api.seedBackupConfig(9, 1440, ".", true)
	app := volumeBackupTestApp(api)

	// POST /backup-configs/create
	createReq := httptest.NewRequest(http.MethodPost, "/backup-configs/create", strings.NewReader(
		"volume_id=10&cadence_minutes=30&backup_path=mods&enabled=true&redirect_url=%2Fgames%2F1%2Fconfigs%2F3",
	))
	createReq.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	createReq.Header.Set("HX-Request", "true")
	createW := httptest.NewRecorder()
	app.handleBackupConfigCreate(createW, createReq)

	if createW.Code != http.StatusOK {
		t.Fatalf("standalone create status = %d, want 200; body: %s", createW.Code, createW.Body.String())
	}
	if got := createW.Header().Get("HX-Redirect"); got != "/games/1/configs/3" {
		t.Errorf("standalone create HX-Redirect = %q, want the submitted redirect_url", got)
	}
	if len(api.createCalls) != 1 || api.createCalls[0].VolumeId != 10 {
		t.Fatalf("expected exactly 1 CreateBackupConfig call for volume 10, got %+v", api.createCalls)
	}

	// POST /backup-configs/{id}/delete
	deleteReq := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/backup-configs/%d/delete", existing.BackupConfigId), nil)
	deleteReq.Header.Set("HX-Request", "true")
	deleteReq.Header.Set("Referer", "/games/1/configs/3")
	deleteW := httptest.NewRecorder()
	app.handleBackupConfigDelete(deleteW, deleteReq)

	if deleteW.Code != http.StatusOK {
		t.Fatalf("standalone delete status = %d, want 200; body: %s", deleteW.Code, deleteW.Body.String())
	}
	if got := deleteW.Header().Get("HX-Redirect"); got != "/games/1/configs/3" {
		t.Errorf("standalone delete HX-Redirect = %q, want the request Referer", got)
	}
	if len(api.deleteCalls) != 1 || api.deleteCalls[0].BackupConfigId != existing.BackupConfigId {
		t.Fatalf("expected exactly 1 DeleteBackupConfig call for %d, got %+v", existing.BackupConfigId, api.deleteCalls)
	}
}
