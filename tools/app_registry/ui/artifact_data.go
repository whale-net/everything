package main

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/ui/viewdata"
)

// buildArtifactDetail resolves screen 21's data (FR-23): one artifact by
// digest, its build provenance, and -- for an IMAGE artifact -- the chart
// artifacts that pin it (ListArtifactPins, deliberately unpaginated per its
// own doc comment). A nil, nil return means no artifact with this digest is
// recorded; the caller renders 404, matching handleBuildDetail's
// no-build-recorded pattern rather than surfacing GetArtifact's NotFound as
// a generic transport failure.
func (app *App) buildArtifactDetail(ctx context.Context, digest string) (*viewdata.ArtifactDetailData, error) {
	resp, err := app.registry.Artifact.GetArtifact(ctx, &pb.GetArtifactRequest{Digest: digest})
	if err != nil {
		return nil, fmt.Errorf("GetArtifact(%q): %w", digest, err)
	}
	artifact := resp.GetArtifact()
	if artifact.GetArtifactId() == "" {
		return nil, nil
	}

	data := &viewdata.ArtifactDetailData{Artifact: artifact, Build: resp.GetBuild()}

	switch artifact.GetKind() {
	case pb.ArtifactKind_ARTIFACT_KIND_IMAGE:
		if appResp, err := app.registry.App.GetApp(ctx, &pb.GetAppRequest{AppId: artifact.GetAppId()}); err == nil {
			data.OwnerFullName = appResp.GetApp().GetFullName()
		}

		pinsResp, err := app.registry.Artifact.ListArtifactPins(ctx, &pb.ListArtifactPinsRequest{Digest: digest})
		if err != nil {
			// FR-23: an unpinned image is an explicit empty state, never an
			// error -- but a failed ListArtifactPins CALL is a real error,
			// kept separate from PinnedBy so the template can distinguish
			// "nothing pins this" from "couldn't find out".
			data.PinnedByErr = fmt.Errorf("ListArtifactPins(%q): %w", digest, err)
			break
		}
		chartsResp, chartsErr := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{})
		chartNameByID := make(map[string]string)
		if chartsErr == nil {
			for _, c := range chartsResp.GetCharts() {
				chartNameByID[c.GetChartId()] = c.GetFullName()
			}
		}
		for _, ca := range pinsResp.GetChartArtifacts() {
			name := chartNameByID[ca.GetChartId()]
			if name == "" {
				name = ca.GetChartId()
			}
			data.PinnedBy = append(data.PinnedBy, viewdata.PinnedByRow{
				ChartFullName: name,
				ChartVersion:  ca.GetVersion(),
			})
		}

	case pb.ArtifactKind_ARTIFACT_KIND_BINARY, pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE:
		if appResp, err := app.registry.App.GetApp(ctx, &pb.GetAppRequest{AppId: artifact.GetAppId()}); err == nil {
			data.OwnerFullName = appResp.GetApp().GetFullName()
		}

	case pb.ArtifactKind_ARTIFACT_KIND_CHART:
		// The wireframe note: for a chart-kind artifact this same layout
		// instead shows "Pins (contains)" -- Artifact.contains resolved to
		// display-ready rows, the same shape chart detail's EnvPins.Images
		// uses (chart_data.go).
		if chartsResp, err := app.registry.App.ListCharts(ctx, &pb.ListChartsRequest{}); err == nil {
			for _, c := range chartsResp.GetCharts() {
				if c.GetChartId() == artifact.GetChartId() {
					data.OwnerFullName = c.GetFullName()
					break
				}
			}
		}

		appByID := make(map[string]*pb.App)
		if appsResp, err := app.registry.App.ListApps(ctx, &pb.ListAppsRequest{}); err == nil {
			for _, a := range appsResp.GetApps() {
				appByID[a.GetAppId()] = a
			}
		}

		links := artifact.GetContains()
		ids := make([]string, 0, len(links))
		for _, link := range links {
			ids = append(ids, link.GetArtifactId())
		}
		lookups := app.fetchArtifactsByID(ctx, ids)

		for _, link := range links {
			img := viewdata.PinnedImage{Version: link.GetVersion(), Digest: link.GetDigest()}
			if a, ok := appByID[link.GetAppId()]; ok {
				img.AppFullName = a.GetFullName()
			} else {
				img.AppFullName = link.GetAppId()
			}
			if lookup := lookups[link.GetArtifactId()]; lookup != nil {
				img.Provenance = lookup.GetArtifact().GetProvenance()
			}
			data.Contains = append(data.Contains, img)
		}
	}

	return data, nil
}
