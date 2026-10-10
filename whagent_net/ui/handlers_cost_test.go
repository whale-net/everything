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

type fakeUsageReportServer struct {
	whagentpb.UnimplementedSessionServiceServer

	resp    *whagentpb.GetUsageReportResponse
	lastReq *whagentpb.GetUsageReportRequest
}

func (f *fakeUsageReportServer) GetUsageReport(ctx context.Context, req *whagentpb.GetUsageReportRequest) (*whagentpb.GetUsageReportResponse, error) {
	f.lastReq = req
	if f.resp == nil {
		return &whagentpb.GetUsageReportResponse{Total: &whagentpb.UsageTotals{}}, nil
	}
	return f.resp, nil
}

func renderCost(t *testing.T, server *fakeUsageReportServer, query string, hx bool) *httptest.ResponseRecorder {
	t.Helper()
	app := &App{auth: devModeAuthenticator(t), session: newBufconnUISessionClient(t, server)}
	wrapped := app.auth.RequireAuthFunc(app.auth.WithAccessToken(app.handleCost))
	target := "/cost"
	if query != "" {
		target += "?" + query
	}
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if hx {
		req.Header.Set("HX-Request", "true")
	}
	w := httptest.NewRecorder()
	wrapped(w, req)
	return w
}

func TestHandleCost_QueryMapsToRequest(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		period  whagentpb.UsagePeriod
		byAgent bool
		byModel bool
	}{
		{"defaults: day, by agent", "", whagentpb.UsagePeriod_USAGE_PERIOD_DAY, true, false},
		{"period absent, model only keeps agent default", "model=1", whagentpb.UsagePeriod_USAGE_PERIOD_DAY, true, true},
		{"unchecked agent toggle stays off when period submitted", "period=week", whagentpb.UsagePeriod_USAGE_PERIOD_WEEK, false, false},
		{"week with agent and model", "period=week&agent=1&model=1", whagentpb.UsagePeriod_USAGE_PERIOD_WEEK, true, true},
		{"month by model only", "period=month&model=1", whagentpb.UsagePeriod_USAGE_PERIOD_MONTH, false, true},
		{"all time", "period=all&agent=1", whagentpb.UsagePeriod_USAGE_PERIOD_ALL_TIME, true, false},
		{"none", "period=none", whagentpb.UsagePeriod_USAGE_PERIOD_NONE, false, false},
		{"invalid period falls back to day", "period=bogus&agent=1", whagentpb.UsagePeriod_USAGE_PERIOD_DAY, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := &fakeUsageReportServer{}
			w := renderCost(t, server, tc.query, true)
			require.Equal(t, http.StatusOK, w.Code, w.Body.String())
			require.NotNil(t, server.lastReq)
			require.Equal(t, tc.period, server.lastReq.GetPeriod())
			require.Equal(t, tc.byAgent, server.lastReq.GetByAgent())
			require.Equal(t, tc.byModel, server.lastReq.GetByModel())
		})
	}
}

func TestHandleCost_FragmentVsFullPage(t *testing.T) {
	server := &fakeUsageReportServer{}
	frag := renderCost(t, server, "", true).Body.String()
	require.NotContains(t, frag, "<html")
	require.NotContains(t, frag, `hx-get="/cost"`)

	full := renderCost(t, server, "", false).Body.String()
	require.Contains(t, full, `hx-get="/cost"`)
	require.Contains(t, full, `id="cost-table"`)
}

func TestHandleCost_EstimatedBadgeAndEmptyState(t *testing.T) {
	server := &fakeUsageReportServer{resp: &whagentpb.GetUsageReportResponse{
		Rows: []*whagentpb.UsageRow{
			{PeriodStart: "2024-01-03", AgentId: "a1", PromptTokens: 10, CompletionTokens: 5, Turns: 1, CostUsd: 0.5, CostIncludesEstimate: true},
			{PeriodStart: "2024-01-04", AgentId: "a2", PromptTokens: 1, CompletionTokens: 1, Turns: 1, CostUsd: 0.25},
		},
		Total: &whagentpb.UsageTotals{PromptTokens: 11, CompletionTokens: 6, Turns: 2, CostUsd: 0.75, CostIncludesEstimate: true},
	}}
	body := renderCost(t, server, "period=day&agent=1", true).Body.String()
	require.Equal(t, 2, strings.Count(body, ">estimated<"), "badge on the estimated row and the total only")
	require.Contains(t, body, "2024-01-03")
	require.Contains(t, body, "$0.7500")

	empty := renderCost(t, &fakeUsageReportServer{}, "", true).Body.String()
	require.Contains(t, empty, "No usage recorded")
	require.NotContains(t, empty, "<table")
}

func TestFormatPeriodLabel(t *testing.T) {
	require.Equal(t, "2024-01-03", formatPeriodLabel("day", "2024-01-03"))
	require.Equal(t, "2024-W01", formatPeriodLabel("week", "2024-01-01"))
	require.Equal(t, "2020-W53", formatPeriodLabel("week", "2021-01-03"))
	require.Equal(t, "2024-02", formatPeriodLabel("month", "2024-02-01"))
	require.Equal(t, "All time", formatPeriodLabel("all", ""))
	require.Equal(t, "", formatPeriodLabel("none", ""))
	require.Equal(t, "garbage", formatPeriodLabel("week", "garbage"))
}
