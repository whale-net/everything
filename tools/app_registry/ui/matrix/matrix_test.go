package matrix

import (
	"errors"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// --- Promotability guardrail (FR-5, NFR-12) --------------------------------

// TestBuild_ViaChartAppNeverTopLevelRow proves a VIA_CHART app is never a
// top-level Row (and so never a positional sibling of a PROMOTABLE row) --
// it is reachable only via its owning chart's Row.Children.
func TestBuild_ViaChartAppNeverTopLevelRow(t *testing.T) {
	charts := []*pb.Chart{
		{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", AppIds: []string{"app-via-chart"}},
	}
	apps := []*pb.App{
		{AppId: "app-via-chart", Domain: "platform", FullName: "platform-web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
		{AppId: "app-standalone", Domain: "platform", FullName: "platform-worker", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}
	environments := []*pb.Environment{{EnvironmentId: "e1", Key: "dev", Rank: 0}}
	columns := map[string]ColumnResult{"dev": {Env: environments[0], Resp: &pb.GetEnvironmentStateResponse{}}}

	m := Build(charts, apps, environments, columns)

	if len(m.Rows) != 2 {
		t.Fatalf("expected exactly 2 top-level rows (chart + standalone image), got %d: %+v", len(m.Rows), m.Rows)
	}
	for _, row := range m.Rows {
		if row.Kind == RowKindViaChartApp {
			t.Fatalf("a VIA_CHART row must never be top-level, found: %+v", row)
		}
		if row.FullName == "platform-web-api" {
			t.Fatalf("the VIA_CHART app rendered as a top-level row/sibling, which FR-5/NFR-12 forbids: %+v", row)
		}
	}

	// It IS reachable as a child of its owning chart's row.
	var chartRow *Row
	for _, row := range m.Rows {
		if row.Kind == RowKindChart {
			chartRow = row
		}
	}
	if chartRow == nil {
		t.Fatal("expected a chart row")
	}
	if len(chartRow.Children) != 1 || chartRow.Children[0].FullName != "platform-web-api" {
		t.Fatalf("expected the VIA_CHART app reachable only via the chart's Children, got %+v", chartRow.Children)
	}
}

// --- Drift rollup (FR-9) ----------------------------------------------------

func TestBuild_DriftRollup_ChartRowAndTotalAndSpecificEntry(t *testing.T) {
	charts := []*pb.Chart{
		{ChartId: "chart-1", Domain: "platform", FullName: "platform-web", AppIds: []string{"app-1"}},
	}
	apps := []*pb.App{
		{AppId: "app-1", Domain: "platform", FullName: "platform-web-api", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
	}
	environments := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	columns := map[string]ColumnResult{
		"prod": {
			Env: environments[0],
			Resp: &pb.GetEnvironmentStateResponse{
				Entries: []*pb.EnvironmentStateEntry{
					{
						Artifact: &pb.Artifact{ChartId: "chart-1", Version: "v2.0.0"},
						Drift: []*pb.DriftEntry{
							{AppId: "app-1", AppFullName: "platform-web-api", ChartPinnedDigest: "sha256:pinned", PromotedDigest: "sha256:override"},
						},
					},
				},
			},
		},
	}

	m := Build(charts, apps, environments, columns)

	var chartRow *Row
	for _, row := range m.Rows {
		if row.Kind == RowKindChart {
			chartRow = row
		}
	}
	if chartRow == nil || !chartRow.AnyDrift {
		t.Fatalf("expected the chart row to be marked AnyDrift, got %+v", chartRow)
	}

	if got := m.TotalDrift(); got != 1 {
		t.Fatalf("TotalDrift() = %d, want 1", got)
	}

	driftRows := m.DriftRows()
	if len(driftRows) != 1 {
		t.Fatalf("DriftRows() len = %d, want 1: %+v", len(driftRows), driftRows)
	}
	dr := driftRows[0]
	if dr.AppFullName != "platform-web-api" || dr.AppID != "app-1" {
		t.Errorf("DriftRows() must identify the SPECIFIC drifted entry, got %+v", dr)
	}
	if dr.ChartFullName != "platform-web" {
		t.Errorf("expected the drift row to name its owning chart, got %+v", dr)
	}

	// FR-9 consistency: the dashboard/drift-audit headline count is the same
	// literal function call in both places -- this proves the count and the
	// per-entry count for a single-drift case agree.
	if m.TotalDrift() != len(driftRows) {
		t.Errorf("TotalDrift()=%d disagrees with len(DriftRows())=%d for a single-entry case", m.TotalDrift(), len(driftRows))
	}
}

// --- Empty/failure states (NFR-6) -------------------------------------------

func TestBuild_ColumnError_NeverRendersAsNotPromoted(t *testing.T) {
	charts := []*pb.Chart{{ChartId: "chart-1", Domain: "platform", FullName: "platform-web"}}
	environments := []*pb.Environment{{EnvironmentId: "e1", Key: "prod", Rank: 10}}
	columns := map[string]ColumnResult{
		"prod": {Env: environments[0], Err: errors.New("simulated GetEnvironmentState failure")},
	}

	m := Build(charts, nil, environments, columns)

	if len(m.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(m.Rows))
	}
	cell := m.Rows[0].Cells[0]
	if cell.ColumnErr == nil {
		t.Fatal("expected the cell to carry the column error explicitly")
	}
	if cell.Entry != nil {
		t.Fatal("a failed column must never carry a non-nil Entry")
	}
	// The distinguishing signal a template branches on: ColumnErr set is a
	// DIFFERENT state than Entry == nil && ColumnErr == nil ("legitimately
	// not promoted") -- prove they're distinguishable, not conflated.
	notPromotedButHealthy := &Cell{EnvKey: "prod"}
	if (cell.ColumnErr != nil) == (notPromotedButHealthy.ColumnErr != nil) {
		t.Fatal("an error cell and a legitimate not-promoted cell must be distinguishable via ColumnErr")
	}

	if len(m.ColumnErrors) != 1 {
		t.Fatalf("expected buildErr to also be summarized in Matrix.ColumnErrors, got %+v", m.ColumnErrors)
	}
}

func TestBuild_BinaryToolApp_StandaloneBinaryRow(t *testing.T) {
	apps := []*pb.App{
		{AppId: "cli-1", Domain: "tools", FullName: "tools-app-registry", AppType: "cli", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
		{AppId: "cli-2", Domain: "tools", FullName: "tools-release_helper_go", AppType: "cli", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
		{AppId: "none-app", Domain: "demo", FullName: "demo-build-only", AppType: "job", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
	}
	environments := []*pb.Environment{{EnvironmentId: "e1", Key: "dev", Rank: 0}}
	columns := map[string]ColumnResult{"dev": {Env: environments[0], Resp: &pb.GetEnvironmentStateResponse{}}}

	m := Build(nil, apps, environments, columns)

	if len(m.Rows) != 2 {
		t.Fatalf("expected 2 rows for binary tools (build-only job excluded), got %d: %+v", len(m.Rows), m.Rows)
	}
	for _, row := range m.Rows {
		if row.Kind != RowKindStandaloneBinary {
			t.Errorf("expected row %s to have Kind=RowKindStandaloneBinary, got %v", row.FullName, row.Kind)
		}
	}
}
