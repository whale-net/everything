package main

import (
	"net/http"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// handleDashboard is GET /: today's (UTC) sessions and turns per agent and
// total cost today. "/" is also the mux catch-all, so any other path 404s.
func (app *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	logger := logging.Get("main")
	ctx := r.Context()

	data := components.DashboardPageData{
		Layout: components.LayoutData{Title: "Dashboard", Active: "Dashboard", User: htmxauth.GetUser(ctx)},
	}
	resp, err := app.session.GetDashboardSummary(ctx)
	if err != nil {
		logger.Error("failed to load dashboard summary", "error", err)
		data.FetchError = "the whagent-net api is unavailable"
	} else {
		data.UTCDate = resp.GetUtcDate()
		data.TotalCostUSDToday = resp.GetTotalCostUsdToday()
		data.CostIncludesEstimate = resp.GetCostIncludesEstimate()
		for _, a := range resp.GetAgents() {
			data.Agents = append(data.Agents, components.AgentTrafficView{
				AgentID: a.GetAgentId(), SessionsToday: a.GetSessionsToday(), TurnsToday: a.GetTurnsToday(),
			})
		}
	}

	if err := RenderTempl(w, r, data.Layout.Title, components.Dashboard(data)); err != nil {
		logger.Error("failed to render dashboard", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
