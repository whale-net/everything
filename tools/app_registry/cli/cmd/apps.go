package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

func newAppsCmd() *cobra.Command {
	appsCmd := &cobra.Command{
		Use:   "apps",
		Short: "Query and manage registered apps",
	}
	appsCmd.AddCommand(
		newAppsListCmd(),
		newAppsGetCmd(),
		newAppsSetStatusCmd(),
		newAppsReconcileCmd(),
		newAppsAssertCmd(),
		newAppsRecordBuildLogCmd(),
	)
	return appsCmd
}

func newAppsListCmd() *cobra.Command {
	var domain, status, deployUnit string
	c := &cobra.Command{
		Use:   "list",
		Short: "List apps",
		RunE: func(cmd *cobra.Command, args []string) error {
			req := &pb.ListAppsRequest{Domain: domain}
			if status != "" {
				s, err := parseAppStatus(status)
				if err != nil {
					return err
				}
				req.Statuses = []pb.AppStatus{s}
			}
			if deployUnit != "" {
				d, err := parseDeployUnit(deployUnit)
				if err != nil {
					return err
				}
				req.DeployUnit = d
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.ListApps(cmd.Context(), req)
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&domain, "domain", "", "Filter by domain")
	c.Flags().StringVar(&status, "status", "", "Filter by status (active|missing|archived)")
	c.Flags().StringVar(&deployUnit, "deploy-unit", "", "Filter by deploy unit (chart|image|none|binary)")
	return c
}

func newAppsGetCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "get <domain-name>",
		Short: "Get one app by full name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.GetApp(cmd.Context(), &pb.GetAppRequest{FullName: args[0]})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	return c
}

func newAppsSetStatusCmd() *cobra.Command {
	var status, reason string
	c := &cobra.Command{
		Use:   "set-status <app-id>",
		Short: "Triage a MISSING app: archive it or restore it",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			s, err := parseAppStatus(status)
			if err != nil {
				return err
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.SetAppStatus(cmd.Context(), &pb.SetAppStatusRequest{
					AppId:  args[0],
					Status: s,
					Reason: reason,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&status, "status", "", "New status (active|archived)")
	c.Flags().StringVar(&reason, "reason", "", "Reason, recorded in the audit log")
	_ = c.MarkFlagRequired("status")
	_ = c.MarkFlagRequired("reason")
	return c
}

func newAppsReconcileCmd() *cobra.Command {
	var fromPlan string
	var dryRun bool
	c := &cobra.Command{
		Use:   "reconcile",
		Short: "Reconcile app/chart identity from a release plan (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(fromPlan)
			if err != nil {
				return fmt.Errorf("failed to read plan file %s: %w", fromPlan, err)
			}
			manifests := &appmetapb.AppManifestSet{}
			if err := unmarshalJSON(data, manifests); err != nil {
				return fmt.Errorf("failed to parse plan file %s as AppManifestSet: %w", fromPlan, err)
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.ReconcileApps(cmd.Context(), &pb.ReconcileAppsRequest{
					Manifests:      manifests,
					DryRun:         dryRun,
					IdempotencyKey: idempotencyKeyFlag,
				})
				if err != nil {
					return err
				}
				// Skipped-stale is a no-op SUCCESS (see
				// ReconcileAppsResponse.skipped_stale's doc comment) so it
				// must not fail the command, but it must not be silent
				// either -- a CI log that only shows a green step for a
				// call that wrote nothing is exactly the failure mode
				// issue #545 exists to fix. Mirrors promote.go's
				// already-promoted/dry-run stderr banners.
				if resp.SkippedStale {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"reconcile skipped: this manifest set (git_sha=%q) is stale relative to the current watermark (git_sha=%q); nothing was written\n",
						manifests.GitSha, resp.CurrentWatermarkGitSha)
				}
				printUnresolvedChartsWarning(cmd, resp)
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&fromPlan, "from-plan", "", "Path to an AppManifestSet JSON file (from bazel query)")
	c.Flags().BoolVar(&dryRun, "dry-run", false, "Compute the diff without writing")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>")
	_ = c.MarkFlagRequired("from-plan")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

// newAppsAssertCmd implements `apps assert` -- AR-7c (issue #558), the
// additive-only counterpart to `apps reconcile`. Called from release.yml as
// the first step of a release run, against whatever manifest set that ref
// discovers -- unlike reconcile it is safe from ANY ref (never marks
// anything MISSING), so it needs no --dry-run flag: there is no absence
// sweep to preview.
func newAppsAssertCmd() *cobra.Command {
	var fromPlan string
	c := &cobra.Command{
		Use:   "assert",
		Short: "Additively assert app/chart identity + manifest snapshot from any ref (release)",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(fromPlan)
			if err != nil {
				return fmt.Errorf("failed to read plan file %s: %w", fromPlan, err)
			}
			manifests := &appmetapb.AppManifestSet{}
			if err := unmarshalJSON(data, manifests); err != nil {
				return fmt.Errorf("failed to parse plan file %s as AppManifestSet: %w", fromPlan, err)
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.App.AssertApps(cmd.Context(), &pb.AssertAppsRequest{
					Manifests:      manifests,
					IdempotencyKey: idempotencyKeyFlag,
				})
				if err != nil {
					return err
				}
				printRejectedOwnersWarning(cmd, resp.RejectedApps, resp.RejectedCharts)
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&fromPlan, "from-plan", "", "Path to an AppManifestSet JSON file (from bazel query)")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>[-<domain>-<app>]")
	_ = c.MarkFlagRequired("from-plan")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

// printRejectedOwnersWarning renders every app/chart AssertApps declined to
// touch (ARCHIVED) prominently, on stderr, ahead of the JSON body --
// mirrors printUnresolvedChartsWarning below: a per-item skip must not be
// able to hide inside an otherwise-green step.
func printRejectedOwnersWarning(cmd *cobra.Command, apps, charts []*pb.RejectedOwner) {
	if len(apps) == 0 && len(charts) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"\n*** REJECTED: %d app(s), %d chart(s) skipped -- ARCHIVED, not resurrected ***\n",
		len(apps), len(charts))
	for _, a := range apps {
		fmt.Fprintf(cmd.ErrOrStderr(), "  - app %s/%s: %s\n", a.Domain, a.Name, a.Reason)
	}
	for _, c := range charts {
		fmt.Fprintf(cmd.ErrOrStderr(), "  - chart %s/%s: %s\n", c.Domain, c.Name, c.Reason)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
}

// printUnresolvedChartsWarning renders every UnresolvedChart in resp
// prominently, on stderr, ahead of the JSON body on stdout -- mirroring
// promote.go's printDriftWarning. AR-7a made an unresolved chart a per-chart
// skip rather than a whole-reconcile failure (see
// ReconcileAppsResponse.unresolved_charts's doc comment), so the command
// exits 0 and the JSON response looks otherwise healthy; without this, a
// skipped chart -- and the app identity that never got recorded because of
// it -- could hide inside a green CI step exactly like the continue-on-error
// wedge this phase exists to fix.
func printUnresolvedChartsWarning(cmd *cobra.Command, resp *pb.ReconcileAppsResponse) {
	if len(resp.UnresolvedCharts) == 0 {
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(),
		"\n*** UNRESOLVED CHARTS: %d chart(s) skipped -- their apps references did not fully resolve ***\n",
		len(resp.UnresolvedCharts))
	for _, uc := range resp.UnresolvedCharts {
		fmt.Fprintf(cmd.ErrOrStderr(), "  - %s/%s: %s (offending: %v)\n",
			uc.Domain, uc.Name, uc.Reason, uc.AppRefs)
	}
	fmt.Fprintln(cmd.ErrOrStderr())
}

func parseAppStatus(s string) (pb.AppStatus, error) {
	switch s {
	case "active":
		return pb.AppStatus_APP_STATUS_ACTIVE, nil
	case "missing":
		return pb.AppStatus_APP_STATUS_MISSING, nil
	case "archived":
		return pb.AppStatus_APP_STATUS_ARCHIVED, nil
	default:
		return pb.AppStatus_APP_STATUS_UNSPECIFIED, fmt.Errorf("unknown status %q (want active|missing|archived)", s)
	}
}

func parseDeployUnit(s string) (appmetapb.DeployUnit, error) {
	switch s {
	case "chart":
		return appmetapb.DeployUnit_DEPLOY_UNIT_CHART, nil
	case "image":
		return appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE, nil
	case "none":
		return appmetapb.DeployUnit_DEPLOY_UNIT_NONE, nil
	case "binary":
		return appmetapb.DeployUnit_DEPLOY_UNIT_BINARY, nil
	default:
		return appmetapb.DeployUnit_DEPLOY_UNIT_UNSPECIFIED, fmt.Errorf("unknown deploy-unit %q (want chart|image|none|binary)", s)
	}
}
