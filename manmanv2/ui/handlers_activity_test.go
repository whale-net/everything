package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"google.golang.org/grpc"
)

// This file guards task #2271 (manmanv2 M5 Activity page): FR14's Live
// now/History tables, NFR10's fleet-wide authorized set, and the
// ground-truth rule that History's status column reads Session.status
// directly -- never ComputeDeploymentStatus's two-state rollup, and never
// ServerGameConfig.status's active/inactive lifecycle flag.

// activityLiveStatuses mirrors the live_only status set the real API
// enforces server-side (postgres/session.go), so fakeActivityAPIClient's
// ListSessions can emulate LiveOnly filtering the same way the real gRPC
// server does.
var activityLiveStatuses = map[string]bool{
	"pending":  true,
	"starting": true,
	"running":  true,
	"stopping": true,
}

// fakeActivityAPIClient is scoped to handleActivity's call graph:
// ListServers, ListServerGameConfigs, GetGameConfig, GetGame, ListSessions.
// Any other call panics on the nil embedded interface, deliberately -- see
// handlers_sgc_test.go's fakeManManAPIClient for the same convention.
type fakeActivityAPIClient struct {
	manmanpb.ManManAPIClient

	servers         []*manmanpb.Server
	sgcsByServerID  map[int64][]*manmanpb.ServerGameConfig
	gameConfigsByID map[int64]*manmanpb.GameConfig
	gamesByID       map[int64]*manmanpb.Game
	sessions        []*manmanpb.Session

	// lastLiveOnlyReq / lastNonLiveReq capture the two ListSessions
	// requests handleActivity issues (Live's LiveOnly=true call and
	// History's LiveOnly=false call) so tests can assert on what was
	// actually sent, not just on the rendered body.
	lastLiveOnlyReq *manmanpb.ListSessionsRequest
	lastHistoryReq  *manmanpb.ListSessionsRequest
}

func (f *fakeActivityAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeActivityAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.sgcsByServerID[in.ServerId]}, nil
}

func (f *fakeActivityAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	cfg, ok := f.gameConfigsByID[in.ConfigId]
	if !ok {
		return nil, fmt.Errorf("game config %d not found", in.ConfigId)
	}
	return &manmanpb.GetGameConfigResponse{Config: cfg}, nil
}

func (f *fakeActivityAPIClient) GetGame(ctx context.Context, in *manmanpb.GetGameRequest, opts ...grpc.CallOption) (*manmanpb.GetGameResponse, error) {
	game, ok := f.gamesByID[in.GameId]
	if !ok {
		return nil, fmt.Errorf("game %d not found", in.GameId)
	}
	return &manmanpb.GetGameResponse{Game: game}, nil
}

// ListSessions emulates the real API's live_only/status_filter semantics
// (manmanv2 postgres/session.go) against a single fixture sessions slice,
// the same way the real server would apply both filters to one underlying
// table -- rather than pre-splitting fixtures into separate "live" and
// "history" lists, which would let a handler bug (e.g. issuing the wrong
// filter on the wrong request) go unnoticed.
func (f *fakeActivityAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	if in.LiveOnly {
		f.lastLiveOnlyReq = in
	} else {
		f.lastHistoryReq = in
	}

	var statusSet map[string]bool
	if len(in.StatusFilter) > 0 {
		statusSet = make(map[string]bool, len(in.StatusFilter))
		for _, s := range in.StatusFilter {
			statusSet[s] = true
		}
	}

	var out []*manmanpb.Session
	for _, s := range f.sessions {
		if in.LiveOnly && !activityLiveStatuses[s.Status] {
			continue
		}
		if statusSet != nil && !statusSet[s.Status] {
			continue
		}
		out = append(out, s)
	}
	return &manmanpb.ListSessionsResponse{Sessions: out}, nil
}

func renderActivityHTTP(t *testing.T, api *fakeActivityAPIClient, rawQuery string) (int, string) {
	t.Helper()
	app := &App{grpc: &ControlClient{api: api}}
	target := "/activity"
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleActivity(w, req)
	return w.Code, w.Body.String()
}

