package main

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/viewdata"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// buildAppVersionHistory resolves screen 13's data: every artifact
// ListArtifacts has recorded for the app (FR-21), independent of what's
// currently promoted anywhere, plus a best-effort "currently live in"
// cross-reference against the same GetEnvironmentState fanout the
// deployments matrix and app detail use (deployments_data.go's
// fetchEnvironmentColumns) — so this screen can never disagree with those
// about what's live. provenanceFilter/liveOnly are the FR-21 wireframe's own
// filters, applied server-side (this UI has no client-side filtering JS,
// NFR-20): provenance is pushed into the ListArtifacts RPC itself;
// liveOnly is applied after the live cross-reference is computed.
func (app *App) buildAppVersionHistory(ctx context.Context, fullName, provenanceFilter string, liveOnly bool) (*viewdata.AppHistoryData, error) {
	getResp, err := app.registry.App.GetApp(ctx, &pb.GetAppRequest{FullName: fullName})
	if err != nil {
		return nil, fmt.Errorf("GetApp(%q): %w", fullName, err)
	}
	a := getResp.GetApp()

	listReq := &pb.ListArtifactsRequest{
		OwnerFullName: fullName,
		Kind:          pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
	}
	switch provenanceFilter {
	case "observed":
		listReq.Provenance = pb.ArtifactProvenance_ARTIFACT_PROVENANCE_OBSERVED
	case "adopted":
		listReq.Provenance = pb.ArtifactProvenance_ARTIFACT_PROVENANCE_ADOPTED
	}
	artifactsResp, err := app.registry.Artifact.ListArtifacts(ctx, listReq)
	if err != nil {
		return nil, fmt.Errorf("ListArtifacts(%q): %w", fullName, err)
	}

	// owningChart mirrors buildAppDetail's own resolution (apps_data.go) --
	// only meaningful when this app rides a chart's pin rather than being
	// directly promotable.
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

	liveByDigest := make(map[string][]viewdata.LiveEnvironment)
	for _, env := range environments {
		col := columns[env.GetKey()]
		if col.Err != nil || col.Resp == nil {
			continue
		}
		if direct := findEntry(col.Resp, func(p *pb.Promotion) bool { return p.GetAppId() == a.GetAppId() }); direct != nil {
			digest := direct.GetArtifact().GetDigest()
			liveByDigest[digest] = append(liveByDigest[digest], viewdata.LiveEnvironment{
				EnvKey:   env.GetKey(),
				Override: direct.GetPromotion().GetIsOverride(),
			})
			continue
		}
		if owningChart != nil {
			chartEntry := findEntry(col.Resp, func(p *pb.Promotion) bool { return p.GetChartId() == owningChart.GetChartId() })
			for _, link := range chartEntry.GetImages() {
				if link.GetAppId() == a.GetAppId() {
					liveByDigest[link.GetDigest()] = append(liveByDigest[link.GetDigest()], viewdata.LiveEnvironment{
						EnvKey: env.GetKey(),
					})
				}
			}
		}
	}

	buildIDs := make([]string, 0, len(artifactsResp.GetArtifacts()))
	for _, art := range artifactsResp.GetArtifacts() {
		buildIDs = append(buildIDs, art.GetArtifactId())
	}
	lookups := app.fetchArtifactsByID(ctx, buildIDs)

	rows := make([]viewdata.AppHistoryArtifact, 0, len(artifactsResp.GetArtifacts()))
	for _, art := range artifactsResp.GetArtifacts() {
		liveIn := liveByDigest[art.GetDigest()]
		if liveOnly && len(liveIn) == 0 {
			continue
		}
		row := viewdata.AppHistoryArtifact{Artifact: art, LiveIn: liveIn}
		if lookup := lookups[art.GetArtifactId()]; lookup != nil {
			row.Build = lookup.GetBuild()
		}
		rows = append(rows, row)
	}

	return &viewdata.AppHistoryData{
		App:              a,
		Artifacts:        rows,
		ProvenanceFilter: provenanceFilter,
		LiveOnly:         liveOnly,
	}, nil
}
