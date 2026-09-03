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

// This file guards #1626's handleSessions wiring: DeploymentRows must be
// derived from each SGC's *latest* session -- via a dedicated unfiltered
// ListSessionsWithFilters call -- not from the pre-existing live-only fetch
// (which returns nothing for a stopped/crashed/lost deployment) and not
// from the page's own filtered `sessions` slice (which is shaped by the
// user's status/live_only/server_game_config_id query params and would
// silently mis-derive the latest session whenever a filter is applied).
//
// fakeSessionsAPIClient embeds the nil manmanpb.ManManAPIClient interface
// and overrides only the RPCs handleSessions' DeploymentRows call graph
// reaches for these scenarios (ListServers, ListServerGameConfigs,
// GetGameConfig, ListSessions); any other call panics on the nil embedded
// interface, which is deliberate -- it fails loudly instead of silently
// returning a zero value if the call graph ever grows. GetGameConfig always
// errors here (display-name resolution isn't what these tests guard;
// sessions_deployment_row_test.go and the DisplayName field already cover
// the fallback-to-"SGC %d" path via the handler's existing tolerate-and-
// continue behavior), so GetGame is never reached and doesn't need a fake.
//
// ListSessions dispatches on the request shape to stand in for
// handleSessions' three distinct ListSessionsWithFilters calls:
//   - PageSize == 200            -> the new unfiltered "all sessions for
//     deployment rows" fetch this task adds.
//   - LiveOnly == true (PageSize 100) -> the pre-existing live-session fetch
//     used for the Live Session cell.
//   - otherwise                  -> the page's own filtered `sessions` list.
//
// mutation-tested (verified red, by hand, then reverted): changing
// handleSessions' `deploymentRows` loop to use `liveSessionByConfig` instead
// of `components.LatestSession(sessionsBySGC[...])` for LatestSession made
// TestHandleSessions_DeploymentRows_LatestSessionNotLiveOnly fail -- the
// stopped-only SGC's row went back to "Never started" / no Start button --
// while compiling cleanly; reverting restored green.

type fakeSessionsAPIClient struct {
	manmanpb.ManManAPIClient

	servers       []*manmanpb.Server
	serverConfigs []*manmanpb.ServerGameConfig
	mainSessions  []*manmanpb.Session
	liveSessions  []*manmanpb.Session
	allSessions   []*manmanpb.Session
	allSessionErr error
}

func (f *fakeSessionsAPIClient) ListServers(ctx context.Context, in *manmanpb.ListServersRequest, opts ...grpc.CallOption) (*manmanpb.ListServersResponse, error) {
	return &manmanpb.ListServersResponse{Servers: f.servers}, nil
}

func (f *fakeSessionsAPIClient) ListServerGameConfigs(ctx context.Context, in *manmanpb.ListServerGameConfigsRequest, opts ...grpc.CallOption) (*manmanpb.ListServerGameConfigsResponse, error) {
	return &manmanpb.ListServerGameConfigsResponse{Configs: f.serverConfigs}, nil
}

func (f *fakeSessionsAPIClient) GetGameConfig(ctx context.Context, in *manmanpb.GetGameConfigRequest, opts ...grpc.CallOption) (*manmanpb.GetGameConfigResponse, error) {
	return nil, fmt.Errorf("no game config configured for id %d", in.ConfigId)
}

func (f *fakeSessionsAPIClient) ListSessions(ctx context.Context, in *manmanpb.ListSessionsRequest, opts ...grpc.CallOption) (*manmanpb.ListSessionsResponse, error) {
	if in.PageSize == 200 {
		if f.allSessionErr != nil {
			return nil, f.allSessionErr
		}
		return &manmanpb.ListSessionsResponse{Sessions: f.allSessions}, nil
	}
	if in.LiveOnly {
		return &manmanpb.ListSessionsResponse{Sessions: f.liveSessions}, nil
	}
	return &manmanpb.ListSessionsResponse{Sessions: f.mainSessions}, nil
}

func newSessionsTestApp(api *fakeSessionsAPIClient) *App {
	return &App{grpc: &ControlClient{api: api}}
}

func renderSessions(t *testing.T, app *App, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	app.handleSessions(w, req)
	return w
}

// deploymentRowSection isolates a single deployment row's rendered markup
// (from its `id="deployment-row-<sgcID>"` opening tag to the closing
// </tr>), mirroring handlers_sgc_test.go's statusConnectSection/
// sessionHistorySection helpers of the same shape.
func deploymentRowSection(t *testing.T, body string, sgcID int64) string {
	t.Helper()
	marker := fmt.Sprintf(`id="deployment-row-%d"`, sgcID)
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("expected a deployment row with %q in rendered body, got %q", marker, body)
	}
	end := strings.Index(body[start:], "</tr>")
	if end < 0 {
		t.Fatalf("expected a closing </tr> after the deployment row, got %q", body[start:])
	}
	return body[start : start+end]
}

// --- 1. latest session not live-only: the regression this task exists to prevent

