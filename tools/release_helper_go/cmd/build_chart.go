package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// BuildChartManifestFileName is the name of the JSON index written at the
// root of build-chart's --output-dir, listing every chart it built (issue
// #928). finalize.go reads this to correlate a resolved plan's chart
// target keys (owner_full_name, the *published* chart name -- e.g.
// "demo-hello-fastapi", not the raw "helm-..." Bazel chart name) back to
// the extracted chart-sources directory finalize-chart should package.
const BuildChartManifestFileName = "manifest.json"

// BuildChartParams configures ExecuteBuildCharts.
type BuildChartParams struct {
	Ctx           context.Context
	Charts        string
	IncludeDemo   bool
	OutputDir     string
	Bazel         BazelRunner
	FS            FileSystem
	WorkspaceRoot string
}

// BuildChartResult is one chart's build-chart outcome.
type BuildChartResult struct {
	// ChartName is the raw Bazel/HelmChartMetadata name (e.g.
	// "helm-demo") -- what chartOutputPaths and the on-disk directory are
	// keyed by.
	ChartName string `json:"chart_name"`
	Domain    string `json:"domain"`
	// FullName is the *published* full name (domain + "-" +
	// strings.TrimPrefix(ChartName, "helm-")) -- the exact
	// OwnerFullName/target-key format a resolved plan's chart entries use
	// (see release_charts.go's ExecuteReleaseCharts, which calls
	// ExecuteRelease with OwnerFullName: publishedName). finalize.go
	// correlates ResolvedPlan.Versions' chart keys against this field, not
	// ChartName.
	FullName string `json:"full_name"`
	// ChartDir is where the chart's built source tree (Chart.yaml,
	// values.yaml, templates/, image-lockfile.json) was copied to, relative
	// to the manifest file's own directory -- stable across the
	// upload-artifact/download-artifact round trip, unlike an absolute path
	// baked in by the GHA runner. finalize.go resolves it against wherever
	// it extracted the "chart-sources" artifact.
	ChartDir string `json:"chart_dir"`
}

func newBuildChartCmd() *cobra.Command {
	var (
		charts      string
		includeDemo bool
		outputDir   string
	)

	cmd := &cobra.Command{
		Use:   "build-chart",
		Short: "Build (compose) Helm chart source trees without packaging, versioning, or uploading (issue #928)",
		Long: "build-chart runs each selected chart's Bazel build and copies the resulting chart " +
			"source tree out for later packaging -- it does not run `helm package`, does not " +
			"resolve a final chart version, and does not upload to ChartMuseum. Those steps -- " +
			"which require the batch's resolved plan and safety-sensitive App Registry " +
			"interaction -- move to Temporal's finalize-chart (see " +
			"tools/app_registry/worker/release/finalize.go), which re-packages this directory " +
			"with the resolved final version and app versions once they are known.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			results, err := ExecuteBuildCharts(BuildChartParams{
				Ctx:           cmd.Context(),
				Charts:        charts,
				IncludeDemo:   includeDemo,
				OutputDir:     outputDir,
				Bazel:         defaultBazel,
				FS:            defaultFS,
				WorkspaceRoot: workspaceRoot,
			})
			if err != nil {
				return err
			}
			for _, r := range results {
				fmt.Fprintf(cmd.OutOrStdout(), "Built chart source tree for %s at %s\n", r.ChartName, r.ChartDir)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&charts, "charts", "", "Comma- or space-separated list of charts, domain names, or 'all'")
	cmd.Flags().BoolVar(&includeDemo, "include-demo", false, "Include demo domain charts when using 'all'")
	cmd.Flags().StringVar(&outputDir, "output-dir", "/tmp/build-manifest/charts", "Directory to copy each chart's built source tree into (one subdirectory per chart, named by chart.Name)")

	_ = cmd.MarkFlagRequired("charts")

	return cmd
}

// ExecuteBuildCharts builds every selected chart's Bazel target and copies
// the resulting chart source directory into p.OutputDir/<chart.Name>/, ready
// to ship as a single workflow artifact and be re-packaged later by
// finalize-chart with the batch's resolved version/app-versions.
func ExecuteBuildCharts(p BuildChartParams) ([]BuildChartResult, error) {
	bazel := p.Bazel
	if bazel == nil {
		bazel = defaultBazel
	}
	fs := p.FS
	if fs == nil {
		fs = defaultFS
	}
	workspaceRoot := p.WorkspaceRoot
	if workspaceRoot == "" {
		var err error
		workspaceRoot, err = defaultWorkspaceRoot()
		if err != nil {
			return nil, fmt.Errorf("workspace root: %w", err)
		}
	}

	allCharts, err := ListAllHelmCharts(bazel, fs, workspaceRoot)
	if err != nil {
		return nil, fmt.Errorf("list all helm charts: %w", err)
	}
	selected := filterHelmCharts(p.Charts, allCharts, p.IncludeDemo)
	if len(selected) == 0 {
		if p.Charts != "" && strings.ToLower(strings.TrimSpace(p.Charts)) != "all" {
			return nil, fmt.Errorf("no helm charts matched %q", p.Charts)
		}
		return nil, nil
	}

	outputDir := p.OutputDir
	if outputDir == "" {
		outputDir = "/tmp/build-manifest/charts"
	}

	results := make([]BuildChartResult, 0, len(selected))
	for _, chart := range selected {
		chartTarget, chartDir := chartOutputPaths(workspaceRoot, chart)
		fmt.Printf("Building bazel target: %s\n", chartTarget)
		if _, err := bazel.Run("build", chartTarget); err != nil {
			return nil, fmt.Errorf("bazel build %s: %w", chartTarget, err)
		}

		relDir := chart.Name
		dest := filepath.Join(outputDir, relDir)
		if err := os.RemoveAll(dest); err != nil {
			return nil, fmt.Errorf("clear destination %s: %w", dest, err)
		}
		if err := copyDir(chartDir, dest); err != nil {
			return nil, fmt.Errorf("copy chart dir %s -> %s: %w", chartDir, dest, err)
		}

		publishedName := strings.TrimPrefix(chart.Name, "helm-")
		results = append(results, BuildChartResult{
			ChartName: chart.Name,
			Domain:    chart.Domain,
			FullName:  chart.Domain + "-" + publishedName,
			ChartDir:  relDir,
		})
	}

	if err := os.MkdirAll(outputDir, 0755); err != nil {
		return nil, fmt.Errorf("create output dir %s: %w", outputDir, err)
	}
	manifestData, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal build-chart manifest: %w", err)
	}
	manifestPath := filepath.Join(outputDir, BuildChartManifestFileName)
	if err := os.WriteFile(manifestPath, manifestData, 0644); err != nil {
		return nil, fmt.Errorf("write build-chart manifest %s: %w", manifestPath, err)
	}

	return results, nil
}
