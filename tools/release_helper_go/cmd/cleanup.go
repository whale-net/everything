package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newCleanupReleasesCmd() *cobra.Command {
	var (
		keepMinorVersions int
		minAgeDays        int
		dryRun            bool
		deletePackages    bool
	)

	cmd := &cobra.Command{
		Use:          "cleanup-releases",
		Short:        "Clean up old Git tags and optionally their corresponding GHCR packages",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			token := defaultEnv("GITHUB_TOKEN")
			if token == "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: GITHUB_TOKEN environment variable not set\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "Please set GITHUB_TOKEN with appropriate permissions:\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "  - contents:write (for tag deletion)\n")
				fmt.Fprintf(cmd.ErrOrStderr(), "  - packages:write (for GHCR package deletion)\n")
				return fmt.Errorf("missing GITHUB_TOKEN")
			}

			_ = keepMinorVersions
			_ = minAgeDays
			_ = dryRun
			_ = deletePackages

			// TODO: implement full cleanup logic
			fmt.Fprintln(cmd.ErrOrStderr(), "cleanup-releases: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().IntVar(&keepMinorVersions, "keep-minor-versions", 2, "Number of recent minor versions to keep")
	cmd.Flags().IntVar(&minAgeDays, "min-age-days", 14, "Minimum age in days for deletion")
	cmd.Flags().BoolVar(&dryRun, "dry-run", true, "Preview changes without executing")
	cmd.Flags().BoolVar(&deletePackages, "delete-packages", true, "Also delete corresponding GHCR packages")

	return cmd
}