// baseActivityFixture is a two-server, two-game fleet fixture shared by
// several tests below: server 10 hosts SGC 55 (game config 900 / game
// 9000, "Minecraft"); server 20 hosts SGC 66 (game config 901 / game 9001,
// "Valheim").
func baseActivityFixture() *fakeActivityAPIClient {
	return &fakeActivityAPIClient{
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
			901: {ConfigId: 901, GameId: 9001, Name: "Hardcore"},
		},
		gamesByID: map[int64]*manmanpb.Game{
			9000: {GameId: 9000, Name: "Minecraft"},
			9001: {GameId: 9001, Name: "Valheim"},
		},
	}
}

// TestHandleActivity_LiveTablePopulatedFromLiveOnlySessions_UptimeFromStart
// covers: "Live table populated from live_only sessions; uptime computed
// from session start." A 2-hour-old start renders as "2h0m0s" (Truncate to
// second), and the same running session must not also appear in History
// (the default, unfiltered History call narrows to terminal statuses).
func TestHandleActivity_LiveTablePopulatedFromLiveOnlySessions_UptimeFromStart(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: time.Now().Add(-2 * time.Hour).Unix()},
	}

	code, body := renderActivityHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if !strings.Contains(body, "2h0m0s") {
		t.Errorf("expected Live table to render uptime 2h0m0s for a session started 2h ago, got body: %s", body)
	}
	if !strings.Contains(body, "Survival") {
		t.Errorf("expected Live table to contain config name Survival, got body: %s", body)
	}
	if !strings.Contains(body, "No history for the selected filters") {
		// A running session must never double-count into History's default
		// (terminal-status-only) view.
		t.Errorf("expected empty History table (running session is not terminal), got body: %s", body)
	}
}

// TestHandleActivity_HistoryReadsSessionStatusNotSGCStatus is the
// ground-truth guard from the issue's Testing section: "assert the value
// rendered is Session.status, and add a case where ServerGameConfig.status
// differs from the session's status to prove the wrong field is not being
// read." The fixture SGC's status is "active" (a lifecycle flag, never
// "stopped"); the session's status is "stopped" (a terminal run state).
// Reading the wrong field would render "active" in History's status
// column, which never otherwise appears anywhere on this page.
func TestHandleActivity_HistoryReadsSessionStatusNotSGCStatus(t *testing.T) {
	api := baseActivityFixture()
	// SGC 55's status stays "active" (baseActivityFixture default) while
	// the session itself is terminal ("stopped") -- the two fields
	// deliberately disagree.
	api.sessions = []*manmanpb.Session{
		{SessionId: 42, ServerGameConfigId: 55, Status: "stopped", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
	}

	code, body := renderActivityHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if !strings.Contains(body, "<td>stopped</td>") {
		t.Errorf("expected History status column to render Session.status %q, got body: %s", "stopped", body)
	}
	if strings.Contains(body, "<td>active</td>") {
		t.Errorf("expected History status column to never render ServerGameConfig.status %q (the wrong field), got body: %s", "active", body)
	}
}

// TestHandleActivity_HistoryTerminalStatusesPopulated covers: "History
// table populated with terminal statuses" for the full documented
// vocabulary (stopped/crashed/completed), all surfacing without any status
// filter applied.
func TestHandleActivity_HistoryTerminalStatusesPopulated(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "stopped", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
		{SessionId: 2, ServerGameConfigId: 55, Status: "crashed", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
		{SessionId: 3, ServerGameConfigId: 55, Status: "completed", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
	}

	code, body := renderActivityHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	for _, want := range []string{"<td>stopped</td>", "<td>crashed</td>", "<td>completed</td>"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected History table to contain %q, got body: %s", want, body)
		}
	}
}

// TestHandleActivity_FilterByGame covers: "Filter by game ... narrows the
// result set." Filtering to Minecraft's game id must drop Valheim's rows
// from both tables.
func TestHandleActivity_FilterByGame(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: time.Now().Add(-1 * time.Minute).Unix()},
		{SessionId: 2, ServerGameConfigId: 66, Status: "running", StartedAt: time.Now().Add(-1 * time.Minute).Unix()},
	}

	code, body := renderActivityHTTP(t, api, "game_id=9000")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Minecraft") {
		t.Errorf("expected Minecraft's session to survive the game_id=9000 filter, got body: %s", body)
	}
	if strings.Contains(body, "Valheim") {
		t.Errorf("expected Valheim's session to be filtered out by game_id=9000, got body: %s", body)
	}
}

