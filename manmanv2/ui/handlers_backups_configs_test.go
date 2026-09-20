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

// This file guards task #2816 (root plan #2777, M7): the fleet-wide
// "/backups" surface's "Backup configs" tab (FR9-FR11, NFR3, NFR4).
// Unauthenticated-request coverage for "/backups/configs" lives in
// main_test.go's TestSetupRoutes_OnlyFivePublicRoutesReachableUnauthenticated
// route table (NFR3), not duplicated here.
//
// FR11 red/green (verified by hand, then reverted): temporarily adding
//
//	<button hx-post="/backup-configs/create">Delete</button>
//
// to backupConfigRow (pages/backups.templ) made
// TestHandleBackupConfigsFragment_NoCreateEditDeleteControlForBackupConfigs
// fail on the "must not contain a mutation control" assertion below;
// removing it restored green.

// fakeBackupConfigsFleetAPIClient is scoped to handleBackupConfigsFragment's
// call graph: only ListBackupConfigs. Any other call panics on the nil
// embedded interface, matching fakeBackupsFleetAPIClient's convention
// (handlers_backups_fleet_test.go). Kept separate from that fake rather
// than reused: handleBackupConfigsFragment issues exactly one
// ListBackupConfigs call per request, so this fake's lastReq capture is
// never at risk of being clobbered by a second, unrelated ListBackupConfigs
// call the way it would be if driven through the full "/backups" page
// (which also resolves the filter bar's name-based picker options via a
// second ListBackupConfigs call).
type fakeBackupConfigsFleetAPIClient struct {
	manmanpb.ManManAPIClient

	resp *manmanpb.ListBackupConfigsResponse
	err  error

	// lastReq captures the most recent ListBackupConfigs request so tests
	// can assert a query-string filter was forwarded onto the right
	// request field (FR10), not just that the call happened.
	lastReq *manmanpb.ListBackupConfigsRequest
}

func (f *fakeBackupConfigsFleetAPIClient) ListBackupConfigs(ctx context.Context, in *manmanpb.ListBackupConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListBackupConfigsResponse, error) {
	f.lastReq = in
	if f.err != nil {
		return nil, f.err
	}
	return f.resp, nil
}

func newBackupConfigsFleetTestApp(api *fakeBackupConfigsFleetAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

func renderBackupConfigsFragmentHTTP(t *testing.T, api *fakeBackupConfigsFleetAPIClient, rawQuery string) (int, string) {
	t.Helper()
	app := newBackupConfigsFleetTestApp(api)
	target := "/backups/configs"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleBackupConfigsFragment(w, req)
	return w.Code, w.Body.String()
}

// twoGameConfigBackupConfigItems fixtures one BackupConfigListItem per
// GameConfig (proving the tab is fleet-wide, not scoped to one GameConfig)
// plus a distinct enabled state and last-run state per row: item 1 has run
// before, item 2 has never run (LastBackupAt: 0, timeAgo's "Never" case).
func twoGameConfigBackupConfigItems() []*manmanpb.BackupConfigListItem {
	return []*manmanpb.BackupConfigListItem{
		{
			Config: &manmanpb.BackupConfig{
				BackupConfigId: 1, VolumeId: 501, CadenceMinutes: 60,
				BackupPath: "/data/world", Enabled: true, LastBackupAt: 1700000000,
			},
			VolumeName: "World Data", GameConfigId: 900, GameConfigName: "Survival",
			GameName: "Blockworld", GameId: 9000,
		},
		{
			Config: &manmanpb.BackupConfig{
				BackupConfigId: 2, VolumeId: 502, CadenceMinutes: 1440,
				BackupPath: "/data/mods", Enabled: false, LastBackupAt: 0,
			},
			VolumeName: "Mods", GameConfigId: 901, GameConfigName: "Creative",
			GameName: "Blockworld", GameId: 9000,
		},
	}
}

// --- FR9: fleet-wide, every column renders -----------------------------

func TestHandleBackupConfigsFragment_RendersConfigsSpanningMultipleGameConfigs(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{Items: twoGameConfigBackupConfigItems()}}

	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{"Survival", "Creative", "World Data", "Mods"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected row content %q in rendered fragment (fleet-wide, spans >1 GameConfig), got: %s", want, body)
		}
	}
}