func TestHandleSessions_DeploymentRows_LatestSessionNotLiveOnly(t *testing.T) {
	api := &fakeSessionsAPIClient{
		servers: []*manmanpb.Server{{ServerId: 1, Name: "Alpha", Status: "online"}},
		serverConfigs: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: 100, ServerId: 1, Status: "active"},
		},
		// Not live: the pre-existing live-only fetch returns nothing for
		// this SGC.
		liveSessions: nil,
		mainSessions: nil,
		allSessions: []*manmanpb.Session{
			{SessionId: 1, ServerGameConfigId: 100, Status: "stopped", StartedAt: 100},
		},
	}
	app := newSessionsTestApp(api)

	w := renderSessions(t, app, "/sessions?server_id=1")
	if w.Code != http.StatusOK {
		t.Fatalf("handleSessions status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	section := deploymentRowSection(t, w.Body.String(), 100)

	if strings.Contains(section, "Never started") {
		t.Errorf("expected the row's LatestSession to resolve from the unfiltered fetch (not \"Never started\"), got section %q", section)
	}
	if !strings.Contains(section, ">Start<") {
		t.Errorf("expected CanStart true (stopped latest session) to render a Start button, got section %q", section)
	}
}

// --- 2. deployment rows unaffected by the page's status filter -------------

func TestHandleSessions_DeploymentRows_UnaffectedByStatusFilter(t *testing.T) {
	api := &fakeSessionsAPIClient{
		servers: []*manmanpb.Server{{ServerId: 2, Name: "Beta", Status: "online"}},
		serverConfigs: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: 200, ServerId: 2, Status: "active"},
		},
		liveSessions: nil,
		// The filtered `sessions` list (status=running) has nothing for this
		// SGC -- its real latest session is crashed, not running.
		mainSessions: nil,
		allSessions: []*manmanpb.Session{
			{SessionId: 2, ServerGameConfigId: 200, Status: "crashed", StartedAt: 100},
		},
	}
	app := newSessionsTestApp(api)

	w := renderSessions(t, app, "/sessions?server_id=2&status=running")
	if w.Code != http.StatusOK {
		t.Fatalf("handleSessions status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	section := deploymentRowSection(t, w.Body.String(), 200)

	if !strings.Contains(section, "crashed") {
		t.Errorf("expected the row to still report the crashed latest session despite the status=running filter, got section %q", section)
	}
	if !strings.Contains(section, ">Start<") || !strings.Contains(section, ">Restart<") {
		t.Errorf("expected Start+Restart (crashed) to be available regardless of the page filter, got section %q", section)
	}
}

// --- 3. SGC with zero sessions: no Start action -----------------------------

func TestHandleSessions_DeploymentRows_NoSessions(t *testing.T) {
	api := &fakeSessionsAPIClient{
		servers: []*manmanpb.Server{{ServerId: 3, Name: "Gamma", Status: "online"}},
		serverConfigs: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: 300, ServerId: 3, Status: "active"},
		},
		liveSessions: nil,
		mainSessions: nil,
		allSessions:  nil,
	}
	app := newSessionsTestApp(api)

	w := renderSessions(t, app, "/sessions?server_id=3")
	if w.Code != http.StatusOK {
		t.Fatalf("handleSessions status = %d, want %d; body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	section := deploymentRowSection(t, w.Body.String(), 300)

	if !strings.Contains(section, "Never started") {
		t.Errorf("expected a \"Never started\" indication for an SGC with zero sessions, got section %q", section)
	}
	if strings.Contains(section, ">Start<") {
		t.Errorf("expected no Start action for an SGC with zero sessions (FR1), got section %q", section)
	}
}

// --- 4. the unfiltered fetch failing still renders 200, not 500 ------------

func TestHandleSessions_ListSessionsError_StillRenders(t *testing.T) {
	api := &fakeSessionsAPIClient{
		servers: []*manmanpb.Server{{ServerId: 4, Name: "Delta", Status: "online"}},
		serverConfigs: []*manmanpb.ServerGameConfig{
			{ServerGameConfigId: 400, ServerId: 4, Status: "active"},
		},
		liveSessions:  nil,
		mainSessions:  nil,
		allSessionErr: fmt.Errorf("boom: deployment-row fetch unavailable"),
	}
	app := newSessionsTestApp(api)

	w := renderSessions(t, app, "/sessions?server_id=4")
	if w.Code != http.StatusOK {
		t.Fatalf("handleSessions status = %d, want %d (page must still render on fetch failure); body: %s", w.Code, http.StatusOK, w.Body.String())
	}
	// The handler tolerates the fetch failure by treating every SGC as
	// having no session data (rather than failing the page or dropping the
	// row entirely) -- same shape as the zero-sessions case: the row still
	// renders, with no Start/Stop/Restart action available.
	section := deploymentRowSection(t, w.Body.String(), 400)
	if !strings.Contains(section, "Never started") {
		t.Errorf("expected the row to fall back to a \"Never started\" no-session state on fetch failure, got section %q", section)
	}
	if strings.Contains(section, ">Start<") || strings.Contains(section, ">Stop<") || strings.Contains(section, ">Restart<") {
		t.Errorf("expected no action controls on a fetch-failure fallback row, got section %q", section)
	}
}
