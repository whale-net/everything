package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// This file guards task #2812 (root plan #2777, M7): the fleet-wide
// "/backups" surface shell and its first tab, the backup-run list (FR1,
// FR3, FR4, NFR3, NFR4). Unauthenticated-request coverage for "/backups"
// and "/backups/runs" lives in main_test.go's
// TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated route table
// (NFR3), not duplicated here.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// changing backupRunOrigin's empty-string branch to `return "manual"`
// (pages/backups.templ) made
// TestHandleBackupRunsFragment_TriggerOriginRendering's "unknown (empty
// trigger_source)" subcase fail on the "must not render as manual"
// assertion below; reverting restored green. See pages/backups_test.go's
// TestBackupRunOrigin_UnknownNeverRendersAsManual for the same rule guarded
// at the unit level.

// fakeBackupsFleetAPIClient is scoped to handleBackupsPage/
// handleBackupRunsFragment's call graph: ListServers and
// ListServerGameConfigs (resolveFleetWideActivitySet), GetGameConfig
// (activityDisplayNames), ListBackupConfigs (ListBackupConfigItems) for the
// filter bar's name-based pickers, and ListBackups for the row data itself.
// Any other call panics on the nil embedded interface, matching
// handlers_activity_test.go's fakeActivityAPIClient convention.
type fakeBackupsFleetAPIClient struct {
	manmanpb.ManManAPIClient

	servers         []*manmanpb.Server
	sgcsByServerID  map[int64][]*manmanpb.ServerGameConfig
	gameConfigsByID map[int64]*manmanpb.GameConfig

	backupConfigItems []*manmanpb.BackupConfigListItem

	listBackupsResp *manmanpb.ListBackupsResponse
	listBackupsErr  error
	// lastListBackupsReq captures the most recent ListBackups request so
	// tests can assert a query-string filter was forwarded onto the right
	// request field (FR4), not just that the call happened.
	lastListBackupsReq *manmanpb.ListBackupsRequest

	// triggerBackupResp/triggerBackupErr back TriggerBackup (task #2813,
	// FR5); lastTriggerBackupReq captures the most recent request so tests
	// can assert the submitted ids -- not just backupConfigIDs -- are
	// forwarded onto the RPC unchanged.
	triggerBackupResp    *manmanpb.TriggerBackupResponse
	triggerBackupErr     error
	lastTriggerBackupReq *manmanpb.TriggerBackupRequest

	// backupsByID/getBackupErr back GetBackup for handleBackupRunDetail
	// (task #2814, FR6). backupsByID is keyed by backup id rather than a
	// single fixed response so a test can assert an unknown id 404s
	// without a second fake instance.
	backupsByID  map[int64]*manmanpb.Backup
	getBackupErr error

	// actionsByBackupConfigID backs ListBackupConfigActions for the detail
	// view's pre-backup Actions section (task #2814, FR6) -- keyed by
	// backup_config_id so a test can fixture one BackupConfig's Actions
	// without affecting another's.
	actionsByBackupConfigID    map[int64][]*manmanpb.BackupConfigActionItem
	listBackupConfigActionsErr error
}

func (f *fakeBackupsFleetAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeBackupsFleetAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.sgcsByServerID[in.ServerId]}, nil
}

func (f *fakeBackupsFleetAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	cfg, ok := f.gameConfigsByID[in.ConfigId]
	if !ok {
		return nil, fmt.Errorf("game config %d not found", in.ConfigId)
	}
	return &manmanpb.GetGameConfigResponse{Config: cfg}, nil
}

func (f *fakeBackupsFleetAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	return nil, fmt.Errorf("game %d not found", in.GameId)
}

func (f *fakeBackupsFleetAPIClient) ListBackupConfigs(ctx context.Context, in *manmanpb.ListBackupConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupConfigsResponse, error) {
	return &manmanpb.ListBackupConfigsResponse{Items: f.backupConfigItems}, nil
}

