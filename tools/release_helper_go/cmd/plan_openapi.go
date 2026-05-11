package cmd

import (
	"encoding/json"
	"fmt"
	"strings"

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

			workspaceRoot, err := defaultWorkspaceRoot()
			if err != nil {
				return fmt.Errorf("workspace root: %w", err)
			}

			allApps, err := ListAllApps(defaultBazel, defaultFS, workspaceRoot)
			if err != nil {
				return err
			}

			requested := parseAppList(apps)
			resolved, err := resolveApps(requested, allApps)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "Error validating apps: %v\n", err)
				return err
			}

			type specEntry struct {
				App          string `json:"app"`
				Domain       string `json:"domain"`
				OpenAPITarget string `json:"openapi_target"`
			}
			var appsWithSpecs []specEntry
			for _, a := range resolved {
				if a.OpenAPISpecTarget != "" {
					appsWithSpecs = append(appsWithSpecs, specEntry{
						App:          a.Name,
						Domain:       a.Domain,
						OpenAPITarget: a.OpenAPISpecTarget,
					})
				}
			}

			if format == "github" {
				if len(appsWithSpecs) > 0 {
					matrix := map[string]interface{}{"include": appsWithSpecs}
					matrixJSON, _ := json.Marshal(matrix)
					fmt.Fprintf(cmd.OutOrStdout(), "matrix=%s\n", matrixJSON)
					names := make([]string, len(appsWithSpecs))
					for i, a := range appsWithSpecs {
						names[i] = a.Domain + "-" + a.App
					}
					fmt.Fprintf(cmd.OutOrStdout(), "apps=%s\n", strings.Join(names, " "))
				} else {
					fmt.Fprintln(cmd.OutOrStdout(), "matrix={}")
					fmt.Fprintln(cmd.OutOrStdout(), "apps=")
				}
				return nil
			}

			result := map[string]interface{}{
				"apps_with_specs": appsWithSpecs,
				"count":           len(appsWithSpecs),
			}
			if appsWithSpecs == nil {
				result["apps_with_specs"] = []specEntry{}
			}
			out, _ := json.MarshalIndent(result, "", "  ")
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		},
	}

	cmd.Flags().StringVar(&apps, "apps", "", "Space or comma-separated list of apps to check for OpenAPI specs")
	cmd.Flags().StringVar(&format, "format", "github", "Output format (json or github)")

	return cmd
}

func parseAppList(apps string) []string {
	if strings.TrimSpace(apps) == "" {
		return nil
	}
	sep := ","
	if !strings.Contains(apps, ",") {
		sep = " "
	}
	var result []string
	for _, s := range strings.Split(apps, sep) {
		if s = strings.TrimSpace(s); s != "" {
			result = append(result, s)
		}
	}
	return result
}
