package cmd

import (
	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// newEnvCmd is the admin-only environment management surface. Role: admin,
// per ARCHITECTURE.md "Authorization".
func newEnvCmd() *cobra.Command {
	envCmd := &cobra.Command{
		Use:   "env",
		Short: "Manage environments (admin)",
	}
	envCmd.AddCommand(
		newEnvListCmd(),
		newEnvUpsertCmd(),
		newEnvArchiveCmd(),
	)
	return envCmd
}

func newEnvListCmd() *cobra.Command {
	var includeArchived bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List environments, ordered by rank",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Environment.ListEnvironments(cmd.Context(), &pb.ListEnvironmentsRequest{
					IncludeArchived: includeArchived,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().BoolVar(&includeArchived, "include-archived", false, "Include archived environments")
	return c
}

func newEnvUpsertCmd() *cobra.Command {
	var displayName, gitopsPath string
	var rank int32
	var requiresApproval bool
	var allowedPrincipals []string
	c := &cobra.Command{
		Use:   "upsert <key>",
		Short: "Create or update an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Environment.UpsertEnvironment(cmd.Context(), &pb.UpsertEnvironmentRequest{
					Key:               args[0],
					DisplayName:       displayName,
					Rank:              rank,
					RequiresApproval:  requiresApproval,
					GitopsPath:        gitopsPath,
					AllowedPrincipals: allowedPrincipals,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&displayName, "display-name", "", "Human-readable name")
	c.Flags().Int32Var(&rank, "rank", 0, "Ordering for promotion-legality rules; higher is closer to prod")
	c.Flags().BoolVar(&requiresApproval, "requires-approval", false, "Gate promotions behind approval (not enforced pre-gate)")
	c.Flags().StringVar(&gitopsPath, "gitops-path", "", "Path in the gitops repo this environment's state writes to")
	c.Flags().StringSliceVar(&allowedPrincipals, "allowed-principals", nil, "OIDC subjects permitted to promote here")
	return c
}

func newEnvArchiveCmd() *cobra.Command {
	var reason string
	c := &cobra.Command{
		Use:   "archive <key>",
		Short: "Archive an environment",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Environment.ArchiveEnvironment(cmd.Context(), &pb.ArchiveEnvironmentRequest{
					Key:    args[0],
					Reason: reason,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&reason, "reason", "", "Reason, recorded in the audit log")
	_ = c.MarkFlagRequired("reason")
	return c
}
