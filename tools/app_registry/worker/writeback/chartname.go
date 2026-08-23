package writeback

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// findChartArtifact scans entries for the environment state's chart
// artifact (ArtifactKind_ARTIFACT_KIND_CHART), returning its targetRevision
// (Artifact.Version) and chart id. Both GitOpsActivities and StubActivities
// need this exact lookup -- see resolveChart's doc comment for why it's
// split out separately rather than folded into resolveChart itself.
func findChartArtifact(entries []*pb.EnvironmentStateEntry) (targetRevision, chartID string, found bool) {
	for _, entry := range entries {
		if entry.Artifact != nil && entry.Artifact.Kind == pb.ArtifactKind_ARTIFACT_KIND_CHART {
			return entry.Artifact.Version, entry.Artifact.ChartId, true
		}
	}
	return "", "", false
}

// resolveChart resolves chartID to its *pb.Chart via AppClient.ListCharts,
// matching GitOpsActivities' pre-existing behavior (whale-net/everything#1035,
// #1037): falls back to the sole chart in the domain when chartID is empty
// and there's exactly one. Returns an error -- never a nil Chart -- if
// AppClient is nil, ListCharts fails, or no chart matches, so callers never
// build a malformed "<chart_name>-<env>" ArgoCD Application name
// (workflow.go) with an empty chart-name segment.
//
// Both GitOpsActivities' and StubActivities' RenderEnvironmentState use the
// returned Chart for both RenderedState.ChartName (chart.FullName) and
// RenderedState.ArgoApplicationName (resolveArgoApplicationName below) --
// one ListCharts lookup, not two.
func resolveChart(ctx context.Context, appClient pb.AppRegistryClient, domain, chartID string) (*pb.Chart, error) {
	if appClient == nil {
		return nil, fmt.Errorf("resolve chart for domain %q: no AppClient configured", domain)
	}
	chartResp, err := appClient.ListCharts(ctx, &pb.ListChartsRequest{Domain: domain})
	if err != nil {
		return nil, fmt.Errorf("list charts: %w", err)
	}
	for _, c := range chartResp.Charts {
		if c.ChartId == chartID || (chartID == "" && len(chartResp.Charts) == 1) {
			return c, nil
		}
	}
	return nil, fmt.Errorf("chart %q not found in domain %q", chartID, domain)
}

// resolveArgoApplicationName is chart's ArgoCD-Application-name
// counterpart to FullName: chart.ArgoApplicationNameOverrides[environmentKey],
// if an explicit override is set for this exact environment (an ad-hoc/
// legacy deployment override, admin-settable via
// SetChartArgoApplicationNameOverride -- dev and prod need not share any
// naming pattern, each environment's override is independent); otherwise
// the convention "<FullName>-<environmentKey>" every standard deployment
// uses. Mirrors repository.Chart.ResolveArgoApplicationName's rule exactly,
// but operates on the *pb.Chart this package already has in hand via
// AppClient, not a repository.Chart (this package talks to the registry
// only over its API, never Postgres directly -- see ARCHITECTURE.md "The
// API is the write path; git is the delivery path").
func resolveArgoApplicationName(chart *pb.Chart, environmentKey string) string {
	if name := chart.GetArgoApplicationNameOverrides()[environmentKey]; name != "" {
		return name
	}
	return chart.GetFullName() + "-" + environmentKey
}