// TestHandleActivity_FilterByStatus covers: "Filter by ... status ...
// narrows the result set." Filtering to status=crashed must drop a
// "stopped" history row.
func TestHandleActivity_FilterByStatus(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "crashed", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
		{SessionId: 2, ServerGameConfigId: 66, Status: "stopped", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
	}

	code, body := renderActivityHTTP(t, api, "status=crashed")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "<td>crashed</td>") {
		t.Errorf("expected the crashed session to survive the status=crashed filter, got body: %s", body)
	}
	if strings.Contains(body, "<td>stopped</td>") {
		t.Errorf("expected the stopped session to be filtered out by status=crashed, got body: %s", body)
	}
}

// TestHandleActivity_FilterStateEchoedIntoControls covers: "filter state is
// echoed back into the controls" so a filtered view is linkable and
// survives reload.
func TestHandleActivity_FilterStateEchoedIntoControls(t *testing.T) {
	api := baseActivityFixture()

	_, body := renderActivityHTTP(t, api, "game_id=9000&status=crashed")

	if !strings.Contains(body, `value="9000"`) {
		t.Errorf("expected the game filter input to echo back value=9000, got body: %s", body)
	}
	if !strings.Contains(body, `<option value="crashed" selected>`) {
		t.Errorf("expected the status filter select to echo back crashed as selected, got body: %s", body)
	}
}

// TestHandleActivity_HistoryRowsLinkToSessionDetail covers: "History rows
// link to /sessions/<id>."
func TestHandleActivity_HistoryRowsLinkToSessionDetail(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 4242, ServerGameConfigId: 55, Status: "stopped", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
	}

	_, body := renderActivityHTTP(t, api, "")
	if !strings.Contains(body, `href="/sessions/4242"`) {
		t.Errorf("expected a History row linking to /sessions/4242, got body: %s", body)
	}
}

// TestHandleActivity_FleetWideAcrossMultipleServers covers: "Fleet-wide:
// sessions from more than one server all appear (guards against
// accidentally inheriting the server-scoped derivation)."
func TestHandleActivity_FleetWideAcrossMultipleServers(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: time.Now().Add(-1 * time.Minute).Unix()},
		{SessionId: 2, ServerGameConfigId: 66, Status: "running", StartedAt: time.Now().Add(-1 * time.Minute).Unix()},
	}

	_, body := renderActivityHTTP(t, api, "")
	if !strings.Contains(body, "Alpha") {
		t.Errorf("expected server Alpha's session to appear, got body: %s", body)
	}
	if !strings.Contains(body, "Beta") {
		t.Errorf("expected server Beta's session to appear too (fleet-wide, not server-scoped), got body: %s", body)
	}
}

// TestHandleActivity_EmptyAuthorizedSetHandledNotPanic covers NFR10: "Empty
// authorized set -> handled response, never a panic."
func TestHandleActivity_EmptyAuthorizedSetHandledNotPanic(t *testing.T) {
	api := &fakeActivityAPIClient{
		servers:         nil,
		sgcsByServerID:  map[int64][]*manmanpb.ServerGameConfig{},
		gameConfigsByID: map[int64]*manmanpb.GameConfig{},
		gamesByID:       map[int64]*manmanpb.Game{},
		sessions: []*manmanpb.Session{
			{SessionId: 1, ServerGameConfigId: 999, Status: "running", StartedAt: time.Now().Unix()},
		},
	}

	code, body := renderActivityHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d (empty authorized set must be a handled response, not an error); body: %s", code, http.StatusOK, body)
	}
	if !strings.Contains(body, "Nothing is live right now") {
		t.Errorf("expected an empty Live table for an empty authorized set, got body: %s", body)
	}
	if !strings.Contains(body, "No history for the selected filters") {
		t.Errorf("expected an empty History table for an empty authorized set, got body: %s", body)
	}
}

// --- FR15 (#2277): correct as-of-load state and the not-live indicator,
// with the stream never establishing ---
//
// handleActivity's first render always comes from a plain request (it never
// depends on app.sseHub -- see its doc comment), so exercising it directly
// here *is* the "stream never establishes" scenario: nothing about these
// two tests' assertions changes whether or not a browser ever successfully
// opens the /api/live/activity EventSource afterwards.