func (f *fakeBackupsFleetAPIClient) ListBackups(ctx context.Context, in *manmanpb.ListBackupsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupsResponse, error) {
	f.lastListBackupsReq = in
	if f.listBackupsErr != nil {
		return nil, f.listBackupsErr
	}
	return f.listBackupsResp, nil
}

func (f *fakeBackupsFleetAPIClient) TriggerBackup(ctx context.Context, in *manmanpb.TriggerBackupRequest, opts ...grpc.CallOption) (*manmanpb.TriggerBackupResponse, error) {
	f.lastTriggerBackupReq = in
	if f.triggerBackupErr != nil {
		return nil, f.triggerBackupErr
	}
	if f.triggerBackupResp != nil {
		return f.triggerBackupResp, nil
	}
	return &manmanpb.TriggerBackupResponse{BackupId: 999}, nil
}

func (f *fakeBackupsFleetAPIClient) GetBackup(ctx context.Context, in *manmanpb.GetBackupRequest, opts ...grpc.CallOption) (*manmanpb.GetBackupResponse, error) {
	if f.getBackupErr != nil {
		return nil, f.getBackupErr
	}
	backup, ok := f.backupsByID[in.BackupId]
	if !ok {
		return nil, status.Error(codes.NotFound, "backup not found")
	}
	return &manmanpb.GetBackupResponse{Backup: backup}, nil
}

func (f *fakeBackupsFleetAPIClient) ListBackupConfigActions(ctx context.Context, in *manmanpb.ListBackupConfigActionsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupConfigActionsResponse, error) {
	if f.listBackupConfigActionsErr != nil {
		return nil, f.listBackupConfigActionsErr
	}
	return &manmanpb.ListBackupConfigActionsResponse{Items: f.actionsByBackupConfigID[in.BackupConfigId]}, nil
}

func newBackupsFleetTestApp(api *fakeBackupsFleetAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

// baseBackupsFleetFixture is a two-server, two-deployment fleet: server 10
// ("Alpha") hosts SGC 55 (game config 900 "Survival"); server 20 ("Beta")
// hosts SGC 66 (game config 901 "Creative"). Two BackupConfigs/volumes back
// the filter bar's Volume/BackupConfig pickers.
func baseBackupsFleetFixture() *fakeBackupsFleetAPIClient {
	return &fakeBackupsFleetAPIClient{
		servers: []*manmanpb.Server{
			{ServerId: 10, Name: "Alpha"},
			{ServerId: 20, Name: "Beta"},
		},
		sgcsByServerID: map[int64][]*manmanpb.ServerGameConfig{
			10: {{ServerGameConfigId: 55, ServerId: 10, GameConfigId: 900, Status: "active"}},
			20: {{ServerGameConfigId: 66, ServerId: 20, GameConfigId: 901, Status: "active"}},
		},
		gameConfigsByID: map[int64]*manmanpb.GameConfig{
			900: {ConfigId: 900, GameId: 9000, Name: "Survival"},
			901: {ConfigId: 901, GameId: 9001, Name: "Creative"},
		},
		backupConfigItems: []*manmanpb.BackupConfigListItem{
			{Config: &manmanpb.BackupConfig{BackupConfigId: 1, VolumeId: 501}, VolumeName: "World Data", GameConfigId: 900, GameConfigName: "Survival"},
			{Config: &manmanpb.BackupConfig{BackupConfigId: 2, VolumeId: 502}, VolumeName: "Mods", GameConfigId: 901, GameConfigName: "Creative"},
		},
	}
}

// threeOriginBackupItems fixtures one BackupListItem per FR2/FR3 trigger
// origin, plus the empty-string historical-row case, each pinned to a
// distinct volume name so a test can locate a specific row's rendered
// output without the other rows' text colliding.
func threeOriginBackupItems() []*manmanpb.BackupListItem {
	return []*manmanpb.BackupListItem{
		{
			Backup:               &manmanpb.Backup{BackupId: 1, ServerGameConfigId: 55, Status: "completed", CreatedAt: 1700000000, SizeBytes: 2097152, TriggerSource: "scheduled"},
			ServerGameConfigName: "Alpha / Survival",
			VolumeName:           "World Data",
		},
		{
			Backup:               &manmanpb.Backup{BackupId: 2, ServerGameConfigId: 66, Status: "running", TriggerSource: "manual"},
			ServerGameConfigName: "Beta / Creative",
			VolumeName:           "Mods",
		},
		{
			Backup:               &manmanpb.Backup{BackupId: 3, ServerGameConfigId: 55, Status: "failed", TriggerSource: ""},
			ServerGameConfigName: "Alpha / Survival",
			VolumeName:           "Legacy Volume",
		},
	}
}

func renderBackupsPageHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, rawQuery string) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	target := "/backups"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleBackupsPage(w, req)
	return w.Code, w.Body.String()
}

func renderBackupRunsFragmentHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, rawQuery string) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	target := "/backups/runs"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleBackupRunsFragment(w, req)
	return w.Code, w.Body.String()
}

// --- criterion 1/2: "/backups" renders 200 with fleet-wide rows -----------

func TestHandleBackupsPage_RendersRowsFromFleetWideListBackups(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{Items: threeOriginBackupItems()}

	code, body := renderBackupsPageHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{"Alpha / Survival", "Beta / Creative", "World Data", "Mods", "Legacy Volume"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected row content %q in rendered page, got: %s", want, body)
		}
	}

	// No SGC scoping anywhere in the request path (FR1): the ListBackups
	// call the initial page render issues carries no ServerGameConfigId
	// filter.
	if api.lastListBackupsReq == nil {
		t.Fatal("expected /backups to call ListBackups")
	}
	if api.lastListBackupsReq.ServerGameConfigId != 0 {
		t.Errorf("unfiltered /backups load scoped ListBackups to server_game_config_id=%d, want unscoped (0)", api.lastListBackupsReq.ServerGameConfigId)
	}
}

func TestHandleBackupsPage_IsReachableFromNav(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}
	code, body := renderBackupsPageHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, `href="/backups"`) {
		t.Errorf("expected a nav link to /backups on its own page, got: %s", body)
	}
}

// --- FR4: each filter is forwarded to the gRPC request as the right field -

func TestHandleBackupRunsFragment_ForwardsFiltersToListBackupsRequest(t *testing.T) {
	cases := []struct {
		name  string
		query string
		check func(t *testing.T, req *manmanpb.ListBackupsRequest)
	}{
		{
			name:  "server_game_config_id",
			query: "server_game_config_id=55",
			check: func(t *testing.T, req *manmanpb.ListBackupsRequest) {
				if req.ServerGameConfigId != 55 {
					t.Errorf("ServerGameConfigId = %d, want 55", req.ServerGameConfigId)
				}
			},
		},
		{
			name:  "volume_id",
			query: "volume_id=501",
			check: func(t *testing.T, req *manmanpb.ListBackupsRequest) {
				if req.VolumeId != 501 {
					t.Errorf("VolumeId = %d, want 501", req.VolumeId)
				}
			},
		},
		{
			name:  "backup_config_id",
			query: "backup_config_id=1",
			check: func(t *testing.T, req *manmanpb.ListBackupsRequest) {
				if req.BackupConfigId != 1 {
					t.Errorf("BackupConfigId = %d, want 1", req.BackupConfigId)
				}
			},
		},
		{
			name:  "status",
			query: "status=completed",
			check: func(t *testing.T, req *manmanpb.ListBackupsRequest) {
				if req.Status != "completed" {
					t.Errorf("Status = %q, want %q", req.Status, "completed")
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := baseBackupsFleetFixture()
			api.listBackupsResp = &manmanpb.ListBackupsResponse{}
			code, body := renderBackupRunsFragmentHTTP(t, api, tc.query)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
			}
			if api.lastListBackupsReq == nil {
				t.Fatal("expected /backups/runs to call ListBackups")
			}
			tc.check(t, api.lastListBackupsReq)
		})
	}
}