func TestHandleBackupConfigsFragment_RenderingShowsCadencePathEnabledAndLastRun(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{Items: twoGameConfigBackupConfigItems()}}

	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	for _, want := range []string{"60 min", "/data/world", "Enabled", "1440 min", "/data/mods", "Disabled"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected column content %q in rendered fragment, got: %s", want, body)
		}
	}
}

// TestHandleBackupConfigsFragment_NeverRunRendersNeverNotEpoch isolates the
// never-run row (LastBackupAt: 0) and confirms its own Last Run cell says
// "Never" -- never an epoch/zero rendering (timeAgo(0) == "Never") -- the
// same isolate-the-row-then-check-its-own-cell technique
// handlers_backups_fleet_test.go's TestHandleBackupRunsFragment_
// TriggerOriginRendering uses for FR2/FR3's "unknown never renders as
// manual" rule.
func TestHandleBackupConfigsFragment_NeverRunRendersNeverNotEpoch(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{Items: twoGameConfigBackupConfigItems()}}

	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	idx := strings.Index(body, "Mods")
	if idx == -1 {
		t.Fatalf("expected the never-run row (%q) in the rendered fragment, got: %s", "Mods", body)
	}
	rowTail := body[idx:]
	if end := strings.Index(rowTail, "</tr>"); end != -1 {
		rowTail = rowTail[:end]
	}
	if !strings.Contains(rowTail, "Never") {
		t.Errorf("expected the never-run row to render %q, got row: %s", "Never", rowTail)
	}
	for _, unwanted := range []string{"1970", "epoch"} {
		if strings.Contains(rowTail, unwanted) {
			t.Errorf("never-run row must not render an epoch/zero timestamp (%q), got row: %s", unwanted, rowTail)
		}
	}

	// The row that has run before must not itself say "Never".
	idx2 := strings.Index(body, "World Data")
	if idx2 == -1 {
		t.Fatalf("expected the has-run row (%q) in the rendered fragment, got: %s", "World Data", body)
	}
	rowTail2 := body[idx2:]
	if end := strings.Index(rowTail2, "</tr>"); end != -1 {
		rowTail2 = rowTail2[:end]
	}
	if strings.Contains(rowTail2, "Never") {
		t.Errorf("has-run row must not render %q, got row: %s", "Never", rowTail2)
	}
}

// --- FR10: each filter is forwarded to the gRPC request as the right field,
// enabled=false distinguishable from enabled unset ----------------------

func TestHandleBackupConfigsFragment_ForwardsFiltersToRequest(t *testing.T) {
	cases := []struct {
		name  string
		query string
		check func(t *testing.T, req *manmanpb.ListBackupConfigsRequest)
	}{
		{
			name:  "volume_id",
			query: "volume_id=501",
			check: func(t *testing.T, req *manmanpb.ListBackupConfigsRequest) {
				if req.VolumeId != 501 {
					t.Errorf("VolumeId = %d, want 501", req.VolumeId)
				}
			},
		},
		{
			name:  "game_config_id",
			query: "game_config_id=900",
			check: func(t *testing.T, req *manmanpb.ListBackupConfigsRequest) {
				if req.GameConfigId != 900 {
					t.Errorf("GameConfigId = %d, want 900", req.GameConfigId)
				}
			},
		},
		{
			name:  "enabled=true",
			query: "enabled=true",
			check: func(t *testing.T, req *manmanpb.ListBackupConfigsRequest) {
				if req.Enabled == nil {
					t.Fatal("Enabled = nil, want a set *bool")
				}
				if !*req.Enabled {
					t.Errorf("Enabled = %v, want true", *req.Enabled)
				}
			},
		},
		{
			name:  "enabled=false",
			query: "enabled=false",
			check: func(t *testing.T, req *manmanpb.ListBackupConfigsRequest) {
				if req.Enabled == nil {
					t.Fatal("Enabled = nil, want a set *bool (false must be distinguishable from unset)")
				}
				if *req.Enabled {
					t.Errorf("Enabled = %v, want false", *req.Enabled)
				}
			},
		},
		{
			name:  "enabled unset",
			query: "",
			check: func(t *testing.T, req *manmanpb.ListBackupConfigsRequest) {
				if req.Enabled != nil {
					t.Errorf("Enabled = %v, want nil (unset)", *req.Enabled)
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{}}
			code, body := renderBackupConfigsFragmentHTTP(t, api, tc.query)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
			}
			if api.lastReq == nil {
				t.Fatal("expected /backups/configs to call ListBackupConfigs")
			}
			tc.check(t, api.lastReq)
		})
	}
}

