package main

import (
	"context"
	"fmt"
	"sort"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/matrix"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// hideDemoDomain reports whether demoDomain rows should be dropped from the
// Apps Catalog (issue #750). A nil config (e.g. an App built directly in a
// test fixture without LoadConfig) is treated the same as the documented
// default — hidden — rather than panicking.
func (app *App) hideDemoDomain() bool {
	return app.config == nil || !app.config.ShowDemoDomain
}

// buildAppsCatalog loads the environments, charts, and apps screens 20
// (apps catalog) and 11 (app detail) share, and shapes the flat
// matrix.AppCatalogRow list. It reuses fetchEnvironmentColumns — the same
// primitive buildDeploymentMatrix uses — so the catalog's per-environment
// promotability can never disagree with the Deployments matrix.
func (app *App) buildAppsCatalog(ctx context.Context) ([]*pb.Environment, []matrix.AppCatalogRow, error) {
	envResp, err := app.registry.Environment.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		return nil, nil, fmt.Errorf("ListEnvironments: %w", err)
	}
	environments := envResp.GetEnvironments()

	chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
	if err != nil {
		return nil, nil, fmt.Errorf("ListCharts: %w", err)
	}
	appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		return nil, nil, fmt.Errorf("ListApps: %w", err)
	}

	columns := app.fetchEnvironmentColumns(ctx, environments, 0)

	rows := make([]matrix.AppCatalogRow, 0, len(chartsResp.GetCharts())+len(appsResp.GetApps()))
	for _, c := range chartsResp.GetCharts() {
		// demoDomain (release_scope.go) is hidden from the catalog by
		// default (issue #750) — same "demo" domain release.yml's
		// include_demo input and resolveReleaseScope's includeDemo param
		// exclude by default; ShowDemoDomain (APP_REGISTRY_UI_SHOW_DEMO_DOMAIN)
		// opts back in.
		if c.GetDomain() == demoDomain && app.hideDemoDomain() {
			continue
		}
		rows = append(rows, matrix.AppCatalogRow{
			IsChart:    true,
			ID:         c.GetChartId(),
			Domain:     c.GetDomain(),
			FullName:   c.GetFullName(),
			DeployUnit: c.GetDeployUnit(),
			Cells:      matrix.CellsForKey(environments, columns, "chart:"+c.GetChartId()),
		})
	}
	for _, a := range appsResp.GetApps() {
		if a.GetDomain() == demoDomain && app.hideDemoDomain() {
			continue
		}
		rows = append(rows, matrix.AppCatalogRow{
			IsChart:    false,
			ID:         a.GetAppId(),
			Domain:     a.GetDomain(),
			FullName:   a.GetFullName(),
			DeployUnit: a.GetDeployUnit(),
			Cells:      matrix.CellsForKey(environments, columns, "app:"+a.GetAppId()),
		})
	}
	sort.SliceStable(rows, func(i, j int) bool { return rows[i].FullName < rows[j].FullName })

	return environments, rows, nil
}

// findEntry returns the EnvironmentStateEntry in resp whose Promotion
// matches predicate, or nil.
func findEntry(resp *pb.GetEnvironmentStateResponse, match func(*pb.Promotion) bool) *pb.EnvironmentStateEntry {
	for _, e := range resp.GetEntries() {
		if match(e.GetPromotion()) {
			return e
		}
	}
	return nil
}

// buildAppDetail resolves everything screen 11 needs for full_name --
// GetApp, the environment fanout, and (owner_full_name-filtered) promotion
// events -- in one place so the handler stays a thin HTTP shim.
func (app *App) buildAppDetail(ctx context.Context, fullName string) (*matrix.AppDetailData, error) {
	getResp, err := app.registry.App.GetApp(ctx, &pb.GetAppRequest{FullName: fullName})
	if err != nil {
		return nil, fmt.Errorf("GetApp(%q): %w", fullName, err)
	}
	a := getResp.GetApp()

	var owningChart *pb.Chart
	if a.GetDeployUnit() == appmetapb.DeployUnit_DEPLOY_UNIT_CHART && len(getResp.GetCharts()) > 0 {
		owningChart = getResp.GetCharts()[0]
	}

	envResp, err := app.registry.Environment.ListEnvironments(ctx, &pb.ListEnvironmentsRequest{})
	if err != nil {
		return nil, fmt.Errorf("ListEnvironments: %w", err)
	}
	environments := envResp.GetEnvironments()
	columns := app.fetchEnvironmentColumns(ctx, environments, 0)

	states := make([]matrix.AppEnvState, 0, len(environments))
	for _, env := range environments {
		col := columns[env.GetKey()]
		s := matrix.AppEnvState{Env: env, ColumnErr: col.Err}
		if col.Err != nil || col.Resp == nil {
			states = append(states, s)
			continue
		}

		direct := findEntry(col.Resp, func(p *pb.Promotion) bool { return p.GetAppId() == a.GetAppId() })
		if direct != nil {
			s.Promoted = true
			s.Override = direct.GetPromotion().GetIsOverride()
			s.Artifact = direct.GetArtifact()
			s.Version = direct.GetArtifact().GetVersion()
			s.Digest = direct.GetArtifact().GetDigest()

			if owningChart != nil {
				chartEntry := findEntry(col.Resp, func(p *pb.Promotion) bool { return p.GetChartId() == owningChart.GetChartId() })
				for _, d := range chartEntry.GetDrift() {
					if d.GetAppId() == a.GetAppId() {
						s.Drifted = true
						s.ChartPinnedDigest = d.GetChartPinnedDigest()
						break
					}
				}
			}
		} else if owningChart != nil {
			chartEntry := findEntry(col.Resp, func(p *pb.Promotion) bool { return p.GetChartId() == owningChart.GetChartId() })
			for _, link := range chartEntry.GetImages() {
				if link.GetAppId() == a.GetAppId() {
					s.Promoted = true
					s.ViaChart = true
					s.Version = link.GetVersion()
					s.Digest = link.GetDigest()
					break
				}
			}
		}

		states = append(states, s)
	}

	events, eventsErr := app.registry.Promotion.ListPromotionEvents(ctx, &pb.ListPromotionEventsRequest{
		OwnerFullName: a.GetFullName(),
	})
	var eventList []*pb.PromotionEvent
	if eventsErr == nil {
		eventList = events.GetEvents()
	}

	return &matrix.AppDetailData{
		App:         a,
		OwningChart: owningChart,
		States:      states,
		Events:      eventList,
		EventsErr:   eventsErr,
	}, nil
}