// --- NFR4: server-side pagination -------------------------------------

func TestHandleBackupRunsFragment_PageTokenForwardedToRequest(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}
	code, _ := renderBackupRunsFragmentHTTP(t, api, "page_token=abc123")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
	if api.lastListBackupsReq.PageToken != "abc123" {
		t.Errorf("PageToken = %q, want %q", api.lastListBackupsReq.PageToken, "abc123")
	}
}

func TestHandleBackupRunsFragment_LoadMoreOnlyWhenTokenReturned(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{
		Items:         threeOriginBackupItems(),
		NextPageToken: "next-tok",
	}
	code, body := renderBackupRunsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Load more") {
		t.Errorf("expected a Load more control when ListBackups returns a next_page_token, got: %s", body)
	}

	api2 := baseBackupsFleetFixture()
	api2.listBackupsResp = &manmanpb.ListBackupsResponse{Items: threeOriginBackupItems()}
	code2, body2 := renderBackupRunsFragmentHTTP(t, api2, "")
	if code2 != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code2, http.StatusOK, body2)
	}
	if strings.Contains(body2, "Load more") {
		t.Errorf("expected no Load more control when ListBackups returns no next_page_token, got: %s", body2)
	}
}

// --- FR2/FR3: trigger-origin rendering, "unknown" never displays as
// "manual" -----------------------------------------------------------------

func TestHandleBackupRunsFragment_TriggerOriginRendering(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{Items: threeOriginBackupItems()}
	code, body := renderBackupRunsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if !strings.Contains(body, "scheduled") {
		t.Errorf("expected the scheduled row's origin to render as %q, got: %s", "scheduled", body)
	}
	if !strings.Contains(body, "manual") {
		t.Errorf("expected the manual row's origin to render as %q, got: %s", "manual", body)
	}
	if !strings.Contains(body, "unknown") {
		t.Errorf("expected the empty-trigger_source row's origin to render as %q, got: %s", "unknown", body)
	}

	// Isolate the "Legacy Volume" row (empty trigger_source) and confirm
	// its own origin cell says "unknown", not "manual" -- FR2/FR3's core
	// rule, not just "the word manual appears somewhere on the page".
	idx := strings.Index(body, "Legacy Volume")
	if idx == -1 {
		t.Fatalf("expected the empty-trigger_source row (%q) in the rendered fragment, got: %s", "Legacy Volume", body)
	}
	rowTail := body[idx:]
	end := strings.Index(rowTail, "</tr>")
	if end != -1 {
		rowTail = rowTail[:end]
	}
	if !strings.Contains(rowTail, "unknown") {
		t.Errorf("expected the empty-trigger_source row to render %q, got row: %s", "unknown", rowTail)
	}
	if strings.Contains(rowTail, "manual") {
		t.Errorf("empty-trigger_source row must not render as %q, got row: %s", "manual", rowTail)
	}
}

// --- in-page error state, not a bare 500 (Implementation-phase addition) --

func TestHandleBackupsPage_ListBackupsFailureRendersInlineError(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsErr = fmt.Errorf("control API unreachable")

	code, body := renderBackupsPageHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (an upstream ListBackups failure must render in-page, not bubble up as a 500); body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Failed to load backup runs") {
		t.Errorf("expected an inline error alert, got: %s", body)
	}
}

func TestHandleBackupRunsFragment_ListBackupsFailureRendersInlineError(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsErr = fmt.Errorf("control API unreachable")

	code, body := renderBackupRunsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Failed to load backup runs") {
		t.Errorf("expected an inline error alert, got: %s", body)
	}
}

// --- empty state (Implementation-phase addition) ---------------------------

func TestHandleBackupRunsFragment_EmptyStateWhenNoRunsMatch(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}

	code, body := renderBackupRunsFragmentHTTP(t, api, "status=failed")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "No backup runs") {
		t.Errorf("expected the empty state, got: %s", body)
	}
}

