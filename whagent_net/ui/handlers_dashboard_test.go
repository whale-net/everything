package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
)

type fakeDashboardServer struct {
	whagentpb.UnimplementedSessionServiceServer
	resp *whagentpb.GetDashboardSummaryResponse
}

func (f *fakeDashboardServer) GetDashboardSummary(context.Context, *whagentpb.GetDashboardSummaryRequest) (*whagentpb.GetDashboardSummaryResponse, error) {
	return f.resp, nil
}

func (f *fakeDashboardServer) ListSessions(context.Context, *whagentpb.ListSessionsRequest) (*whagentpb.ListSessionsResponse, error) {
	return &whagentpb.ListSessionsResponse{}, nil
}

func newDashboardMux(t *testing.T, resp *whagentpb.GetDashboardSummaryResponse) *http.ServeMux {
	t.Helper()
	app := &App{
		auth:        devModeAuthenticator(t),
		session:     newBufconnUISessionClient(t, &fakeDashboardServer{resp: resp}),
		mcpProvider: newTestMCPProvider(t),
	}
	mux := http.NewServeMux()
	app.setupRoutes(mux)
	return mux
}

func getPath(mux *http.ServeMux, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func TestDashboard_RendersZeroRowsAndLinks(t *testing.T) {
	mux := newDashboardMux(t, &whagentpb.GetDashboardSummaryResponse{
		UtcDate:              "2026-03-15",
		CostIncludesEstimate: true,
		Agents: []*whagentpb.AgentTraffic{
			{AgentId: "idle-agent"},
			{AgentId: "busy-agent", SessionsToday: 3, TurnsToday: 7},
		},
		TotalCostUsdToday: 1.5,
	})
	w := getPath(mux, "/")
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	body := w.Body.String()
	require.Contains(t, body, "idle-agent")
	require.Contains(t, body, "busy-agent")
	require.Contains(t, body, "<td>0</td>", "zero rows must render their zeros")
	require.Contains(t, body, "<td>7</td>")
	require.Contains(t, body, "2026-03-15")
	require.Contains(t, body, "UTC")
	require.Contains(t, body, "estimated")
	require.Contains(t, body, "$1.5000")
	for _, href := range []string{`href="/sessions"`, `href="/agents"`, `href="/cost"`} {
		require.Contains(t, body, href)
	}
}

func TestDashboard_NoEstimateBadgeWhenExact(t *testing.T) {
	mux := newDashboardMux(t, &whagentpb.GetDashboardSummaryResponse{UtcDate: "2026-03-15"})
	body := getPath(mux, "/").Body.String()
	require.False(t, strings.Contains(body, "estimated"))
}

func TestRoutes_RootIsDashboardAndSessionsIsList(t *testing.T) {
	mux := newDashboardMux(t, &whagentpb.GetDashboardSummaryResponse{UtcDate: "2026-03-15"})

	root := getPath(mux, "/")
	require.Equal(t, http.StatusOK, root.Code)
	require.Contains(t, root.Body.String(), "Total cost today")

	list := getPath(mux, "/sessions")
	require.Equal(t, http.StatusOK, list.Code, list.Body.String())
	require.NotContains(t, list.Body.String(), "Total cost today")

	require.Equal(t, http.StatusNotFound, getPath(mux, "/nope").Code)
}
