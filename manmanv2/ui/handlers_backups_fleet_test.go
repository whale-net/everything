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

	// actionDefsByGameID/actionDefsByConfigID back ListActionDefinitions for
	// the Actions panel's "add" picker (task #2817, FR13,
	// buildBackupConfigActionsData) -- keyed by game_id/config_id so a test
	// can fixture one BackupConfig's owning GameConfig/Game without
	// affecting another's, mirroring actionsByBackupConfigID's own
	// per-BackupConfig keying just above.
	actionDefsByGameID   map[int64][]*manmanpb.ActionDefinition
	actionDefsByConfigID map[int64][]*manmanpb.ActionDefinition

	// addBackupConfigActionErr/removeBackupConfigActionErr/
	// reorderBackupConfigActionsErr, when non-nil, are returned by the
	// corresponding RPC -- tests set an InvalidArgument to drive the
	// mutation handlers' inline-error rendering (task #2817's "an API error
	// renders inline, not a 500" requirement) without a real API round trip.
	// The lastXReq fields capture the most recent request so tests can
	// assert the exact fields forwarded, not just that some call happened.
	lastAddBackupConfigActionReq *manmanpb.AddBackupConfigActionRequest
	addBackupConfigActionErr     error

	lastRemoveBackupConfigActionReq *manmanpb.RemoveBackupConfigActionRequest
	removeBackupConfigActionErr     error

	lastReorderBackupConfigActionsReq *manmanpb.ReorderBackupConfigActionsRequest
	reorderBackupConfigActionsErr     error

	// deleteBackupErr, when non-nil, is returned by DeleteBackup -- tests
	// set it to a status.Error(codes.FailedPrecondition, ...) or
	// codes.NotFound to drive handleBackupRunDelete's response-handling
	// branches (task #2815, FR7, FR8) without a real API/S3 round trip.
	deleteBackupErr error
	// lastDeleteBackupReq captures the most recent DeleteBackup request so
	// tests can assert the right backup_id was forwarded, not just that
	// some delete happened.
	lastDeleteBackupReq *manmanpb.DeleteBackupRequest
}