// --- FR4: filter bar exposes name-based pickers, not raw ID inputs --------

func TestHandleBackupsPage_FilterBarUsesNameBasedPickers(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}

	code, body := renderBackupsPageHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{"Alpha / Survival", "Beta / Creative", "Survival / World Data", "Creative / Mods"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected filter picker option %q in rendered page, got: %s", want, body)
		}
	}
}

// --- task #2813 (FR5): trigger a backup run from the fleet surface --------
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// deleting handleBackupTrigger's `bcErr != nil` check from its malformed-id
// guard (keeping only the sgcID check) made
// TestHandleBackupTrigger_MissingBackupConfigIDReturns400WithoutCallingRPC
// fail (a non-numeric backup_config_id no longer got rejected before
// TriggerBackup was called); restoring the check made it pass again.

func renderBackupTriggerFormHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, rawQuery string) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	target := "/backups/trigger-form"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleBackupTriggerForm(w, req)
	return w.Code, w.Body.String()
}

func postBackupTriggerHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, form url.Values) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	req := httptest.NewRequest(http.MethodPost, "/backups/trigger", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBackupTrigger(w, req)
	return w.Code, w.Body.String()
}

// Picker test: the fragment lists BackupConfigs from more than one
// GameConfig, proving the picker is fleet-wide, not scoped to a single SGC
// or GameConfig the way a per-deployment page's own BackupConfig list would
// be.
func TestHandleBackupTriggerForm_ListsBackupConfigsFleetWide(t *testing.T) {
	api := baseBackupsFleetFixture()

	code, body := renderBackupTriggerFormHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	for _, want := range []string{"Survival / World Data", "Creative / Mods"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected BackupConfig option %q from the fleet-wide picker, got: %s", want, body)
		}
	}
}

// Given a chosen BackupConfig, the picker lists only the deployments (SGCs)
// eligible for it -- i.e. whose GameConfigId matches that BackupConfig's own
// game config -- not every deployment fleet-wide.
func TestHandleBackupTriggerForm_SelectedConfigListsOnlyEligibleDeployments(t *testing.T) {
	api := baseBackupsFleetFixture()

	code, body := renderBackupTriggerFormHTTP(t, api, "backup_config_id=1")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Alpha / Survival") {
		t.Errorf("expected the Survival BackupConfig's picker to list its own eligible deployment (Alpha / Survival), got: %s", body)
	}
	if strings.Contains(body, "Beta / Creative") {
		t.Errorf("expected the Survival BackupConfig's picker to exclude the Creative deployment (Beta / Creative), got: %s", body)
	}
}

// A well-formed POST calls TriggerBackup with the submitted deployment and
// BackupConfig ids and re-renders the runs fragment (via the out-of-band
// panel refresh) so the new pending row appears without navigation.
func TestHandleBackupTrigger_WellFormedPostCallsTriggerBackupAndRefreshesRunList(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.triggerBackupResp = &manmanpb.TriggerBackupResponse{BackupId: 42}
	api.listBackupsResp = &manmanpb.ListBackupsResponse{
		Items: []*manmanpb.BackupListItem{
			{
				Backup:               &manmanpb.Backup{BackupId: 42, ServerGameConfigId: 55, Status: "pending", TriggerSource: "manual"},
				ServerGameConfigName: "Alpha / Survival",
				VolumeName:           "World Data",
			},
		},
	}

	form := url.Values{"server_game_config_id": {"55"}, "backup_config_id": {"1"}}
	code, body := postBackupTriggerHTTP(t, api, form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastTriggerBackupReq == nil {
		t.Fatal("expected POST /backups/trigger to call TriggerBackup")
	}
	if api.lastTriggerBackupReq.ServerGameConfigId != 55 {
		t.Errorf("TriggerBackup ServerGameConfigId = %d, want 55", api.lastTriggerBackupReq.ServerGameConfigId)
	}
	if api.lastTriggerBackupReq.BackupConfigId != 1 {
		t.Errorf("TriggerBackup BackupConfigId = %d, want 1", api.lastTriggerBackupReq.BackupConfigId)
	}

	if !strings.Contains(body, "42") {
		t.Errorf("expected the returned backup id (42) in the response, got: %s", body)
	}
	// The refreshed run list carries the new pending row and its manual
	// origin (#2808's trigger_source, round-tripped here rather than
	// re-implemented -- see backupRunOrigin).
	if !strings.Contains(body, "Alpha / Survival") || !strings.Contains(body, "pending") || !strings.Contains(body, "manual") {
		t.Errorf("expected an out-of-band refresh of the run list showing the new pending/manual row, got: %s", body)
	}
	if !strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("expected the run list refresh to be an out-of-band swap so the Blade's own response isn't disturbed, got: %s", body)
	}
}

