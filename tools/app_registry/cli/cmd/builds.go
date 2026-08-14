package cmd

import (
	"time"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

func newBuildsCmd() *cobra.Command {
	buildsCmd := &cobra.Command{
		Use:   "builds",
		Short: "Record and inspect CI builds",
	}
	buildsCmd.AddCommand(newBuildsRecordCmd(), newBuildsStatusCmd(), newBuildsListCmd())
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

// newBuildsStatusCmd is the run-log query CLI (AR-7d, issue #558): the
// build for one CI run plus every artifact hanging off it, in whatever
// state each is in. This is the "did my release finish, and if not, what's
// left" command an operator reaches for after a release run is killed or
// fails partway through -- see OPERATIONS.md "A release run didn't
// complete".
func newBuildsStatusCmd() *cobra.Command {
	var workflowAttempt int
	var incompleteOnly bool
	c := &cobra.Command{
		Use:   "status <workflow-run-id>",
		Short: "Show a CI run's build and every child artifact's state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.GetReleaseRun(cmd.Context(), &pb.GetReleaseRunRequest{
					WorkflowRunId:   args[0],
					WorkflowAttempt: int32(workflowAttempt),
				})
				if err != nil {
					return err
				}
				if incompleteOnly {
					resp.Artifacts = incompleteArtifacts(resp.Artifacts)
				}
				return printResponse(resp)
			})
		},
	}
	c.Flags().IntVar(&workflowAttempt, "attempt", 0, "GitHub Actions run attempt (default: latest recorded for this run id)")
	c.Flags().BoolVar(&incompleteOnly, "incomplete", false, "Only show artifacts not yet published")
	return c
}

// newBuildsListCmd is the operator-facing browser over the `build` table
// (migration 001, issue #608) -- see ARCHITECTURE.md "ListBuilds" and
// OPERATIONS.md's "browsing build history" note. Distinct from `builds
// status <workflow-run-id>` above: this browses recent builds, that does a
// point lookup for one CI run's build plus its child artifacts.
func newBuildsListCmd() *cobra.Command {
	var since int64
	var pageSize int32
	var pageToken string
	c := &cobra.Command{
		Use:   "list",
		Short: "List recent CI builds, most recent first",
		RunE: func(cmd *cobra.Command, args []string) error {
			return withClient(cmd, func(rc *registryClient) error {
				resp, err := rc.Artifact.ListBuilds(cmd.Context(), &pb.ListBuildsRequest{
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
	c.Flags().Int64Var(&since, "since", 0, "Only show builds recorded at or after this Unix timestamp")
	c.Flags().Int32Var(&pageSize, "page-size", 0, "Max rows to return (0 = server default)")
	c.Flags().StringVar(&pageToken, "page-token", "", "Resume from a previous response's next_page_token")
	return c
}

// incompleteArtifacts filters artifacts down to those NOT in state
// ARTIFACT_STATE_PUBLISHED -- "what is incomplete?" per ARCHITECTURE.md
// "The run log". A plain slice filter, not a server-side field: it is
// exactly a filter over GetReleaseRunResponse.artifacts, not separately
// derived data, so keeping it client-side avoids a second wire shape for
// the same information.
func incompleteArtifacts(artifacts []*pb.Artifact) []*pb.Artifact {
	out := make([]*pb.Artifact, 0, len(artifacts))
	for _, a := range artifacts {
		if a.State != pb.ArtifactState_ARTIFACT_STATE_PUBLISHED {
			out = append(out, a)
		}
	}
	return out
}
