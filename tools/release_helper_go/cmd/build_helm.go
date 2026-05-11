package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newBuildHelmChartCmd() *cobra.Command {
	var (
		chartVersion        string
		outputDir           string
		useReleasedVersions bool
		autoVersion         bool
		bumpType            string
	)

	cmd := &cobra.Command{
		Use:          "build-helm-chart <chart-name>",
		Short:        "Build and package a helm chart",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if bumpType != "major" && bumpType != "minor" && bumpType != "patch" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: --bump must be one of: major, minor, patch\n")
				return fmt.Errorf("invalid bump type")
			}

			_ = args[0]
			_ = chartVersion
			_ = outputDir
			_ = useReleasedVersions
			_ = autoVersion

			// TODO: implement full helm chart building
			fmt.Fprintln(cmd.ErrOrStderr(), "build-helm-chart: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().StringVar(&chartVersion, "version", "", "Explicit chart version")
	cmd.Flags().StringVar(&outputDir, "output-dir", "", "Output directory for packaged chart")
	cmd.Flags().BoolVar(&useReleasedVersions, "use-released", true, "Use released app versions or 'latest'")
	cmd.Flags().BoolVar(&autoVersion, "auto-version", true, "Automatically determine chart version from git tags")
	cmd.Flags().StringVar(&bumpType, "bump", "patch", "Version bump type: major, minor, or patch")

	return cmd
}
