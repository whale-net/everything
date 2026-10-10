// GET /cost: the usage report page (period/agent/model grouping) built on
// SessionService.GetUsageReport.
package main

import (
	"net/http"

	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// parseCostFilters reads period/agent/model from the query string,
// defaulting to period=day, by_agent=true.
func parseCostFilters(r *http.Request) components.CostFilters {
	return components.CostFilters{Period: "day", ByAgent: true}
}

// buildUsageReportRequest maps filters onto the gRPC request.
func buildUsageReportRequest(f components.CostFilters) *whagentpb.GetUsageReportRequest {
	return &whagentpb.GetUsageReportRequest{}
}

// formatPeriodLabel renders a UsageRow.period_start for the given period.
func formatPeriodLabel(period, periodStart string) string {
	return periodStart
}

func (app *App) handleCost(w http.ResponseWriter, r *http.Request) {
	http.Error(w, "not implemented", http.StatusNotImplemented)
}
