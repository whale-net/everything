package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newAppsRecordBuildLogCmd is FR8's write-side call site skeleton (issue
// #923, Scaffold phase): ci.yml's reconcile-app-registry job is meant to
// call this once per discovered app/chart, on every push, unconditionally
// -- see server/repository.AppBuildLogRepository.RecordBuildLog's doc
// comment for why this is deliberately NOT content-gated the way `apps
// reconcile` is.
//
// STUB (Scaffold only): there is no RPC yet exposing
// AppBuildLogRepository.RecordBuildLog over gRPC, so this command cannot
// do anything real yet. Adding that RPC, wiring this command to call it,
// and wiring ci.yml's reconcile-app-registry job to invoke this command
// after its existing reconcile step (see
// .github/actions/app-registry-reconcile/action.yml) are all
// Implementation-phase work for issue #923's FR8. This command exists now
// so the CLI's flag/command-tree shape is settled before Implementation
// fills in the RPC call -- Hidden so it doesn't appear in `--help` output
// promising functionality that isn't there yet.
func newAppsRecordBuildLogCmd() *cobra.Command {
	var owner, kind, gitSHA, buildID string
	c := &cobra.Command{
		Use:    "record-build-log",
		Short:  "Record one app_build_log row for an app or chart (FR8, issue #923)",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("record-build-log: not implemented yet -- see issue #923's Implementation phase (no RecordBuildLog RPC exists yet)")
		},
	}
	c.Flags().StringVar(&owner, "owner", "", "Owner full name (domain-name)")
	c.Flags().StringVar(&kind, "kind", "", "image or chart")
	c.Flags().StringVar(&gitSHA, "git-sha", "", "Commit SHA this build was produced from")
	c.Flags().StringVar(&buildID, "build-id", "", "build_id returned by RecordBuild")
	return c
}
