package release

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestResolveBuildRef_CurrentRowPresent_ResolvesToItsGitSHA proves FR9: a
// current app_build_log row for the target resolves to that row's GitSHA,
// not the fallback branch name.
func TestResolveBuildRef_CurrentRowPresent_ResolvesToItsGitSHA(t *testing.T) {
	repo := newTestRegistry(t)
	appID := seedApp(t, repo, "demo", "widget")
	ctx := context.Background()
	_, err := repo.AppBuildLogs().RecordBuildLog(ctx, appID, repository.ArtifactKindImage, "sha-current", "build-1")
	require.NoError(t, err)

	ref, err := resolveBuildRef(ctx, repo, "demo-widget", repository.ArtifactKindImage, "main")
	require.NoError(t, err)
	require.Equal(t, "sha-current", ref)
}

// TestResolveBuildRef_NoCurrentRow_FallsBackToLiteralBranch proves FR10: a
// target with no app_build_log row yet (fresh environment, or an app added
// in a commit reconcile hasn't processed yet) falls back to the literal
// branch name rather than failing the release.
func TestResolveBuildRef_NoCurrentRow_FallsBackToLiteralBranch(t *testing.T) {
	repo := newTestRegistry(t)
	seedApp(t, repo, "demo", "widget")
	ctx := context.Background()

	ref, err := resolveBuildRef(ctx, repo, "demo-widget", repository.ArtifactKindImage, "main")
	require.NoError(t, err)
	require.Equal(t, "main", ref)
}

// TestResolveDispatchRef_UniformRefs_ResolvesToSharedRef proves
// resolveDispatchRef accepts a batch whose targets all resolve to the same
// ref (via their own current app_build_log rows) and returns that ref.
func TestResolveDispatchRef_UniformRefs_ResolvesToSharedRef(t *testing.T) {
	repo := newTestRegistry(t)
	ctx := context.Background()

	widgetID := seedApp(t, repo, "demo", "widget")
	_, err := repo.AppBuildLogs().RecordBuildLog(ctx, widgetID, repository.ArtifactKindImage, "sha-shared", "build-1")
	require.NoError(t, err)

	gadgetID := seedApp(t, repo, "demo", "gadget")
	_, err = repo.AppBuildLogs().RecordBuildLog(ctx, gadgetID, repository.ArtifactKindImage, "sha-shared", "build-1")
	require.NoError(t, err)

	ref, err := resolveDispatchRef(ctx, repo, map[string]string{
		"image:demo-widget": "v1.0.0",
		"image:demo-gadget": "v1.0.0",
	}, "main")
	require.NoError(t, err)
	require.Equal(t, "sha-shared", ref)
}

// TestResolveDispatchRef_HeterogeneousRefs_RejectedWithClearError proves the
// documented uniformVersion-style limitation (buildref.go's
// resolveDispatchRef doc comment): GitHub Actions workflow_dispatch accepts
// exactly one `ref` per call, so a batch whose targets resolve to genuinely
// different refs must be rejected rather than silently building some
// targets from the wrong commit.
func TestResolveDispatchRef_HeterogeneousRefs_RejectedWithClearError(t *testing.T) {
	repo := newTestRegistry(t)
	ctx := context.Background()

	widgetID := seedApp(t, repo, "demo", "widget")
	_, err := repo.AppBuildLogs().RecordBuildLog(ctx, widgetID, repository.ArtifactKindImage, "sha-widget", "build-1")
	require.NoError(t, err)

	gadgetID := seedApp(t, repo, "demo", "gadget")
	_, err = repo.AppBuildLogs().RecordBuildLog(ctx, gadgetID, repository.ArtifactKindImage, "sha-gadget", "build-1")
	require.NoError(t, err)

	_, err = resolveDispatchRef(ctx, repo, map[string]string{
		"image:demo-widget": "v1.0.0",
		"image:demo-gadget": "v1.0.0",
	}, "main")
	require.Error(t, err)
	require.ErrorContains(t, err, "heterogeneous build refs")
}

// TestResolveDispatchRef_MixedCurrentAndFallback_RejectedWhenTheyDiffer
// covers the case FR10's fallback and FR9's current-row resolution meet in
// the same batch: one target has a current app_build_log row, the other
// does not (so it falls back to the literal branch) -- when those two
// values differ, the batch is still rejected rather than silently
// dispatching against a mix.
func TestResolveDispatchRef_MixedCurrentAndFallback_RejectedWhenTheyDiffer(t *testing.T) {
	repo := newTestRegistry(t)
	ctx := context.Background()

	widgetID := seedApp(t, repo, "demo", "widget")
	_, err := repo.AppBuildLogs().RecordBuildLog(ctx, widgetID, repository.ArtifactKindImage, "sha-widget", "build-1")
	require.NoError(t, err)

	// gadget has no app_build_log row -- resolves to the fallback "main",
	// which differs from widget's "sha-widget".
	seedApp(t, repo, "demo", "gadget")

	_, err = resolveDispatchRef(ctx, repo, map[string]string{
		"image:demo-widget": "v1.0.0",
		"image:demo-gadget": "v1.0.0",
	}, "main")
	require.Error(t, err)
	require.ErrorContains(t, err, "heterogeneous build refs")
}

// TestResolveDispatchRef_NoVersions_ReturnsClearError proves the guard
// against an empty resolved plan reaching this far.
func TestResolveDispatchRef_NoVersions_ReturnsClearError(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := resolveDispatchRef(context.Background(), repo, map[string]string{}, "main")
	require.Error(t, err)
	require.ErrorContains(t, err, "no versions")
}

// TestResolveBuildRef_UnknownOwner_ReturnsError proves an owner that was
// never reconciled (no app/chart identity row at all -- distinct from FR10's
// "reconciled but no build log yet" case) fails clearly rather than being
// treated as a fallback case.
func TestResolveBuildRef_UnknownOwner_ReturnsError(t *testing.T) {
	repo := newTestRegistry(t)
	_, err := resolveBuildRef(context.Background(), repo, "demo-nonexistent", repository.ArtifactKindImage, "main")
	require.Error(t, err)
}
