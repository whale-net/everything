package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// newAppsRecordBuildLogCmd implements FR8's write-side call site (issue
// #923): ci.yml's reconcile-app-registry job calls this once, after its
// existing `apps reconcile` step, passing the SAME manifest-set.json
// `apps reconcile` already produced -- see .github/actions/app-registry-
// reconcile/action.yml and ci.yml's reconcile-app-registry job. This
// command loops client-side over every app/chart discovered by that
// manifest set and issues one RecordBuildLog RPC call per owner, all
// sharing --build-id (the build_id from a single, separate `builds record`
// call made once per push -- see migration 019's doc comment for why
// build_id is per-push, not per-app).
//
// Chart full-name derivation mirrors
// server/repository.NormalizeChartName exactly (normalizeChartName below)
// -- duplicated here, not imported, to keep the CLI on the client side of
// the gRPC boundary (cli/cmd deliberately does not depend on
// server/repository; see BUILD.bazel). This is safe because by the time
// this command runs, `apps reconcile` has already run against the exact
// same manifest set and performed the identical normalization server-side
// when creating/updating the chart's identity row -- RecordBuildLog's own
// owner resolution (ArtifactServer.resolveOwner) looks up that row by the
// resulting full name, so a drift here would surface immediately as an
// "owner not reconciled" error, not a silent mismatch.
func newAppsRecordBuildLogCmd() *cobra.Command {
	var fromPlan, buildID, idempotencyKey string
	c := &cobra.Command{
		Use:   "record-build-log",
		Short: "Record an app_build_log row for every app/chart in a manifest set (FR8, issue #923)",
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(fromPlan)
			if err != nil {
				return fmt.Errorf("failed to read plan file %s: %w", fromPlan, err)
			}
			manifests := &appmetapb.AppManifestSet{}
			if err := unmarshalJSON(data, manifests); err != nil {
				return fmt.Errorf("failed to parse plan file %s as AppManifestSet: %w", fromPlan, err)
			}
			if manifests.GitSha == "" {
				return fmt.Errorf("plan file %s has no git_sha", fromPlan)
			}

			return withClient(cmd, func(rc *registryClient) error {
				var written []*pb.AppBuildLog
				for _, app := range manifests.Apps {
					ownerFullName := app.Domain + "-" + app.Name
					resp, err := rc.Artifact.RecordBuildLog(cmd.Context(), &pb.RecordBuildLogRequest{
						OwnerFullName:  ownerFullName,
						Kind:           pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
						GitSha:         manifests.GitSha,
						BuildId:        buildID,
						IdempotencyKey: perOwnerIdempotencyKey(idempotencyKey, "image", ownerFullName),
					})
					if err != nil {
						return fmt.Errorf("record build log for app %s: %w", ownerFullName, err)
					}
					written = append(written, resp.AppBuildLog)
				}
				for _, chart := range manifests.Charts {
					ownerFullName := chart.Domain + "-" + normalizeChartName(chart.Domain, chart.Name)
					resp, err := rc.Artifact.RecordBuildLog(cmd.Context(), &pb.RecordBuildLogRequest{
						OwnerFullName:  ownerFullName,
						Kind:           pb.ArtifactKind_ARTIFACT_KIND_CHART,
						GitSha:         manifests.GitSha,
						BuildId:        buildID,
						IdempotencyKey: perOwnerIdempotencyKey(idempotencyKey, "chart", ownerFullName),
					})
					if err != nil {
						return fmt.Errorf("record build log for chart %s: %w", ownerFullName, err)
					}
					written = append(written, resp.AppBuildLog)
				}
				fmt.Fprintf(cmd.OutOrStdout(), "recorded %d app_build_log row(s) for git_sha=%s build_id=%s\n", len(written), manifests.GitSha, buildID)
				for _, l := range written {
					fmt.Fprintf(cmd.OutOrStdout(), "  - %s (%s): %s\n", l.OwnerFullName, l.Kind, l.AppBuildLogId)
				}
				return nil
			})
		},
	}
	c.Flags().StringVar(&fromPlan, "from-plan", "", "Path to an AppManifestSet JSON file (from bazel query -- the same file `apps reconcile` consumed)")
	c.Flags().StringVar(&buildID, "build-id", "", "build_id returned by `builds record` for this push")
	c.Flags().StringVar(&idempotencyKey, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>-buildlog (a per-owner suffix is appended automatically)")
	_ = c.MarkFlagRequired("from-plan")
	_ = c.MarkFlagRequired("build-id")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}

// perOwnerIdempotencyKey derives a unique idempotency key per owner from
// a single base key, so one CI step's N RecordBuildLog calls (one per
// discovered app/chart) do not collide on the idempotency table's
// (key, method) uniqueness -- each owner needs its own record, but a
// retried CI step replaying the whole command should still replay each
// owner's individual RPC rather than double-writing its row (see
// RecordBuildLogRequest's idempotency_key doc comment).
func perOwnerIdempotencyKey(base, kind, ownerFullName string) string {
	return base + "-" + kind + "-" + ownerFullName
}

// normalizeChartName mirrors server/repository.NormalizeChartName exactly
// -- see this file's package doc comment above for why it is duplicated
// here rather than imported.
func normalizeChartName(domain, name string) string {
	return strings.TrimPrefix(name, "helm-"+domain+"-")
}
