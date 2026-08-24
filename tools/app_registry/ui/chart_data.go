package main

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
	"github.com/whale-net/everything/tools/app_registry/ui/viewdata"
)

// buildChartDetail resolves screen 22's data (FR-24/FR-25): the chart's
// current version per environment, the apps published at that artifact's
// version (build-time artifact_link pins, read straight off
// Artifact.contains -- already resolved by tools/helm at publish time, no
// extra RPC needed), kept separately labelled and never merged (FR-24), the
// chart's currently DECLARED composition (chart.app_ids), and the per-
// environment ArgoCD Application name override editor's rows (ArgoOverrides
// -- admin-settable via SetChartArgoApplicationNameOverride, computed
// straight off Chart.argo_application_name_overrides plus each
// environment's default naming convention, no extra RPC). A nil, nil
// return means no chart with this full_name exists -- mirrors
// handleBuildDetail's "no build recorded" pattern (handlers_builds.go)
// rather than a synthetic error, since there is no GetChart-by-name RPC to
// fail on directly.
func (app *App) buildChartDetail(ctx context.Context, fullName string) (*viewdata.ChartDetailData, error) {
	chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListCharts: %w", err)
	}
	var chart *pb.Chart
	for _, c := range chartsResp.GetCharts() {
		if c.GetFullName() == fullName {
			chart = c
			break
		}
	}
	if chart == nil {
		return nil, nil
	}

	appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListApps: %w", err)
	}
	appByID := make(map[string]*pb.App, len(appsResp.GetApps()))
	for _, a := range appsResp.GetApps() {
		appByID[a.GetAppId()] = a
	}

	envResp, err := app.registry.Environment.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvironments: %w", err)
	}
	environments := envResp.GetEnvironments()
	columns := app.fetchEnvironmentColumns(ctx, environments, 0)

	key := "chart:" + chart.GetChartId()
	cells := matrix.CellsForKey(environments, columns, key)

	// Resolve provenance for every pinned image across every environment in
	// one fanout (FR-33/NFR-11: provenance must be tagged wherever an
	// artifact renders) -- ArtifactLink carries a digest/artifact_id but not
	// provenance itself.
	var pinnedArtifactIDs []string
	for _, cell := range cells {
		if cell.ColumnErr == nil && cell.Entry != nil {
			for _, link := range cell.Entry.GetArtifact().GetContains() {
				pinnedArtifactIDs = append(pinnedArtifactIDs, link.GetArtifactId())
			}
		}
	}
	lookups := app.fetchArtifactsByID(ctx, pinnedArtifactIDs)

	envPins := make([]viewdata.ChartEnvPins, 0, len(environments))
	for i, env := range environments {
		cell := cells[i]
		pin := viewdata.ChartEnvPins{Env: env, ColumnErr: cell.ColumnErr}
		if cell.ColumnErr == nil && cell.Entry != nil {
			pin.Promoted = true
			pin.Version = cell.Entry.GetArtifact().GetVersion()
			pin.PromotedAt = cell.Entry.GetPromotion().GetValidFrom()
			pin.Drift = cell.Entry.GetDrift()
			for _, link := range cell.Entry.GetArtifact().GetContains() {
				img := viewdata.PinnedImage{
					Version: link.GetVersion(),
					Digest:  link.GetDigest(),
				}
				if a, ok := appByID[link.GetAppId()]; ok {
					img.AppFullName = a.GetFullName()
				} else {
					img.AppFullName = link.GetAppId()
				}
				if lookup := lookups[link.GetArtifactId()]; lookup != nil {
					img.Provenance = lookup.GetArtifact().GetProvenance()
				}
				pin.Images = append(pin.Images, img)
			}
		}
		envPins = append(envPins, pin)
	}

	declaredApps := make([]*pb.App, 0, len(chart.GetAppIds()))
	for _, id := range chart.GetAppIds() {
		if a, ok := appByID[id]; ok {
			declaredApps = append(declaredApps, a)
		}
	}
	sort.Slice(declaredApps, func(i, j int) bool { return declaredApps[i].GetFullName() < declaredApps[j].GetFullName() })

	argoOverrides := make([]viewdata.ChartArgoOverrideRow, 0, len(environments))
	overrides := chart.GetArgoApplicationNameOverrides()
	for _, env := range environments {
		row := viewdata.ChartArgoOverrideRow{
			EnvKey:   env.GetKey(),
			Override: overrides[env.GetKey()],
			// Mirrors repository.Chart.ResolveArgoApplicationName's default
			// convention exactly -- see that function's doc comment.
			Default: chart.GetFullName() + "-" + env.GetKey(),
		}
		row.Resolved = row.Default
		if row.Override != "" {
			row.Resolved = row.Override
		}
		argoOverrides = append(argoOverrides, row)
	}

	return &viewdata.ChartDetailData{
		Chart:         chart,
		Environments:  environments,
		EnvPins:       envPins,
		DeclaredApps:  declaredApps,
		ArgoOverrides: argoOverrides,
	}, nil
}
