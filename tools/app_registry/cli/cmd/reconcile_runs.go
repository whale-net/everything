package cmd

import (
	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// newReconcileRunsCmd is the operator-facing browser over the
// `reconcile_run` table (migration 010, AR-8, issue #607) -- see
// ARCHITECTURE.md "ListReconcileRuns" and OPERATIONS.md's "browsing
// reconcile history" note.
func newReconcileRunsCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "reconcile-runs",
		Short: "Browse recorded reconcile sweeps",
	}
	c.AddCommand(newReconcileRunsListCmd())
	return c
}

func newReconcileRunsListCmd() *cobra.Command {
	var since int64
	var pageSize int32
	var pageToken string
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent reconcile sweeps, most recent first",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.ListReconcileRuns(cmd.Context(), &pb.ListReconcileRunsRequest{
					Since: since,
					Page:  &pb.PageRequest{PageSize: pageSize, PageToken: pageToken},
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().Int64Var(&since, "since", 0, "Only show runs applied at or after this Unix timestamp")
	c.Flags().Int32Var(&pageSize, "page-size", 0, "Max rows to return (0 = server default)")
	c.Flags().StringVar(&pageToken, "page-token", "", "Resume from a previous response's next_page_token")
	return c
}
