package cmd

import (
	"time"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func newBuildsCmd() *cobra.Command {
	buildsCmd := &cobra.Command{
		Use:   "builds",
		Short: "Record CI builds",
	}
	buildsCmd.AddCommand(newBuildsRecordCmd())
	return buildsCmd
}

// newBuildsRecordCmd is the CI write path called once per workflow run,
// before per-app artifacts record calls that reference the returned build id.
func newBuildsRecordCmd() *cobra.Command {
	var gitSHA, gitRef, workflowRunID, actor string
	var workflowAttempt int
	var startedAt int64
	c := &cobra.Command{
		Use:   "record",
		Short: "Record a CI build (CI)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if startedAt == 0 {
				startedAt = time.Now().Unix()
			}
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.RecordBuild(cmd.Context(), &pb.RecordBuildRequest{
					GitSha:          gitSHA,
					GitRef:          gitRef,
					WorkflowRunId:   workflowRunID,
					WorkflowAttempt: int32(workflowAttempt),
					Actor:           actor,
					StartedAt:       startedAt,
					IdempotencyKey:  idempotencyKeyFlag,
				})
				if err != nil {
					return err
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().StringVar(&gitSHA, "git-sha", "", "Commit SHA")
	c.Flags().StringVar(&gitRef, "git-ref", "", "Git ref (branch or tag)")
	c.Flags().StringVar(&workflowRunID, "workflow-run-id", "", "GitHub Actions run id")
	c.Flags().IntVar(&workflowAttempt, "workflow-attempt", 1, "GitHub Actions run attempt")
	c.Flags().StringVar(&actor, "actor", "", "GitHub actor that triggered the run")
	c.Flags().Int64Var(&startedAt, "started-at", 0, "Unix timestamp the build started (default: now)")
	c.Flags().StringVar(&idempotencyKeyFlag, "idempotency-key", "", "Required. <workflow_run_id>-<attempt>")
	_ = c.MarkFlagRequired("git-sha")
	_ = c.MarkFlagRequired("workflow-run-id")
	_ = c.MarkFlagRequired("idempotency-key")
	return c
}
