package cmd

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"
	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// NotifyBuildParams configures ExecuteNotifyBuild.
type NotifyBuildParams struct {
	Ctx context.Context
	// ReleaseRunID identifies the release_run row (and through it the
	// Temporal ReleaseWorkflow execution) the GHA build belongs to. Empty
	// means this run was dispatched WITHOUT a Temporal release run behind
	// it -- the manual/bot fallback dispatch path release-v2.yml still
	// supports -- and ExecuteNotifyBuild skips without error (the notify
	// job must never fail a fallback run just because there is nothing to
	// notify).
	ReleaseRunID string
	// GitHubRunID is the notifying Actions run's id (github.RunID). Zero
	// means "unknown" -- the server passes it through as unverifiable
	// rather than rejecting.
	GitHubRunID int64
	// Status is the build run's terminal outcome, using GitHub Actions'
	// own job-result vocabulary so the notify job can forward
	// needs.<job>.result verbatim: success/skipped report succeeded,
	// failure/cancelled report not-succeeded.
	Status string
	// Detail is optional extra context appended to the signal payload's
	// detail line (what the release run's UI shows).
	Detail string
	// ReleaseClient overrides the ReleaseRegistryClient factory -- tests
	// inject a FakeReleaseRegistryClient here. When nil, the real client
	// is dialed via NewReleaseRegistryClient (APP_REGISTRY_ADDRESS +
	// GRPC_AUTH_* env configuration).
	ReleaseClient pb.ReleaseRegistryClient
}

// ExecuteNotifyBuild reports one release-v2 build run's terminal state to
// App Registry's builder-authenticated NotifyBuildComplete RPC, which
// signals the release run's Temporal ReleaseWorkflow so it proceeds without
// waiting for the PollBuild fallback loop. This is the notify half of the
// pipeline's notify-vs-poll split -- see worker/release/awaitBuildCompletion
// and server/handlers/release.go's NotifyBuildComplete for the receiving
// sides.
//
// A response with Signaled=false is deliberately not an error: it means the
// workflow execution is unknown to Temporal (already completed, stale id),
// and polling remains the correctness fallback.
func ExecuteNotifyBuild(p NotifyBuildParams) error {
	if p.ReleaseRunID == "" {
		fmt.Println("no release run to notify (empty --release-run-id: manual/bot dispatch without a Temporal release run); skipping NotifyBuildComplete")
		return nil
	}
	if p.Status == "" {
		return fmt.Errorf("missing required flag: --status")
	}
	succeeded, err := notifyBuildSucceeded(p.Status)
	if err != nil {
		return err
	}

	detail := fmt.Sprintf("run %d conclusion=%s", p.GitHubRunID, p.Status)
	if p.Detail != "" {
		detail = fmt.Sprintf("%s (%s)", detail, p.Detail)
	}

	client, cleanup, err := releaseClientOrDefault(p.Ctx, p.ReleaseClient)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer func() { _ = cleanup() }()
	}

	resp, err := client.NotifyBuildComplete(p.Ctx, &pb.NotifyBuildCompleteRequest{
		ReleaseRunId: p.ReleaseRunID,
		GithubRunId:  p.GitHubRunID,
		Succeeded:    succeeded,
		Detail:       detail,
	})
	if err != nil {
		return fmt.Errorf("notify build complete: %w", err)
	}
	if !resp.GetSignaled() {
		fmt.Println("release workflow not signaled (already completed or unknown to Temporal); the PollBuild fallback owns the release outcome")
		return nil
	}
	fmt.Printf("notified release run %s: build run %d conclusion=%s\n", p.ReleaseRunID, p.GitHubRunID, p.Status)
	return nil
}

// notifyBuildSucceeded maps a GitHub Actions job result (needs.<job>.result
// vocabulary) to the signal's succeeded bit. success and skipped both mean
// the run's green half (skipped = "nothing to build" -- GitHub itself
// concludes the run successful when jobs are skipped, and PollBuild's
// fallback would report the same), failure and cancelled mean the build
// failed.
func notifyBuildSucceeded(status string) (bool, error) {
	switch status {
	case "success", "skipped":
		return true, nil
	case "failure", "cancelled":
		return false, nil
	default:
		return false, fmt.Errorf("--status must be one of success|failure|cancelled|skipped (GitHub Actions job result values), got %q", status)
	}
}

// releaseClientOrDefault resolves the explicit client (tests) or dials the
// real App Registry server via the standard APP_REGISTRY_ADDRESS/GRPC_AUTH_*
// env configuration (NewReleaseRegistryClient).
func releaseClientOrDefault(ctx context.Context, explicit pb.ReleaseRegistryClient) (pb.ReleaseRegistryClient, func() error, error) {
	if explicit != nil {
		return explicit, nil, nil
	}
	return NewReleaseRegistryClient(ctx)
}

// newNotifyBuildCmd builds the `release_helper notify-build` command used by
// release-v2.yml's notify job (which holds the app-registry-builder client
// credentials, so the RPC's builder-role check passes).
func newNotifyBuildCmd() *cobra.Command {
	var (
		releaseRunID string
		runID        int64
		status       string
		detail       string
	)
	cmd := &cobra.Command{
		Use:   "notify-build",
		Short: "Notify the Temporal release workflow that this GitHub Actions build run finished (release-v2 notify job)",
		Long: "notify-build reports one release-v2 build run's terminal state to App " +
			"Registry's builder-authenticated NotifyBuildComplete RPC, which signals " +
			"the release run's Temporal ReleaseWorkflow (release.SignalBuildCompleted) " +
			"so it proceeds to finalize/verify immediately instead of waiting for the " +
			"PollBuild fallback loop. The notify job passes GitHub's own job-result " +
			"vocabulary in --status (success|failure|cancelled|skipped) so\n" +
			"needs.<job>.result can be forwarded verbatim; skipped counts as success " +
			"(nothing to build). A run dispatched WITHOUT a release_run_id (the\n" +
			"manual/bot fallback dispatch path) skips notify entirely -- polling is\n" +
			"always the correctness fallback, so this command must never fail a run\n" +
			"that has nothing to notify.",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if ctx == nil {
				ctx = context.Background()
			}
			return ExecuteNotifyBuild(NotifyBuildParams{
				Ctx:          ctx,
				ReleaseRunID: releaseRunID,
				GitHubRunID:  runID,
				Status:       status,
				Detail:       detail,
			})
		},
	}
	cmd.Flags().StringVar(&releaseRunID, "release-run-id", "",
		"Release run id from the release-v2 workflow_dispatch input (empty: skip -- this run has no Temporal release run behind it)")
	cmd.Flags().Int64Var(&runID, "run-id", 0,
		"GitHub Actions run id of the notifying run (github.RunID; 0/omitted means unknown -- the server passes it through as unverifiable)")
	cmd.Flags().StringVar(&status, "status", "",
		"Build outcome, in GitHub Actions job-result vocabulary: success|failure|cancelled|skipped")
	cmd.Flags().StringVar(&detail, "detail", "",
		"Optional extra detail appended to the signal payload's detail line")
	return cmd
}
