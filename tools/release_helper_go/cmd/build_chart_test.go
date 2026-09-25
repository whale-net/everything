package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// TestExecuteBuildChartsFullNameNotDoubleDomainPrefixed is a regression test
// for a prod incident (App Registry release finalize failing with "no
// chart-sources manifest entry" for manmanv2-control-services): build_chart.go
// used to compute BuildChartResult.FullName as
// chart.Domain+"-"+strings.TrimPrefix(chart.Name, "helm-"), which only strips
// the leading "helm-" and not the "helm-{domain}-" prefix release.bzl's
// release_helm_chart macro actually composes chart.Name with -- double-
// counting the domain (e.g. "manmanv2-manmanv2-control-services") and never
// matching the plain "manmanv2-control-services" key finalize.go looks up
// from the resolved plan. FullName must instead come from
// HelmChartMetadata.FullName() (see plan_helm.go, fixed for the same bug in
// PR #1076 but never wired into this file).
func TestExecuteBuildChartsFullNameNotDoubleDomainPrefixed(t *testing.T) {
	charts := []fakeHelmChart{
		{pkg: "manmanv2", targetName: "manmanv2_chart_chart_metadata", name: "helm-manmanv2-control-services", domain: "manmanv2", apps: []string{"control-api"}},
	}
	_, bazel := buildFakeHelmInfra(charts)

	workspaceRoot := t.TempDir()
	// chartOutputPaths derives this exact path from BazelTarget/Name; create
	// it with a dummy file so copyDir has something real to copy.
	srcChartDir := filepath.Join(workspaceRoot, "bazel-bin", "manmanv2", "helm-manmanv2-control-services_chart", "manmanv2-control-services")
	if err := os.MkdirAll(srcChartDir, 0755); err != nil {
		t.Fatalf("mkdir src chart dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(srcChartDir, "Chart.yaml"), []byte("name: control-services\n"), 0644); err != nil {
		t.Fatalf("write Chart.yaml: %v", err)
	}

	bazel.calls = append(bazel.calls, fakeBazelCall{
		argsContain: []string{"build", "//manmanv2:chart"},
		output:      "",
	})

	outputDir := t.TempDir()
	var progressStates []string
	var progressCharts []string
	results, err := ExecuteBuildCharts(BuildChartParams{
		Charts:        "manmanv2-control-services",
		OutputDir:     outputDir,
		Bazel:         bazel,
		FS:            newFakeFS(),
		WorkspaceRoot: workspaceRoot,
		OnProgress: func(state string, chart HelmChartMetadata) {
			progressStates = append(progressStates, state)
			progressCharts = append(progressCharts, chart.FullName())
		},
	})
	if err != nil {
		t.Fatalf("ExecuteBuildCharts: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %d entries, want 1: %+v", len(results), results)
	}
	if got, want := results[0].FullName, "manmanv2-control-services"; got != want {
		t.Errorf("FullName = %q, want %q", got, want)
	}
	// A chart is reported "building" with its own full name, so a chart
	// batch marks only the in-flight chart rather than the whole batch.
	if len(progressStates) != 1 || progressStates[0] != "building" {
		t.Errorf("progress states = %v, want [building]", progressStates)
	}
	if len(progressCharts) != 1 || progressCharts[0] != "manmanv2-control-services" {
		t.Errorf("progress charts = %v, want [manmanv2-control-services]", progressCharts)
	}
}
