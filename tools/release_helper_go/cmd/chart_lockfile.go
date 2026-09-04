package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/whale-net/everything/tools/helm"
)

// newReadChartLockfileCmd exposes the compose-time image lockfile
// tools/helm/composer.go writes into every generated chart (see
// helm.LockfileFileName / helm.ChartLockfile) to the release path, so AR-2c
// can resolve digests for it after images are pushed and call
// ArtifactRegistry.RecordArtifact without re-deriving what the chart pins.
//
// It does not talk to a registry and does not resolve digests — see
// //tools/app_registry/ARCHITECTURE.md's "Chart -> image lockfile" section
// for why that step is deliberately kept out of the hermetic Bazel action
// that composes the chart.
func newReadChartLockfileCmd() *cobra.Command {
	var skipBuild bool

	cmd := &cobra.Command{
		Use:          "read-chart-lockfile <chart-name>",
		Short:        "Print a built chart's compose-time image lockfile as JSON",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			chartName := args[0]
			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allCharts, err := ListAllHelmCharts(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}
			chart, err := findHelmChartByName(chartName, allCharts)
			if err != nil {
				return err
			}

			chartTarget, chartDir := chartOutputPaths(workspaceRoot, chart)

			if !skipBuild {
				fmt.Fprintf(cmd.ErrOrStderr(), "Building bazel target: %s\n", chartTarget)
				// --config=ci-images: the lockfile below is read straight off
				// bazel-bin/chartDir, same real-file-on-disk requirement as
				// build_app.go's image push (see .bazelrc's ci-images comment).
				if _, err := defaultBazel.Run("build", "--config=ci-images", chartTarget); err != nil {
					return fmt.Errorf("bazel build %s: %w", chartTarget, err)
				}
			}

			lockfilePath := filepath.Join(chartDir, helm.LockfileFileName)
			data, err := defaultFS.ReadFile(lockfilePath)
			if err != nil {
				return fmt.Errorf("read image lockfile at %s (build the chart first, or omit --skip-build): %w", lockfilePath, err)
			}

			fmt.Fprintln(cmd.OutOrStdout(), string(data))
			return nil
		},
	}

	cmd.Flags().BoolVar(&skipBuild, "skip-build", false, "Read the lockfile from a previously built chart without rebuilding it")
	return cmd
}
