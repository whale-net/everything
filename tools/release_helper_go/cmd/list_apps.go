package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
)

func newListAppsCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:          "list-apps",
		Short:        "List all apps with release metadata",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListApps(cmd, format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format (text or json)")
	return cmd
}

func newListCmd() *cobra.Command {
	var format string

	cmd := &cobra.Command{
		Use:          "list",
		Short:        "Alias for list-apps. List all apps with release metadata",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runListApps(cmd, format)
		},
	}
	cmd.Flags().StringVar(&format, "format", "text", "Output format (text or json)")
	return cmd
}

func runListApps(cmd *cobra.Command, format string) error {
	if format != "text" && format != "json" {
		fmt.Fprintf(cmd.ErrOrStderr(), "Warning: unknown format %q, defaulting to text\n", format)
		format = "text"
	}
	workspaceRoot, err := defaultWorkspaceRoot()
	if err != nil {
		return fmt.Errorf("workspace root: %w", err)
	}
	apps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
	if err != nil {
		return err
	}
	if format == "json" {
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(apps)
	}
	for _, app := range apps {
		fmt.Fprintf(cmd.OutOrStdout(), "%s (domain: %s, target: %s)\n", app.Name, app.Domain, app.BazelTarget)
	}
	return nil
}
