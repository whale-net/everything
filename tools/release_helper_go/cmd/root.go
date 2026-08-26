package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// fastDiscovery gates ListAllApps/ListAllHelmCharts between the default
// bazel query/cquery discovery path and the --fast static-AST path (see
// discover_fast.go). A plain package var (rather than a parameter threaded
// through every ListAllApps/ListAllHelmCharts call site) keeps every
// existing caller's behavior identical when --fast isn't passed, and NewRootCmd
// re-registering the flag on every call resets it to false for each fresh
// command tree, so tests that build multiple root commands don't leak state.
var fastDiscovery bool

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
	c.PersistentFlags().BoolVar(&fastDiscovery, "fast", false,
		"Discover release_app/release_helm_chart targets by statically parsing BUILD.bazel files "+
			"instead of shelling out to bazel query/cquery. Requires every release_app/release_helm_chart "+
			"call site to pass literal arguments (no variables, concatenation, glob, or select) -- see "+
			"docs/RELEASE_HELPER_FAST_MODE.md.")
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
		newBuildAppCmd(),
		newBuildChartCmd(),
		newBuildReleaseCmd(),
		newFinalizeAppCmd(),
		newFinalizeChartCmd(),
		newCreateCombinedGithubReleaseCmd(),
		newManifestSetCmd(),
		newReadChartLockfileCmd(),
		newPackageAssetsCmd(),
	)
	return c
}

func Execute() {
	if err := NewRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
