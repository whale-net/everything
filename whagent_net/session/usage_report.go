package session

import (
	"context"
	"fmt"
	"strings"
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

// Fixed SQL fragments per dimension; user input never reaches the statement text.
var periodExprs = map[UsagePeriod]string{
	UsagePeriodDay:   `to_char((tu.created_at AT TIME ZONE 'UTC')::date, 'YYYY-MM-DD')`,
	UsagePeriodWeek:  `to_char(date_trunc('week', tu.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`,
	UsagePeriodMonth: `to_char(date_trunc('month', tu.created_at AT TIME ZONE 'UTC'), 'YYYY-MM-DD')`,
}

const (
	agentExpr = `ad.agent_id`
	modelExpr = `tu.model`
)

// UsageReport runs the single parameterized aggregation over turn_usage.
// Grouped rows and the ungrouped total come from one statement via
// GROUPING SETS; empty buckets never appear.
func (s usageStore) UsageReport(ctx context.Context, q UsageReportQuery) (UsageReport, error) {
	var dims []string
	if q.Period != UsagePeriodNone && q.Period != UsagePeriodAllTime {
		e, ok := periodExprs[q.Period]
		if !ok {
			return UsageReport{}, fmt.Errorf("usage report: invalid period %d", q.Period)
		}
		dims = append(dims, e)
	}
	hasPeriod := len(dims) == 1
	if q.ByAgent {
		dims = append(dims, agentExpr)
	}
	if q.ByModel {
		dims = append(dims, modelExpr)
	}

	from := `turn_usage tu`
	if q.ByAgent {
		from += ` JOIN agent_definition ad ON ad.id = tu.agent_definition_id`
	}

	var cols string
	{
		p, a, m := `NULL::text`, `NULL::text`, `NULL::text`
		i := 0
		if hasPeriod {
			p = dims[i]
			i++
		}
		if q.ByAgent {
			a = dims[i]
			i++
		}
		if q.ByModel {
			m = dims[i]
		}
		cols = p + `, ` + a + `, ` + m
	}

	isTotal := `(true)`
	group := ``
	if len(dims) > 0 {
		list := strings.Join(dims, `, `)
		isTotal = `(GROUPING(` + list + `) = ` + fmt.Sprint((1<<len(dims))-1) + `)`
		group = ` GROUP BY GROUPING SETS ((` + list + `), ())`
	}

	query := `SELECT ` + cols + `,
		COALESCE(SUM(tu.prompt_tokens), 0)::bigint,
		COALESCE(SUM(tu.completion_tokens), 0)::bigint,
		COALESCE(SUM(tu.cost_usd), 0)::float8,
		COUNT(*)::bigint,
		COALESCE(bool_or(tu.cost_estimated), false),
		` + isTotal + ` AS is_total
		FROM ` + from + `
		WHERE ($1::timestamptz IS NULL OR tu.created_at >= $1)
		  AND ($2::timestamptz IS NULL OR tu.created_at < $2)` + group + `
		ORDER BY is_total, 1 DESC NULLS LAST, 2 NULLS LAST, 3 NULLS LAST`

	rows, err := s.pool.Query(ctx, query, q.From, q.To)
	if err != nil {
		return UsageReport{}, fmt.Errorf("usage report: %w", err)
	}
	defer rows.Close()

	var rep UsageReport
	for rows.Next() {
		var p, a, m *string
		var r UsageReportRow
		var total bool
		if err := rows.Scan(&p, &a, &m, &r.PromptTokens, &r.CompletionTokens, &r.CostUSD, &r.Turns, &r.CostIncludesEstimate, &total); err != nil {
			return UsageReport{}, fmt.Errorf("usage report: scan: %w", err)
		}
		if total {
			rep.Total = r
			// No grouping dimension: the total is also the single bucket.
			if len(dims) == 0 && r.Turns > 0 {
				rep.Rows = append(rep.Rows, r)
			}
			continue
		}
		if p != nil {
			r.PeriodStart = *p
		}
		if a != nil {
			r.AgentID = *a
		}
		if m != nil {
			r.Model = *m
		}
		if r.Turns == 0 {
			continue
		}
		rep.Rows = append(rep.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return UsageReport{}, fmt.Errorf("usage report: %w", err)
	}
	return rep, nil
}