// A malformed/missing id is rejected with 400 before TriggerBackup is ever
// called.
func TestHandleBackupTrigger_MissingBackupConfigIDReturns400WithoutCallingRPC(t *testing.T) {
	api := baseBackupsFleetFixture()

	form := url.Values{"server_game_config_id": {"55"}}
	code, _ := postBackupTriggerHTTP(t, api, form)
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
	if api.lastTriggerBackupReq != nil {
		t.Errorf("expected TriggerBackup not to be called for a missing backup_config_id, got request: %+v", api.lastTriggerBackupReq)
	}
}

func TestHandleBackupTrigger_MissingServerGameConfigIDReturns400WithoutCallingRPC(t *testing.T) {
	api := baseBackupsFleetFixture()

	form := url.Values{"backup_config_id": {"1"}}
	code, _ := postBackupTriggerHTTP(t, api, form)
	if code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", code, http.StatusBadRequest)
	}
	if api.lastTriggerBackupReq != nil {
		t.Errorf("expected TriggerBackup not to be called for a missing server_game_config_id, got request: %+v", api.lastTriggerBackupReq)
	}
}

// A FailedPrecondition from TriggerBackup (the "no session found for SGC"
// case api/handlers/backup_config.go returns when the chosen deployment has
// no running session) renders inline as a readable, actionable message --
// not a generic 500 -- and does not refresh the run list (nothing new was
// created).
func TestHandleBackupTrigger_FailedPreconditionRendersActionableInlineMessage(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.triggerBackupErr = status.Error(codes.FailedPrecondition, "no session found for SGC")

	form := url.Values{"server_game_config_id": {"55"}, "backup_config_id": {"1"}}
	code, body := postBackupTriggerHTTP(t, api, form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a FailedPrecondition must render inline, not bubble up); body: %s", code, http.StatusOK, body)
	}
	if strings.Contains(body, "no session found for SGC") {
		t.Errorf("expected the raw gRPC status text to be replaced with an actionable message, got: %s", body)
	}
	if !strings.Contains(body, "start it before triggering a backup") {
		t.Errorf("expected an actionable no-running-session message, got: %s", body)
	}
	if strings.Contains(body, `hx-swap-oob="true"`) {
		t.Errorf("expected no run list refresh on a failed trigger, got: %s", body)
	}
}

// Auth: unauthenticated POST is rejected by the same wrapper as the rest of
// "/backups" (NFR3) -- covered at the route-table level for GET
// (main_test.go's manmanv2RouteTable/TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated,
// which asserts RequireAuthFunc runs ahead of any method check); this test
// confirms the same wrapper is reached for an actual POST.
func TestHandleBackupTrigger_UnauthenticatedPostIsRejected(t *testing.T) {
	app := &App{auth: newTestOIDCAuthenticator(t)}
	mux := http.NewServeMux()
	app.setupRoutes(mux)

	req := httptest.NewRequest(http.MethodPost, "/backups/trigger", strings.NewReader("server_game_config_id=55&backup_config_id=1"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, req)

	if !requestWasAuthBlocked(w) {
		t.Fatalf("expected an unauthenticated POST /backups/trigger to be auth-blocked, got status %d", w.Code)
	}
}
