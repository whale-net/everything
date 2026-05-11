package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func newPlanOpenapiBuildsCmd() *cobra.Command {
	var (
		apps   string
		format string
	)

	cmd := &cobra.Command{
		Use:          "plan-openapi-builds",
		Short:        "Plan OpenAPI spec builds for apps that have fastapi_app configured",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "json" && format != "github" {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error: format must be one of: json, github\n")
				return fmt.Errorf("invalid format")
			}

			_ = apps

			// TODO: implement full logic
			fmt.Fprintln(cmd.ErrOrStderr(), "plan-openapi-builds: not yet fully implemented")
			return fmt.Errorf("not implemented")
		},
	}

	cmd.Flags().StringVar(&apps, "apps", "", "Space or comma-separated list of apps to check for OpenAPI specs")
	cmd.Flags().StringVar(&format, "format", "github", "Output format (json or github)")

	return cmd
}
