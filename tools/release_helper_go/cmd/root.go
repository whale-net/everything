package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// NewRootCmd creates a fresh root command tree for release_helper_go.
// Returning a fresh instance per call ensures tests and parsers do not share
// closed-over state or mutated flag variables.
func NewRootCmd() *cobra.Command {
	c := &cobra.Command{
		Use:           "release_helper",
		Short:         "Release helper for Everything monorepo",
		Long:          "Release helper for Everything monorepo — plan, build, and publish app releases.",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	c.AddCommand(
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
		newReleaseAppCmd(),
		newReleaseChartsCmd(),
		newCreateCombinedGithubReleaseCmd(),
		newManifestSetCmd(),
		newReadChartLockfileCmd(),
	)
	return c
}

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
