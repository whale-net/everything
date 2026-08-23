package cmd

import (
	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// newChartCmd is the admin-only chart management surface. Role: admin, per
// ARCHITECTURE.md "Authorization". Chart identity/status is fully
// reconciled from Bazel manifests (ReconcileApps) -- the only field an
// admin can write directly is the ArgoCD Application name override, for
// ad-hoc/legacy deployments that don't follow the
// "<domain>-<name>-<environment>" naming convention.
func newChartCmd() *cobra.Command {
	chartCmd := &cobra.Command{
		Use:   "chart",
		Short: "Manage chart admin overrides (admin)",
	}
	chartCmd.AddCommand(newChartSetArgoOverrideCmd())
	return chartCmd
}

func newChartSetArgoOverrideCmd() *cobra.Command {
	var fullName, template, reason string
	c := &cobra.Command{
		Use:   "set-argo-override",
		Short: "Set or clear a chart's ArgoCD Application name override",
		Long: "Overrides the ArgoCD Application name WritebackWorkflow targets for this chart's " +
			"promotions, for ad-hoc/legacy deployments that don't follow the " +
			"\"<domain>-<name>-<environment>\" convention. \"{environment}\" in --template is " +
			"replaced with the target environment key. Pass --template \"\" to clear an override " +
			"and return to the convention.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.SetChartArgoApplicationNameOverride(cmd.Context(), &pb.SetChartArgoApplicationNameOverrideRequest{
					FullName:                    fullName,
					ArgoApplicationNameTemplate: template,
					Reason:                      reason,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&fullName, "chart", "", "Chart full name (<domain>-<name>)")
	c.Flags().StringVar(&template, "template", "", "ArgoCD Application name template (\"{environment}\" substituted); empty clears the override")
	c.Flags().StringVar(&reason, "reason", "", "Reason, recorded in the audit log")
	_ = c.MarkFlagRequired("chart")
	_ = c.MarkFlagRequired("reason")
	return c
}