func (f *fakeBackupsFleetAPIClient) DeleteBackup(ctx context.Context, in *manmanpb.DeleteBackupRequest, opts ...grpc.CallOption) (*manmanpb.DeleteBackupResponse, error) {
	f.lastDeleteBackupReq = in
	if f.deleteBackupErr != nil {
		return nil, f.deleteBackupErr
	}
	return &manmanpb.DeleteBackupResponse{}, nil
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

// ListActionDefinitions backs the Actions panel's "add" picker
// (buildBackupConfigActionsData calls this once at game level, once at
// config level). in.GameId/in.ConfigId are the optional oneof-style filters
// ListActionDefinitions(ctx, gameID, configID, sgcID *int64) sets in
// grpc_client.go -- exactly one is non-nil per call here.
func (f *fakeBackupsFleetAPIClient) ListActionDefinitions(ctx context.Context, in *manmanpb.ListActionDefinitionsRequest, opts ...grpc.CallOption) (*manmanpb.ListActionDefinitionsResponse, error) {
	if in.GameId != nil {
		return &manmanpb.ListActionDefinitionsResponse{Actions: f.actionDefsByGameID[in.GetGameId()]}, nil
	}
	if in.ConfigId != nil {
		return &manmanpb.ListActionDefinitionsResponse{Actions: f.actionDefsByConfigID[in.GetConfigId()]}, nil
	}
	return &manmanpb.ListActionDefinitionsResponse{}, nil
}

// AddBackupConfigAction mutates actionsByBackupConfigID in place (appending
// a row named after the requested action_id) so a test can assert the
// re-rendered panel reflects the server's post-mutation state, not
// optimistic local state -- mirroring how the real API commits the
// attachment before ListBackupConfigActions would next see it.
func (f *fakeBackupsFleetAPIClient) AddBackupConfigAction(ctx context.Context, in *manmanpb.AddBackupConfigActionRequest, opts ...grpc.CallOption) (*manmanpb.AddBackupConfigActionResponse, error) {
	f.lastAddBackupConfigActionReq = in
	if f.addBackupConfigActionErr != nil {
		return nil, f.addBackupConfigActionErr
	}
	if f.actionsByBackupConfigID == nil {
		f.actionsByBackupConfigID = map[int64][]*manmanpb.BackupConfigActionItem{}
	}
	f.actionsByBackupConfigID[in.BackupConfigId] = append(f.actionsByBackupConfigID[in.BackupConfigId], &manmanpb.BackupConfigActionItem{
		ActionId:     in.ActionId,
		DisplayOrder: in.DisplayOrder,
		Name:         fmt.Sprintf("Action %d", in.ActionId),
	})
	return &manmanpb.AddBackupConfigActionResponse{}, nil
}

// RemoveBackupConfigAction mutates actionsByBackupConfigID in place
// (dropping the requested action_id) for the same re-render-from-server
// assertion AddBackupConfigAction's doc comment describes.
func (f *fakeBackupsFleetAPIClient) RemoveBackupConfigAction(ctx context.Context, in *manmanpb.RemoveBackupConfigActionRequest, opts ...grpc.CallOption) (*manmanpb.RemoveBackupConfigActionResponse, error) {
	f.lastRemoveBackupConfigActionReq = in
	if f.removeBackupConfigActionErr != nil {
		return nil, f.removeBackupConfigActionErr
	}
	kept := f.actionsByBackupConfigID[in.BackupConfigId][:0]
	for _, item := range f.actionsByBackupConfigID[in.BackupConfigId] {
		if item.ActionId != in.ActionId {
			kept = append(kept, item)
		}
	}
	f.actionsByBackupConfigID[in.BackupConfigId] = kept
	return &manmanpb.RemoveBackupConfigActionResponse{}, nil
}

// ReorderBackupConfigActions mutates actionsByBackupConfigID's order to
// match the submitted action_ids sequence exactly, for the same
// re-render-from-server assertion described above.
func (f *fakeBackupsFleetAPIClient) ReorderBackupConfigActions(ctx context.Context, in *manmanpb.ReorderBackupConfigActionsRequest, opts ...grpc.CallOption) (*manmanpb.ReorderBackupConfigActionsResponse, error) {
	f.lastReorderBackupConfigActionsReq = in
	if f.reorderBackupConfigActionsErr != nil {
		return nil, f.reorderBackupConfigActionsErr
	}
	byID := make(map[int64]*manmanpb.BackupConfigActionItem, len(f.actionsByBackupConfigID[in.BackupConfigId]))
	for _, item := range f.actionsByBackupConfigID[in.BackupConfigId] {
		byID[item.ActionId] = item
	}
	reordered := make([]*manmanpb.BackupConfigActionItem, 0, len(in.ActionIds))
	for i, id := range in.ActionIds {
		item, ok := byID[id]
		if !ok {
			continue
		}
		item.DisplayOrder = int32(i)
		reordered = append(reordered, item)
	}
	f.actionsByBackupConfigID[in.BackupConfigId] = reordered
	return &manmanpb.ReorderBackupConfigActionsResponse{}, nil
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

// --- FR7/FR8: deleting a backup run from the fleet surface ----------------
//
// Testing-phase coverage for handleBackupRunDelete (task #2815), deferred
// from the Implementation phase: success drops the row from the re-rendered
// fragment, FailedPrecondition/NotFound leave the row in place with an
// inline, actionable notice, and neither error path returns a 500.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// collapsing handleBackupRunDelete's codes.FailedPrecondition/codes.NotFound
// switch cases into the default "Failed to delete backup run. Try again."
// branch made TestHandleBackupRunDelete_FailedPreconditionRendersActionableNotice's
// specific-message assertion fail (it got the generic message instead);
// restoring the switch cases returned it to green.

func renderBackupRunDeleteHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, backupID int64, referer string) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	target := fmt.Sprintf("/backups/runs/%d/delete", backupID)
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if referer != "" {
		req.Header.Set("Referer", referer)
	}
	w := httptest.NewRecorder()
	app.handleBackupRunDelete(w, req)
	return w.Code, w.Body.String()
}

// pendingRunBackupItem fixtures FR8's driving case end to end: a run with no
// s3_url yet (the API's precondition for DeleteBackup's FailedPrecondition,
// manmanv2/api/handlers/backup.go), still present in the list so a test can
// confirm it survives a failed delete attempt.
func pendingRunBackupItem() *manmanpb.BackupListItem {
	return &manmanpb.BackupListItem{
		Backup:               &manmanpb.Backup{BackupId: 7, ServerGameConfigId: 55, Status: "running", TriggerSource: "manual"},
		ServerGameConfigName: "Alpha / Survival",
		VolumeName:           "World Data",
	}
}