// TestHandleActivity_SSEEnabled_CorrectDataAndLiveIndicatorPresent is the
// FR15 headline test: with live updates enabled (app.sseHub set, a
// fleet-wide topic to subscribe to), a fresh load still renders real
// Live/History data *and* the not-live indicator infrastructure
// (components.LiveRegion, #2268) that will tell the operator if the stream
// never connects -- never a spinner or an empty table standing in for real
// data, and never a stream-dependent render.
func TestHandleActivity_SSEEnabled_CorrectDataAndLiveIndicatorPresent(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: time.Now().Add(-2 * time.Hour).Unix()},
		{SessionId: 2, ServerGameConfigId: 66, Status: "stopped", StartedAt: time.Now().Add(-1 * time.Hour).Unix(), EndedAt: time.Now().Unix()},
	}

	app := &App{grpc: &ControlClient{api: api}, sseHub: newTestHub()}
	defer app.sseHub.Close()

	req := httptest.NewRequest(http.MethodGet, "/activity", nil)
	w := httptest.NewRecorder()
	app.handleActivity(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	body := w.Body.String()

	// Correct as-of-load data: both the live and terminal sessions rendered
	// for real, not a placeholder.
	if !strings.Contains(body, "2h0m0s") {
		t.Errorf("expected real Live table data (uptime 2h0m0s), got body: %s", body)
	}
	if !strings.Contains(body, "<td>stopped</td>") {
		t.Errorf("expected real History table data (status stopped), got body: %s", body)
	}
	if strings.Contains(body, "Nothing is live right now") {
		t.Errorf("Live table rendered its empty state despite a live session existing -- stream-dependent empty render, not as-of-load data: %s", body)
	}

	// The not-live indicator infrastructure (#2268's components.LiveRegion),
	// wired at Activity's own SSE path and reload target (#2277) -- present
	// on first render regardless of whether the stream ever connects.
	if !strings.Contains(body, `sse-connect="/api/live/activity"`) {
		t.Errorf("expected LiveRegion wired to sse-connect=\"/api/live/activity\", got body: %s", body)
	}
	// The bare href="/activity" substring alone is ambiguous (the page's own
	// breadcrumb link also renders it via the same templ.URL(...) call), so
	// assert on the Reload anchor's own class alongside it -- unique to
	// LiveRegion's reload affordance.
	if !strings.Contains(body, `href="/activity" class="btn btn-sm btn-warning"`) {
		t.Errorf("expected LiveRegion's Reload affordance to target /activity (not /sessions), got body: %s", body)
	}
	if strings.Contains(body, `href="/sessions" class="btn btn-sm btn-warning"`) {
		t.Errorf("expected no hard-coded /sessions Reload target left over from the sessions-page origin, got body: %s", body)
	}
	if !strings.Contains(body, `id="deployments-live-status"`) {
		t.Errorf("expected the not-live indicator badge element to be present, got body: %s", body)
	}
}

// TestHandleActivity_NoSSEHub_CorrectDataNoLiveMarkup covers the other half
// of FR15's no-fallback constraint: when live updates are unavailable
// altogether (app.sseHub nil, e.g. RABBITMQ_URL unset), the page still
// renders its correct plain server-side snapshot -- stale data is never
// presented as current -- with no live-connection markup pointed at a route
// that can only ever 503 (ActivityPageData.LiveUpdatesEnabled's doc
// comment).
func TestHandleActivity_NoSSEHub_CorrectDataNoLiveMarkup(t *testing.T) {
	api := baseActivityFixture()
	api.sessions = []*manmanpb.Session{
		{SessionId: 1, ServerGameConfigId: 55, Status: "running", StartedAt: time.Now().Add(-2 * time.Hour).Unix()},
	}

	code, body := renderActivityHTTP(t, api, "")
	if code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body: %s", code, http.StatusOK, body)
	}

	if !strings.Contains(body, "2h0m0s") {
		t.Errorf("expected real Live table data even with no SSE hub, got body: %s", body)
	}
	if strings.Contains(body, "sse-connect") {
		t.Errorf("expected no sse-connect markup when app.sseHub is nil (nothing to stream from), got body: %s", body)
	}
}
