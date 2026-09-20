package cmd

import (
	"context"
	"log/slog"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
)

// targetProgressReporter builds BuildAppParams.OnProgress for one app
// target: a best-effort call to App Registry's builder-
// authenticated ReportTargetProgress RPC, which signals the release run's
// Temporal ReleaseWorkflow with this target's "built"/"pushed" intra-build
// state -- ahead of the batch-wide NotifyBuildComplete terminal signal (see
// worker/release/workflow.go's awaitBuildCompletion and
// server/handlers/release.go's ReportTargetProgress).
//
// releaseRunID empty (manual/bot fallback dispatch with no Temporal
// release run behind it) returns a no-op callback -- same skip
// ExecuteNotifyBuild applies for the identical reason. client is dialed
// once by the caller (ExecuteBuildReleaseArtifacts) and shared across every
// target/state this batch reports, not redialed per call.
//
// Never fails the build: every error (unknown state, RPC error) is logged
// as a WARNING and swallowed -- a progress-reporting hiccup must not break
// a real image build/push (AGENTS.md logging levels: an optional
// dependency was skipped, the operation still completed).
func targetProgressReporter(ctx context.Context, releaseRunID string, githubRunID int64, client pb.ReleaseRegistryClient, domain, app string) func(state string) {
	if releaseRunID == "" || client == nil {
		return func(string) {}
	}
	ownerFullName := domain + "-" + app
	return func(state string) {
		var pbState pb.ReleaseRunTargetState
		switch state {
		case "built":
			pbState = pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILT
		case "pushed":
			pbState = pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_PUSHED
		default:
			slog.Warn("target progress: unknown state; skipping report",
				slog.String("owner_full_name", ownerFullName), slog.String("state", state))
			return
		}
		_, err := client.ReportTargetProgress(ctx, &pb.ReportTargetProgressRequest{
			ReleaseRunId:  releaseRunID,
			GithubRunId:   githubRunID,
			OwnerFullName: ownerFullName,
			Kind:          pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
			State:         pbState,
		})
		if err != nil {
			slog.Warn("target progress: report failed; continuing build",
				slog.String("owner_full_name", ownerFullName), slog.String("state", state), slog.Any("error", err))
		}
	}
}