// --- NFR4: server-side pagination ---------------------------------------

func TestHandleBackupConfigsFragment_PageTokenForwardedToRequest(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{}}
	code, _ := renderBackupConfigsFragmentHTTP(t, api, "page_token=abc123")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d", code, http.StatusOK)
	}
	if api.lastReq.PageToken != "abc123" {
		t.Errorf("PageToken = %q, want %q", api.lastReq.PageToken, "abc123")
	}
}

func TestHandleBackupConfigsFragment_LoadMoreOnlyWhenTokenReturned(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{
		Items:         twoGameConfigBackupConfigItems(),
		NextPageToken: "next-tok",
	}}
	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Load more") {
		t.Errorf("expected a Load more control when ListBackupConfigs returns a next_page_token, got: %s", body)
	}

	api2 := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{Items: twoGameConfigBackupConfigItems()}}
	code2, body2 := renderBackupConfigsFragmentHTTP(t, api2, "")
	if code2 != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code2, http.StatusOK, body2)
	}
	if strings.Contains(body2, "Load more") {
		t.Errorf("expected no Load more control when ListBackupConfigs returns no next_page_token, got: %s", body2)
	}
}

// --- error state renders in-page, not a bare 500 ------------------------

func TestHandleBackupConfigsFragment_ListBackupConfigsFailureRendersInlineError(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{err: fmt.Errorf("control API unreachable")}

	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (an upstream ListBackupConfigs failure must render in-page, not bubble up as a 500); body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Failed to load backup configs") {
		t.Errorf("expected an inline error alert, got: %s", body)
	}
}

// --- FR11: link-out only, no create/edit/delete control on this tab -----

// TestHandleBackupConfigsFragment_NoCreateEditDeleteControlForBackupConfigs
// is the regression guard that keeps a second, competing BackupConfig
// create/edit/delete control from reappearing on this tab: the row must
// link to the Config Editor's Volumes tab for its volume (backupConfigEditorURL,
// "/games/{game_id}/configs/{game_config_id}") and must not itself carry a
// mutation control -- no hx-post at all on this tab (its filter bar submits
// via hx-get) and specifically none of the Config Editor's own
// backup-config mutation routes.
func TestHandleBackupConfigsFragment_NoCreateEditDeleteControlForBackupConfigs(t *testing.T) {
	api := &fakeBackupConfigsFleetAPIClient{resp: &manmanpb.ListBackupConfigsResponse{Items: twoGameConfigBackupConfigItems()}}

	code, body := renderBackupConfigsFragmentHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	// Link-out present: row 1 (game_id=9000, game_config_id=900) links to
	// the Config Editor's Volumes tab via Game Detail/Config Detail.
	wantLink := `href="/games/9000/configs/900"`
	if !strings.Contains(body, wantLink) {
		t.Errorf("expected a link-out to the Config Editor Volumes tab (%q), got: %s", wantLink, body)
	}

	if strings.Contains(body, "hx-post") {
		t.Errorf("Backup configs tab must not contain a mutation control (hx-post) -- create/edit/delete lives exclusively in the Config Editor; got: %s", body)
	}
	for _, unwanted := range []string{
		"/backup-configs/create",
		"backup-config/assign",
		"backup-config/edit",
		"backup-config/remove",
	} {
		if strings.Contains(body, unwanted) {
			t.Errorf("Backup configs tab must not reference a BackupConfig create/edit/delete route (%q), got: %s", unwanted, body)
		}
	}
}