func TestHandleBackupRunDelete_SuccessCallsDeleteBackupAndDropsRowFromRefreshedFragment(t *testing.T) {
	api := baseBackupsFleetFixture()
	// The refreshed list (post-delete ListBackups call) reflects the
	// backend having already dropped the deleted run -- backup 1 stays,
	// backup 3 (the one being deleted) is gone.
	api.listBackupsResp = &manmanpb.ListBackupsResponse{Items: []*manmanpb.BackupListItem{threeOriginBackupItems()[0]}}

	code, body := renderBackupRunDeleteHTTP(t, api, 3, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastDeleteBackupReq == nil {
		t.Fatal("expected handleBackupRunDelete to call DeleteBackup")
	}
	if api.lastDeleteBackupReq.BackupId != 3 {
		t.Errorf("DeleteBackup called with backup_id=%d, want 3", api.lastDeleteBackupReq.BackupId)
	}

	if strings.Contains(body, "Legacy Volume") {
		t.Errorf("expected the deleted run's row (%q) to be gone from the refreshed fragment, got: %s", "Legacy Volume", body)
	}
	if !strings.Contains(body, "World Data") {
		t.Errorf("expected the surviving run's row (%q) to still be present, got: %s", "World Data", body)
	}
}

func TestHandleBackupRunDelete_FailedPreconditionRendersActionableNoticeAndKeepsRow(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.deleteBackupErr = status.Error(codes.FailedPrecondition, "backup has no S3 URL")
	// The row survives the failed delete: the refreshed list still
	// contains the pending run.
	api.listBackupsResp = &manmanpb.ListBackupsResponse{Items: []*manmanpb.BackupListItem{pendingRunBackupItem()}}

	code, body := renderBackupRunDeleteHTTP(t, api, 7, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a FailedPrecondition must render in-page, not a 500); body: %s", code, http.StatusOK, body)
	}
	if code >= 500 {
		t.Fatalf("FailedPrecondition must not surface as a 500, got status %d", code)
	}

	if !strings.Contains(body, "no archive uploaded yet") {
		t.Errorf("expected a specific, actionable FR8 message naming why the run cannot be deleted, got: %s", body)
	}
	if strings.Contains(body, "Failed to delete backup run. Try again.") {
		t.Errorf("expected the specific FailedPrecondition message, not the generic delete-failure message, got: %s", body)
	}
	if !strings.Contains(body, "World Data") {
		t.Errorf("expected the pending run's row to remain in the refreshed fragment after a failed delete, got: %s", body)
	}
}

func TestHandleBackupRunDelete_NotFoundRendersNotFoundNotice(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.deleteBackupErr = status.Error(codes.NotFound, "backup not found")
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}

	code, body := renderBackupRunDeleteHTTP(t, api, 999, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (a NotFound must render in-page, not a 500); body: %s", code, http.StatusOK, body)
	}
	if code >= 500 {
		t.Fatalf("NotFound must not surface as a 500, got status %d", code)
	}
	if !strings.Contains(body, "not be found") && !strings.Contains(body, "was not found") {
		t.Errorf("expected an inline not-found notice, got: %s", body)
	}
}

func TestHandleBackupRunDelete_UnexpectedErrorRendersGenericNoticeNot500(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.deleteBackupErr = status.Error(codes.Internal, "control API unreachable")
	api.listBackupsResp = &manmanpb.ListBackupsResponse{Items: []*manmanpb.BackupListItem{pendingRunBackupItem()}}

	code, body := renderBackupRunDeleteHTTP(t, api, 7, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (an unexpected DeleteBackup error must render in-page, not a 500); body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Failed to delete backup run. Try again.") {
		t.Errorf("expected the generic delete-failure notice for an unexpected error code, got: %s", body)
	}
}

