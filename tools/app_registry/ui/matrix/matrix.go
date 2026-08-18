// Package matrix builds the FR-4/FR-5 deployments matrix (screen
// 10-environment-status): one row per PROMOTABLE entity (chart, or a
// standalone image-deploy-unit app), one column per environment, with a
// VIA_CHART app reachable only as a child of its owning chart's row —
// never a positional sibling of a PROMOTABLE row.
//
// This package holds pure data shaping only — no gRPC calls of its own, so
// both main (which does the fetching) and pages (which renders) can import
// it without a cycle.
package matrix

import (
	"sort"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// ColumnResult is the outcome of one environment's GetEnvironmentState call.
// Err set means that environment's fetch failed — callers must render an
// explicit per-environment error, never a blank/healthy-looking cell
// (NFR-6).
type ColumnResult struct {
	Env  *pb.Environment
	Resp *pb.GetEnvironmentStateResponse
	Err  error
}

// RowKind distinguishes a chart row (a Helm chart, itself PROMOTABLE) from a
// standalone-image row (an app whose own deploy_unit is
// DEPLOY_UNIT_IMAGE — no owning chart) or standalone-binary row (a CLI or
// binary tool). All are top-level PROMOTABLE rows.
// A VIA_CHART app is never any of these at the top level — it only ever
// appears as a Row.Children entry under its owning chart.
type RowKind int

const (
	RowKindChart RowKind = iota
	RowKindStandaloneImage
	RowKindStandaloneBinary
	RowKindViaChartApp
)

// Cell is one (row, environment) intersection.
type Cell struct {
	EnvKey string

	// ColumnErr is set when this environment's GetEnvironmentState call
	// failed outright — the cell must render as an explicit error, never
	// as "not promoted" (which would misleadingly read as healthy/empty).
	ColumnErr error

	// Entry is nil when this row's entity is not currently promoted to
	// this environment (a legitimate state — e.g. leaflab-api / prod in
	// the wireframe). Non-nil means promoted.
	Entry *pb.EnvironmentStateEntry

	// Drifted mirrors len(Entry.GetDrift()) > 0 — an overridden image no
	// longer matching this chart's pin (FR-9). Only meaningful on chart
	// rows; the drift entries themselves name which composed app diverged.
	Drifted bool
}

// Row is one top-level PROMOTABLE entity, or (via Children) the VIA_CHART
// apps composed into it.
type Row struct {
	Kind     RowKind
	Key      string // "chart:<id>" or "app:<id>" — stable identity for cell lookups
	Domain   string
	FullName string

	Cells []*Cell // aligned index-for-index with Matrix.Environments

	// Children holds this chart's composed VIA_CHART apps. Always empty
	// for non-chart rows. Never rendered as siblings of top-level rows —
	// FR-5/NFR-12.
	Children []*Row

	// AnyDrift is the FR-9 rollup: true when any cell in this row (across
	// every environment) is drifted. Used to badge the chart's summary row
	// before it is expanded.
	AnyDrift bool
}

// Matrix is the full screen-10 data model.
type Matrix struct {
	Environments []*pb.Environment
	Rows         []*Row

	// AsOf is the Unix timestamp the matrix was rendered for (FR-7); 0
	// means "current state".
	AsOf int64

	// ColumnErrors is EnvKey -> error for every environment whose fetch
	// failed, so callers can render a single top-of-page summary in
	// addition to the per-cell error (NFR-6: never silently blank).
	ColumnErrors map[string]error
}

func ownerKey(a *pb.Artifact) string {
	if a.GetChartId() != "" {
		return "chart:" + a.GetChartId()
	}
	return "app:" + a.GetAppId()
}

func buildCell(col ColumnResult, key string) *Cell {
	cell := &Cell{EnvKey: col.Env.GetKey(), ColumnErr: col.Err}
	if col.Err != nil || col.Resp == nil {
		return cell
	}
	for _, e := range col.Resp.GetEntries() {
		if ownerKey(e.GetArtifact()) == key {
			cell.Entry = e
			cell.Drifted = len(e.GetDrift()) > 0
			return cell
		}
	}
	return cell
}

// CellsForKey builds one Cell per environment (aligned index-for-index with
// environments) for the entity identified by key ("chart:<id>" or
// "app:<id>", matching Row.Key's convention). Exported so callers outside
// this package — e.g. the apps catalog (screen 20) and app detail (screen
// 11), which need per-environment promotability for entities Build's own
// row/child nesting doesn't surface flat (a DEPLOY_UNIT_NONE app, or a
// VIA_CHART app looked up on its own rather than as a chart's child) — can
// reuse the same columns data Build consumes instead of re-deriving cell
// semantics.
func CellsForKey(environments []*pb.Environment, columns map[string]ColumnResult, key string) []*Cell {
	cells := make([]*Cell, 0, len(environments))
	for _, env := range environments {
		col, ok := columns[env.GetKey()]
		if !ok {
			col = ColumnResult{Env: env}
		}
		cells = append(cells, buildCell(col, key))
	}
	return cells
}

func anyDrift(cells []*Cell) bool {
	for _, c := range cells {
		if c.Drifted {
			return true
		}
	}
	return false
}

// Build assembles the matrix from already-fetched proto data. charts and
// apps should be the full ACTIVE(+MISSING) sets from ListCharts/ListApps;
// columns must have one entry per key in environments (a missing key is
// treated as an empty/unfetched column, which still renders — every
// environment always gets a column, per FR-4).
func Build(charts []*pb.Chart, apps []*pb.App, environments []*pb.Environment, columns map[string]ColumnResult) *Matrix {
	appByID := make(map[string]*pb.App, len(apps))
	for _, a := range apps {
		appByID[a.GetAppId()] = a
	}
	chartAppIDs := make(map[string]bool)
	for _, c := range charts {
		for _, id := range c.GetAppIds() {
			chartAppIDs[id] = true
		}
	}

	rows := make([]*Row, 0, len(charts)+len(apps))

	for _, c := range charts {
		key := "chart:" + c.GetChartId()
		row := &Row{
			Kind:     RowKindChart,
			Key:      key,
			Domain:   c.GetDomain(),
			FullName: c.GetFullName(),
			Cells:    CellsForKey(environments, columns, key),
		}
		row.AnyDrift = anyDrift(row.Cells)

		for _, appID := range c.GetAppIds() {
			app, ok := appByID[appID]
			if !ok {
				// Composed app not (yet) resolved in this ListApps read —
				// skip rather than render a row with no identity.
				continue
			}
			childKey := "app:" + appID
			child := &Row{
				Kind:     RowKindViaChartApp,
				Key:      childKey,
				Domain:   app.GetDomain(),
				FullName: app.GetFullName(),
				Cells:    CellsForKey(environments, columns, childKey),
			}
			row.Children = append(row.Children, child)
		}
		sort.Slice(row.Children, func(i, j int) bool { return row.Children[i].FullName < row.Children[j].FullName })

		rows = append(rows, row)
	}

	for _, app := range apps {
		isBinaryTool := app.GetAppType() == "cli" || app.GetAppType() == "binary"
		if app.GetDeployUnit() != appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE && !isBinaryTool {
			continue
		}
		// Guard against inconsistent data: a standalone app should
		// never also be a chart's composed member (that combination
		// implies DEPLOY_UNIT_CHART), but never render it twice if it is.
		if chartAppIDs[app.GetAppId()] {
			continue
		}
		rowKind := RowKindStandaloneImage
		if isBinaryTool {
			rowKind = RowKindStandaloneBinary
		}
		key := "app:" + app.GetAppId()
		rows = append(rows, &Row{
			Kind:     rowKind,
			Key:      key,
			Domain:   app.GetDomain(),
			FullName: app.GetFullName(),
			Cells:    CellsForKey(environments, columns, key),
		})
	}

	sort.SliceStable(rows, func(i, j int) bool { return rows[i].FullName < rows[j].FullName })

	columnErrors := make(map[string]error)
	for key, col := range columns {
		if col.Err != nil {
			columnErrors[key] = col.Err
		}
	}

	return &Matrix{
		Environments: environments,
		Rows:         rows,
		ColumnErrors: columnErrors,
	}
}

// EnvSummary is the dashboard's (screen 01) per-environment rollup: how many
// PROMOTABLE/VIA_CHART entities are currently promoted, how many have
// drifted, and whether this environment's own GetEnvironmentState call
// failed (FR-10/FR-9 — a failed environment must read as "unknown", never
// as healthy, NFR-6).
type EnvSummary struct {
	Env           *pb.Environment
	PromotedCount int
	DriftCount    int
	Err           error
}

// countRows walks row and its children, incrementing promoted/drift counts
// at column index i.
func countRows(rows []*Row, i int, promoted, drift *int) {
	for _, row := range rows {
		if i < len(row.Cells) {
			if row.Cells[i].Entry != nil {
				*promoted++
			}
			if row.Cells[i].Drifted {
				*drift++
			}
		}
		countRows(row.Children, i, promoted, drift)
	}
}

// EnvSummaries returns one EnvSummary per environment, in the same order as
// m.Environments.
func (m *Matrix) EnvSummaries() []EnvSummary {
	summaries := make([]EnvSummary, 0, len(m.Environments))
	for i, env := range m.Environments {
		s := EnvSummary{Env: env, Err: m.ColumnErrors[env.GetKey()]}
		if s.Err == nil {
			countRows(m.Rows, i, &s.PromotedCount, &s.DriftCount)
		}
		summaries = append(summaries, s)
	}
	return summaries
}

// TotalDrift is the dashboard's top-level drift count across every
// environment (FR-9).
func (m *Matrix) TotalDrift() int {
	total := 0
	for _, s := range m.EnvSummaries() {
		total += s.DriftCount
	}
	return total
}

// OwnerFullName resolves a "chart:<id>"/"app:<id>" key (Promotion.GetChartId
// / GetAppId, prefixed the same way Row.Key is) to the display name already
// loaded into the matrix — so the dashboard's recent-promotions table can
// name the app/chart a Promotion belongs to without an extra registry call.
// Returns "" if key isn't a row or child row in this matrix (e.g. an app
// since archived).
func (m *Matrix) OwnerFullName(key string) string {
	var find func(rows []*Row) string
	find = func(rows []*Row) string {
		for _, row := range rows {
			if row.Key == key {
				return row.FullName
			}
			if name := find(row.Children); name != "" {
				return name
			}
		}
		return ""
	}
	return find(m.Rows)
}

// DriftRow is one flattened entry for the screen-40 (#650) drift-audit
// table: a single DriftEntry, resolved to the chart/app/environment it
// belongs to plus (when the drifted app's own override cell is resolvable)
// the version and promotion timestamp behind the override. Built only from
// data already loaded into Matrix — the same Cell.Drifted/Entry.GetDrift()
// the dashboard rollup and the deployments-matrix screen read — so this
// table can never disagree with either about which (chart, env) pairs are
// drifted (FR-9 consistency). The headline *count* screen 40 shows is
// m.TotalDrift(), not len(DriftRows()), specifically so it is always the
// literal same number as the dashboard badge even in the (currently
// theoretical, single-app-per-chart-per-env in practice) case where one
// drifted cell carries more than one DriftEntry.
type DriftRow struct {
	EnvKey string

	ChartKey      string // "chart:<id>" — Row.Key convention, for linking
	ChartFullName string

	AppKey      string // "app:<id>" — Row.Key convention, for linking
	AppID       string
	AppFullName string

	// PromotedVersion/Since are resolved from the drifted app's own child
	// row/cell (its override promotion) when present; zero values when the
	// override's own row can't be found (e.g. composed-app resolution
	// skipped it — see Build's "Composed app not (yet) resolved" case).
	PromotedVersion string
	PromotedDigest  string
	Since           int64

	ChartPinnedDigest string
}

// DriftRows flattens every DriftEntry across every chart row's cells into
// one row per (env, drifted app) pair, sorted by environment then app name
// for a stable, scannable table.
func (m *Matrix) DriftRows() []DriftRow {
	var out []DriftRow
	for _, row := range m.Rows {
		if row.Kind != RowKindChart {
			continue
		}
		for i, cell := range row.Cells {
			if !cell.Drifted || cell.Entry == nil {
				continue
			}
			for _, d := range cell.Entry.GetDrift() {
				dr := DriftRow{
					EnvKey:            cell.EnvKey,
					ChartKey:          row.Key,
					ChartFullName:     row.FullName,
					AppKey:            "app:" + d.GetAppId(),
					AppID:             d.GetAppId(),
					AppFullName:       d.GetAppFullName(),
					PromotedDigest:    d.GetPromotedDigest(),
					ChartPinnedDigest: d.GetChartPinnedDigest(),
				}
				for _, child := range row.Children {
					if child.Key != dr.AppKey || i >= len(child.Cells) || child.Cells[i].Entry == nil {
						continue
					}
					dr.PromotedVersion = child.Cells[i].Entry.GetArtifact().GetVersion()
					dr.Since = child.Cells[i].Entry.GetPromotion().GetValidFrom()
					break
				}
				out = append(out, dr)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].EnvKey != out[j].EnvKey {
			return out[i].EnvKey < out[j].EnvKey
		}
		return out[i].AppFullName < out[j].AppFullName
	})
	return out
}
