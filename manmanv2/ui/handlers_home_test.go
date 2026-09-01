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

// This file guards #1532's SGCID plumbing (test 4 of the task's Testing
// section): handleDashboardSessions must populate ActiveSessionInfo.SGCID
// from Session.ServerGameConfigId directly for every returned entry, not
// from the sgcByID enrichment lookup -- so the dashboard card's /sgc/{id}
// link (dashboard.templ) is still correct even when a session's SGC
// enrichment lookup misses (the handler already tolerates that: ServerName/
// GameName/ConfigName fall back to placeholder strings, see
// handleDashboardSessions).
//
// mutation-tested (verified red, by hand, then reverted): changing
// `SGCID: s.ServerGameConfigId` in handleDashboardSessions to
// `SGCID: sgc.ServerGameConfigId` (only set inside the `if sgc, ok :=
// sgcByID[...]; ok` branch, defaulting to the zero value otherwise) made
// TestHandleDashboardSessions_SGCIDPopulatedEvenWhenEnrichmentMisses fail
// -- the missed-enrichment session rendered /sgc/0 instead of /sgc/77 --
// while compiling cleanly; reverting restored green.

// fakeDashboardAPIClient is scoped to handleDashboardSessions' call graph:
// ListSessions, ListServers, ListServerGameConfigs, GetGameConfig, GetGame.
// Any call to an un-overridden ManManAPIClient method panics on the nil
// embedded interface, deliberately -- see handlers_sgc_test.go's
// fakeManManAPIClient for the same convention.
type fakeDashboardAPIClient struct {
	manmanpb.ManManAPIClient

	sessions        []*manmanpb.Session
	servers         []*manmanpb.Server
	sgcsByServerID  map[int64][]*manmanpb.ServerGameConfig
	gameConfigsByID map[int64]*manmanpb.GameConfig
	gamesByID       map[int64]*manmanpb.Game
}

func (f *fakeDashboardAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	return &manmanpb.ListSessionsResponse{Sessions: f.sessions}, nil
}

func (f *fakeDashboardAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeDashboardAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.sgcsByServerID[in.ServerId]}, nil
}

func (f *fakeDashboardAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return &manmanpb.GetGameConfigResponse{Config: f.gameConfigsByID[in.ConfigId]}, nil
}

func (f *fakeDashboardAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	return &manmanpb.GetGameResponse{Game: f.gamesByID[in.GameId]}, nil
}

func renderDashboardSessionsHTTP(t *testing.T, api *fakeDashboardAPIClient) string {
	t.Helper()
	app := &App{grpc: &ControlClient{api: api}}
	req := httptest.NewRequest(http.MethodGet, "/api/dashboard-sessions", nil)
	w := httptest.NewRecorder()
	app.handleDashboardSessions(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("handleDashboardSessions status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	return w.Body.String()
}

// TestHandleDashboardSessions_SGCIDPopulatedEvenWhenEnrichmentMisses covers
// test 4: two sessions, one whose SGC enrichment lookup succeeds (server 1
// lists its SGC) and one whose lookup misses (server 2 has no SGCs known to
// the handler, so sgcByID never gains an entry for its SGCID). Both must
// still render a /sgc/{ServerGameConfigId} link, and the missed-enrichment
// session must still fall back to the "SGC {id}" ServerName placeholder
// the handler already had.
func TestHandleDashboardSessions_SGCIDPopulatedEvenWhenEnrichmentMisses(t *testing.T) {
	api := &fakeDashboardAPIClient{
		sessions: []*manmanpb.Session{
			{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: 100},
			{SessionId: 2, ServerGameConfigId: 77, Status: "running", StartedAt: 100},
		},
		servers: []*manmanpb.Server{
			{ServerId: 10, Name: "Alpha"},
			{ServerId: 20, Name: "Beta"},
		},
		sgcsByServerID: map[int64][]*manmanpb.ServerGameConfig{
			10: {{ServerGameConfigId: 55, ServerId: 10, GameConfigId: 900}},
			// Server 20 has no SGCs, so sgcByID never gains an entry for
			// SGCID 77 -- the enrichment lookup for session 2 misses.
		},
		gameConfigsByID: map[int64]*manmanpb.GameConfig{
			900: {ConfigId: 900, GameId: 9000, Name: "Survival"},
		},
		gamesByID: map[int64]*manmanpb.Game{
			9000: {GameId: 9000, Name: "Minecraft"},
		},
	}

	body := renderDashboardSessionsHTTP(t, api)

	if !strings.Contains(body, `href="/sgc/55"`) {
		t.Errorf("expected enriched session's card to link /sgc/55, got body: %s", body)
	}
	if !strings.Contains(body, `href="/sgc/77"`) {
		t.Errorf("expected the enrichment-miss session's card to still link /sgc/77 (SGCID set directly from the session, not the enrichment lookup), got body: %s", body)
	}
	// The enrichment-miss session's ServerName falls back to "SGC 77" --
	// proves the enrichment miss is real (not accidentally satisfied) and
	// that the existing fallback path still renders correctly alongside
	// the correct SGCID.
	if !strings.Contains(body, "SGC 77") {
		t.Errorf("expected the enrichment-miss session to render the existing ServerName fallback %q, got body: %s", "SGC 77", body)
	}
	if strings.Contains(body, `href="/sgc/0"`) {
		t.Errorf("expected no zero-value SGCID link, got body: %s", body)
	}
}
