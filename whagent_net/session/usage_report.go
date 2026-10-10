package session

import (
	"context"
	"errors"
	"time"
)

// UsagePeriod is a UTC calendar bucket size for UsageReport.
type UsagePeriod int

const (
	UsagePeriodNone UsagePeriod = iota
	UsagePeriodDay
	UsagePeriodWeek
	UsagePeriodMonth
	UsagePeriodAllTime
)

// UsageReportQuery selects the dimensions and optional [From, To) bounds.
type UsageReportQuery struct {
	Period  UsagePeriod
	ByAgent bool
	ByModel bool
	From    *time.Time
	To      *time.Time
}

// UsageReportRow is one aggregated bucket. PeriodStart is "" for
// ALL_TIME/NONE; AgentID/Model are "" when not grouped.
type UsageReportRow struct {
	PeriodStart          string
	AgentID              string
	Model                string
	PromptTokens         int64
	CompletionTokens     int64
	CostUSD              float64
	Turns                int64
	CostIncludesEstimate bool
}

// UsageReport is the grouped rows plus the ungrouped total over the same filter.
type UsageReport struct {
	Rows  []UsageReportRow
	Total UsageReportRow
}

// UsageReport runs the single parameterized aggregation over turn_usage.
func (s usageStore) UsageReport(ctx context.Context, q UsageReportQuery) (UsageReport, error) {
	return UsageReport{}, errors.New("usage report: not implemented")
}
