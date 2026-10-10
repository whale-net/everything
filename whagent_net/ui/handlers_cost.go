// GET /cost: the usage report page (period/agent/model grouping) built on
// SessionService.GetUsageReport.
package main

import (
	"fmt"
	"net/http"
	"time"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/logging"
	whagentpb "github.com/whale-net/everything/whagent_net/protos"
	"github.com/whale-net/everything/whagent_net/ui/components"
)

// parseCostFilters reads period/agent/model from the query string,
// defaulting to period=day, by_agent=true. Defaults apply only when period
// is absent, since a submitted form omits unchecked toggles.
func parseCostFilters(r *http.Request) components.CostFilters {
	q := r.URL.Query()
	period := q.Get("period")
	switch period {
	case "day", "week", "month", "all", "none":
	case "":
		return components.CostFilters{Period: "day", ByAgent: true, ByModel: q.Get("model") == "1"}
	default:
		period = "day"
	}
	return components.CostFilters{Period: period, ByAgent: q.Get("agent") == "1", ByModel: q.Get("model") == "1"}
}

// buildUsageReportRequest maps filters onto the gRPC request.
func buildUsageReportRequest(f components.CostFilters) *whagentpb.GetUsageReportRequest {
	p := whagentpb.UsagePeriod_USAGE_PERIOD_NONE
	switch f.Period {
	case "day":
		p = whagentpb.UsagePeriod_USAGE_PERIOD_DAY
	case "week":
		p = whagentpb.UsagePeriod_USAGE_PERIOD_WEEK
	case "month":
		p = whagentpb.UsagePeriod_USAGE_PERIOD_MONTH
	case "all":
		p = whagentpb.UsagePeriod_USAGE_PERIOD_ALL_TIME
	}
	return &whagentpb.GetUsageReportRequest{Period: p, ByAgent: f.ByAgent, ByModel: f.ByModel}
}

// formatPeriodLabel renders a UsageRow.period_start (YYYY-MM-DD) for the
// given period.
func formatPeriodLabel(period, periodStart string) string {
	switch period {
	case "all":
		return "All time"
	case "none":
		return ""
	}
	t, err := time.Parse("2006-01-02", periodStart)
	if err != nil {
		return periodStart
	}
	switch period {
	case "week":
		y, w := t.ISOWeek()
		return fmt.Sprintf("%04d-W%02d", y, w)
	case "month":
		return t.Format("2006-01")
	}
	return periodStart
}

func (app *App) handleCost(w http.ResponseWriter, r *http.Request) {
	logger := logging.Get("main")
	ctx := r.Context()

	filters := parseCostFilters(r)
	data := components.CostPageData{
		Filters: filters,
		Layout: components.LayoutData{
			Title:  "Cost",
			Active: "Cost",
			User:   htmxauth.GetUser(ctx),
		},
	}

	resp, err := app.session.Client().GetUsageReport(ctx, buildUsageReportRequest(filters))
	if err != nil {
		logger.Error("failed to get usage report", "error", err)
		data.FetchError = "the whagent-net api is unavailable"
	} else {
		for _, row := range resp.GetRows() {
			data.Rows = append(data.Rows, components.CostRowView{
				PeriodLabel:      formatPeriodLabel(filters.Period, row.GetPeriodStart()),
				Agent:            row.GetAgentId(),
				Model:            row.GetModel(),
				PromptTokens:     row.GetPromptTokens(),
				CompletionTokens: row.GetCompletionTokens(),
				Turns:            row.GetTurns(),
				CostUSD:          row.GetCostUsd(),
				Estimated:        row.GetCostIncludesEstimate(),
			})
		}
		t := resp.GetTotal()
		data.Total = components.CostRowView{
			PromptTokens:     t.GetPromptTokens(),
			CompletionTokens: t.GetCompletionTokens(),
			Turns:            t.GetTurns(),
			CostUSD:          t.GetCostUsd(),
			Estimated:        t.GetCostIncludesEstimate(),
		}
	}

	if r.Header.Get("HX-Request") != "" {
		if err := components.CostTable(data).Render(ctx, w); err != nil {
			logger.Error("failed to render cost fragment", "error", err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}
	if err := RenderTempl(w, r, data.Layout.Title, components.Cost(data)); err != nil {
		logger.Error("failed to render cost page", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
	}
}
