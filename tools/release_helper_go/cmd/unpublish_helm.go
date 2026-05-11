package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

func newUnpublishHelmChartCmd() *cobra.Command {
	var (
		chartName string
		versions  string
	)

	cmd := &cobra.Command{
		Use:          "unpublish-helm-chart <index-file>",
		Short:        "Remove specific versions of a chart from the Helm repository index",
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			indexFile := args[0]

			if _, err := defaultFS.Stat(indexFile); os.IsNotExist(err) {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: Index file not found: %s\n", indexFile)
				return fmt.Errorf("index file not found")
			}

			_ = chartName
			_ = versions

			// TODO: implement full unpublish logic
			fmt.Fprintln(cmd.ErrOrStderr(), "unpublish-helm-chart: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().StringVar(&chartName, "chart", "", "Name of the chart to unpublish versions from")
	cmd.Flags().StringVar(&versions, "versions", "", "Comma-separated list of versions to unpublish")

	return cmd
}
