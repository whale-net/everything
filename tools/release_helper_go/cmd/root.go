package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:          "release_helper",
	Short:        "Release helper for Everything monorepo",
	Long:         "Release helper for Everything monorepo — plan, build, and publish app releases.",
	SilenceUsage: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.SilenceErrors = true
	rootCmd.AddCommand(
		newPlanCmd(),
		newPlanOpenapiBuildsCmd(),
		newSummaryCmd(),
		newReleaseNotesCmd(),
		newReleaseNotesAllCmd(),
		newPlanHelmReleaseCmd(),
		newBuildHelmChartCmd(),
		newCleanupReleasesCmd(),
		newUnpublishHelmChartCmd(),
		newListAppsCmd(),
		newListCmd(),
		newChangesCmd(),
		newBuildCmd(),
		newReleaseMultiarchCmd(),
		newCreateCombinedGithubReleaseCmd(),
	)
}