func TestHandleBackupRunDelete_RefreshesFragmentUsingFilterFromReferer(t *testing.T) {
	api := baseBackupsFleetFixture()
	api.listBackupsResp = &manmanpb.ListBackupsResponse{}

	code, _ := renderBackupRunDeleteHTTP(t, api, 3, "https://manman.example.com/backups/runs?server_game_config_id=55&status=completed")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
	if api.lastListBackupsReq == nil {
		t.Fatal("expected the post-delete refresh to call ListBackups")
	}
	if api.lastListBackupsReq.ServerGameConfigId != 55 {
		t.Errorf("ServerGameConfigId = %d, want 55 (forwarded from Referer)", api.lastListBackupsReq.ServerGameConfigId)
	}
	if api.lastListBackupsReq.Status != "completed" {
		t.Errorf("Status = %q, want %q (forwarded from Referer)", api.lastListBackupsReq.Status, "completed")
	}
}

func TestHandleBackupRunDelete_RejectsNonPostMethod(t *testing.T) {
	api := baseBackupsFleetFixture()
	app := newBackupsFleetTestApp(api)
	req := httptest.NewRequest(http.MethodGet, "/backups/runs/3/delete", nil)
	w := httptest.NewRecorder()
	app.handleBackupRunDelete(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d for a non-POST request", w.Code, http.StatusMethodNotAllowed)
	}
	if api.lastDeleteBackupReq != nil {
		t.Errorf("expected a non-POST request never to reach DeleteBackup, got a call with backup_id=%d", api.lastDeleteBackupReq.BackupId)
	}
}

// --- FR12-FR15: pre-backup Action ordering panel (task #2817) -------------
//
// Testing-phase coverage for handleBackupConfigActionsRoute's four
// sub-handlers (list/add/remove/reorder): every mutation re-fetches and
// re-renders the panel from a fresh ListBackupConfigActions call rather than
// optimistic local state, so fakeBackupsFleetAPIClient's Add/Remove/Reorder
// methods above mutate actionsByBackupConfigID in place -- exactly like the
// real API committing the change before the next list call would see it --
// and these tests assert on the *rendered* body, not just the captured
// request, wherever "did it actually re-render from the server" matters.

// threeActionItems fixtures one BackupConfig's attached pre-backup Actions
// in execution order, each with a distinct name so a test can locate a
// specific row (and its position relative to the others) in rendered HTML.
func threeActionItems() []*manmanpb.BackupConfigActionItem {
	return []*manmanpb.BackupConfigActionItem{
		{ActionId: 101, DisplayOrder: 0, Name: "Save World"},
		{ActionId: 102, DisplayOrder: 1, Name: "Flush Cache"},
		{ActionId: 103, DisplayOrder: 2, Name: "Notify Discord"},
	}
}

// backupConfigActionsFixture layers threeActionItems onto
// baseBackupsFleetFixture's BackupConfig 1 (owned by GameConfig 900 / Game
// 9000) so buildBackupConfigActionsData's findBackupConfigItem lookup
// succeeds.
func backupConfigActionsFixture() *fakeBackupsFleetAPIClient {
	api := baseBackupsFleetFixture()
	api.actionsByBackupConfigID = map[int64][]*manmanpb.BackupConfigActionItem{
		1: threeActionItems(),
	}
	return api
}

func renderBackupConfigActionsListHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, backupConfigID int64) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/backups/configs/%d/actions", backupConfigID), nil)
	w := httptest.NewRecorder()
	app.handleBackupConfigActionsRoute(w, req)
	return w.Code, w.Body.String()
}

func postBackupConfigActionsSubrouteHTTP(t *testing.T, api *fakeBackupsFleetAPIClient, backupConfigID int64, subroute string, form url.Values) (int, string) {
	t.Helper()
	app := newBackupsFleetTestApp(api)
	target := fmt.Sprintf("/backups/configs/%d/actions/%s", backupConfigID, subroute)
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	app.handleBackupConfigActionsRoute(w, req)
	return w.Code, w.Body.String()
}

// criterion 1 (FR12): the ordered list renders with names, in display_order.
func TestHandleBackupConfigActionsList_RendersOrderedListWithNames(t *testing.T) {
	api := backupConfigActionsFixture()

	code, body := renderBackupConfigActionsListHTTP(t, api, 1)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, name := range []string{"Save World", "Flush Cache", "Notify Discord"} {
		if !strings.Contains(body, name) {
			t.Errorf("expected the attached action %q in the rendered panel, got: %s", name, body)
		}
	}

	saveIdx := strings.Index(body, "Save World")
	flushIdx := strings.Index(body, "Flush Cache")
	notifyIdx := strings.Index(body, "Notify Discord")
	if !(saveIdx < flushIdx && flushIdx < notifyIdx) {
		t.Errorf("expected actions rendered in display_order (Save World, Flush Cache, Notify Discord), got: %s", body)
	}
}

