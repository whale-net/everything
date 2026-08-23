package writeback

import (
	"context"
	"fmt"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// findChartArtifact scans entries for the environment state's chart
// artifact (ArtifactKind_ARTIFACT_KIND_CHART), returning its targetRevision
// (Artifact.Version) and chart id. Both GitOpsActivities and StubActivities
// need this exact lookup -- see resolveChartName's doc comment for why it's
// split out separately rather than folded into resolveChartName itself.
func findChartArtifact(entries []*pb.EnvironmentStateEntry) (targetRevision, chartID string, found bool) {
	for _, entry := range entries {
		if entry.Artifact != nil && entry.Artifact.Kind == pb.ArtifactKind_ARTIFACT_KIND_CHART {
			return entry.Artifact.Version, entry.Artifact.ChartId, true
		}
	}
	return "", "", false
}

// resolveChartName resolves chartID to its full chart name via
// AppClient.ListCharts, matching GitOpsActivities' pre-existing behavior
// (whale-net/everything#1035, #1037): falls back to the sole chart in the
// domain when chartID is empty and there's exactly one. Returns an error --
// never a zero-value name -- if AppClient is nil, ListCharts fails, or no
// chart matches, so callers never build a malformed
// "<chart_name>-<env>" ArgoCD Application name (workflow.go) with an empty
// chart-name segment.
func resolveChartName(ctx context.Context, appClient pb.AppRegistryClient, domain, chartID string) (string, error) {
	if appClient == nil {
		return "", fmt.Errorf("resolve chart name for domain %q: no AppClient configured", domain)
	}
	chartResp, err := appClient.ListCharts(ctx, &pb.ListChartsRequest{Domain: domain})
	if err != nil {
		return "", fmt.Errorf("list charts: %w", err)
	}
	for _, c := range chartResp.Charts {
		if c.ChartId == chartID || (chartID == "" && len(chartResp.Charts) == 1) {
			return c.FullName, nil
		}
	}
	return "", fmt.Errorf("chart %q not found in domain %q", chartID, domain)
}