// criterion 2 (FR13): add forwards action_id + the chosen display_order, and
// the response re-renders from a fresh ListBackupConfigActions call (the
// newly attached action's name appears -- fakeBackupsFleetAPIClient's
// AddBackupConfigAction only adds it to actionsByBackupConfigID, the
// handler never invents it locally).
func TestHandleBackupConfigActionAdd_ForwardsActionIDAndDisplayOrderThenRerendersFromServer(t *testing.T) {
	api := backupConfigActionsFixture()

	form := url.Values{"action_id": {"104"}, "display_order": {"1"}}
	code, body := postBackupConfigActionsSubrouteHTTP(t, api, 1, "add", form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastAddBackupConfigActionReq == nil {
		t.Fatal("expected POST .../actions/add to call AddBackupConfigAction")
	}
	if api.lastAddBackupConfigActionReq.BackupConfigId != 1 {
		t.Errorf("BackupConfigId = %d, want 1", api.lastAddBackupConfigActionReq.BackupConfigId)
	}
	if api.lastAddBackupConfigActionReq.ActionId != 104 {
		t.Errorf("ActionId = %d, want 104", api.lastAddBackupConfigActionReq.ActionId)
	}
	if api.lastAddBackupConfigActionReq.DisplayOrder != 1 {
		t.Errorf("DisplayOrder = %d, want 1", api.lastAddBackupConfigActionReq.DisplayOrder)
	}

	if !strings.Contains(body, "Action 104") {
		t.Errorf("expected the panel to re-render from a fresh list call showing the newly attached action, got: %s", body)
	}
}

// criterion 3 (FR14): remove forwards action_id, and the response
// re-renders from the server with that action gone (never locally spliced
// out) while the others remain.
func TestHandleBackupConfigActionRemove_ForwardsActionIDThenRerendersFromServer(t *testing.T) {
	api := backupConfigActionsFixture()

	form := url.Values{"action_id": {"102"}}
	code, body := postBackupConfigActionsSubrouteHTTP(t, api, 1, "remove", form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastRemoveBackupConfigActionReq == nil {
		t.Fatal("expected POST .../actions/remove to call RemoveBackupConfigAction")
	}
	if api.lastRemoveBackupConfigActionReq.BackupConfigId != 1 {
		t.Errorf("BackupConfigId = %d, want 1", api.lastRemoveBackupConfigActionReq.BackupConfigId)
	}
	if api.lastRemoveBackupConfigActionReq.ActionId != 102 {
		t.Errorf("ActionId = %d, want 102", api.lastRemoveBackupConfigActionReq.ActionId)
	}

	if strings.Contains(body, "Flush Cache") {
		t.Errorf("expected the removed action to be gone from the re-rendered panel, got: %s", body)
	}
	if !strings.Contains(body, "Save World") || !strings.Contains(body, "Notify Discord") {
		t.Errorf("expected the surviving actions to remain in the re-rendered panel, got: %s", body)
	}
}

// criterion 4 (FR15): reorder forwards the complete action_ids sequence
// exactly as submitted, and the response re-renders in that order.
func TestHandleBackupConfigActionsReorder_ForwardsCompleteActionIDsSequenceThenRerendersFromServer(t *testing.T) {
	api := backupConfigActionsFixture()

	form := url.Values{"action_ids": {"103", "101", "102"}}
	code, body := postBackupConfigActionsSubrouteHTTP(t, api, 1, "reorder", form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastReorderBackupConfigActionsReq == nil {
		t.Fatal("expected POST .../actions/reorder to call ReorderBackupConfigActions")
	}
	got := api.lastReorderBackupConfigActionsReq.ActionIds
	want := []int64{103, 101, 102}
	if len(got) != len(want) {
		t.Fatalf("ActionIds = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ActionIds = %v, want %v (submitted order must be forwarded unchanged)", got, want)
		}
	}

	notifyIdx := strings.Index(body, "Notify Discord")
	saveIdx := strings.Index(body, "Save World")
	flushIdx := strings.Index(body, "Flush Cache")
	if notifyIdx == -1 || saveIdx == -1 || flushIdx == -1 {
		t.Fatalf("expected all three actions still present after reorder, got: %s", body)
	}
	if !(notifyIdx < saveIdx && saveIdx < flushIdx) {
		t.Errorf("expected the re-rendered panel to reflect the new server order (Notify Discord, Save World, Flush Cache), got: %s", body)
	}
}

// Ordering assertion: moving the last action to the top produces a reorder
// request whose first element is that action's id, and the re-rendered
// panel shows it first.
//
// mutation-tested (verified red, by hand, then reverted): temporarily
// changing handleBackupConfigActionsReorder's r.Form["action_ids"] loop
// (handlers_backups_fleet.go) to append parsed ids in *reverse* of the
// submitted order made this test's "first element" assertion fail (it
// forwarded action 102 first instead of 103); reverting restored green.
func TestHandleBackupConfigActionsReorder_MoveLastActionToTopPutsItFirst(t *testing.T) {
	api := backupConfigActionsFixture() // 101, 102, 103 -- 103 is last.

	form := url.Values{"action_ids": {"103", "101", "102"}} // 103 moved to the top.
	code, body := postBackupConfigActionsSubrouteHTTP(t, api, 1, "reorder", form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if api.lastReorderBackupConfigActionsReq == nil {
		t.Fatal("expected POST .../actions/reorder to call ReorderBackupConfigActions")
	}
	if len(api.lastReorderBackupConfigActionsReq.ActionIds) == 0 || api.lastReorderBackupConfigActionsReq.ActionIds[0] != 103 {
		t.Fatalf("ActionIds[0] = %v, want 103 (the action moved from last to first)", api.lastReorderBackupConfigActionsReq.ActionIds)
	}

	if strings.Index(body, "Notify Discord") > strings.Index(body, "Save World") {
		t.Errorf("expected the moved action (Notify Discord, id 103) to render first, got: %s", body)
	}
}

// criterion: an API error (e.g. InvalidArgument from a bad reorder set)
// renders inline, not a 500 -- and the attached-action list itself, whose
// separate ListBackupConfigActions call has already succeeded, still
// renders unmodified.
func TestHandleBackupConfigActionsReorder_InvalidArgumentRendersInlineNotFiveHundred(t *testing.T) {
	api := backupConfigActionsFixture()
	api.reorderBackupConfigActionsErr = status.Error(codes.InvalidArgument, "action_ids must match the current attached set")

	form := url.Values{"action_ids": {"101", "102"}} // a bad/incomplete set.
	code, body := postBackupConfigActionsSubrouteHTTP(t, api, 1, "reorder", form)
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (an InvalidArgument must render in-page, not a 500); body: %s", code, http.StatusOK, body)
	}
	if code >= 500 {
		t.Fatalf("an InvalidArgument reorder error must not surface as a 500, got status %d", code)
	}
	if !strings.Contains(body, "action_ids must match the current attached set") {
		t.Errorf("expected the API's own InvalidArgument message rendered inline, got: %s", body)
	}
	// The reorder failed server-side, so the fake's actionsByBackupConfigID
	// was never mutated -- the re-fetched list must still show the
	// original three actions.
	for _, name := range []string{"Save World", "Flush Cache", "Notify Discord"} {
		if !strings.Contains(body, name) {
			t.Errorf("expected the unmodified attached-action list to still render alongside the inline error, got: %s", body)
		}
	}
}

// Auth (NFR3): an unauthenticated POST to each add/remove/reorder route is
// rejected by the same RequireAuthFunc/WithAccessToken wrapper the rest of
// "/backups" uses, mirroring
// TestHandleBackupTrigger_UnauthenticatedPostIsRejected's posture.
func TestHandleBackupConfigActions_UnauthenticatedPostIsRejected(t *testing.T) {
	for _, subroute := range []string{"add", "remove", "reorder"} {
		t.Run(subroute, func(t *testing.T) {
			app := &App{auth: newTestOIDCAuthenticator(t)}
			mux := http.NewServeMux()
			app.setupRoutes(mux)

			target := fmt.Sprintf("/backups/configs/1/actions/%s", subroute)
			req := httptest.NewRequest(http.MethodPost, target, strings.NewReader("action_id=101"))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, req)

			if !requestWasAuthBlocked(w) {
				t.Fatalf("expected an unauthenticated POST %s to be auth-blocked, got status %d", target, w.Code)
			}
		})
	}
}
