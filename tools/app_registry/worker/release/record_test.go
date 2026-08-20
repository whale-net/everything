package release

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

func newTestRegistry(t *testing.T) *fake.Registry {
	t.Helper()
	return fake.New()
}

// seedApp reconciles one app (image deploy_unit, so its own image
// artifacts are directly promotable/publishable -- irrelevant to
// VerifyPublished, which only cares that an artifact row exists) and
// returns its AppID.
func seedApp(t *testing.T, repo *fake.Registry, domain, name string) string {
	t.Helper()
	ctx := context.Background()
	_, err := repo.Reconcile(ctx, []*appmetapb.AppManifest{
		{Domain: domain, Name: name, DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
	}, nil, repository.ReconcileSource{DiscoveredAt: 1}, false)
	require.NoError(t, err)
	app, err := repo.Apps().GetAppByFullName(ctx, domain+"-"+name)
	require.NoError(t, err)
	return app.AppID
}

func createTestReleaseRun(t *testing.T, repo *fake.Registry, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget) {
	t.Helper()
	var outRun *repository.ReleaseRun
	var outTargets []repository.ReleaseRunTarget
	err := repo.WithTx(context.Background(), func(ctx context.Context, reg repository.Registry) error {
		var ferr error
		outRun, outTargets, ferr = reg.ReleaseRuns().CreateReleaseRun(ctx, repository.ReleaseRun{
			TriggeredBy:        "unit-test",
			RequestedScope:     "demo",
			TemporalWorkflowID: "wf-" + t.Name(),
		}, targets)
		return ferr
	})
	require.NoError(t, err)
	return outRun, outTargets
}

// --- VerifyPublished ---

func TestActivities_VerifyPublished_AllPublished(t *testing.T) {
	repo := newTestRegistry(t)
	appID := seedApp(t, repo, "demo", "widget")
	repo.SeedArtifact(repository.Artifact{
		Kind:    repository.ArtifactKindImage,
		AppID:   appID,
		Version: "v1.0.1",
		Digest:  "sha256:widget",
		State:   repository.ArtifactStatePublished,
	})

	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.True(t, result.AllPublished)
	require.Empty(t, result.Failed)
}

func TestActivities_VerifyPublished_MissingArtifact_ReportsFailed(t *testing.T) {
	repo := newTestRegistry(t)
	// No artifact ever seeded for demo-widget.
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.False(t, result.AllPublished)
	require.Contains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-widget"))
}

func TestActivities_VerifyPublished_PartialFailure(t *testing.T) {
	repo := newTestRegistry(t)
	appID := seedApp(t, repo, "demo", "widget")
	repo.SeedArtifact(repository.Artifact{
		Kind:    repository.ArtifactKindImage,
		AppID:   appID,
		Version: "v1.0.1",
		Digest:  "sha256:widget",
		State:   repository.ArtifactStatePublished,
	})

	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
		{OwnerFullName: "demo-gadget", Kind: repository.ArtifactKindImage}, // never published
	})

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.False(t, result.AllPublished)
	require.NotContains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-widget"))
	require.Contains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-gadget"))
}

// --- RecordTargetState ---

func TestActivities_RecordTargetState_WalksQueuedToSucceeded(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	err := a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", "")
	require.NoError(t, err)

	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.Len(t, targets, 1)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, targets[0].State)
	require.Equal(t, "build-1", targets[0].BuildID)
}

func TestActivities_RecordTargetState_FailedDirectFromQueued(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	err := a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateFailed, "", "build failed")
	require.NoError(t, err)

	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.Equal(t, repository.ReleaseRunTargetStateFailed, targets[0].State)
	require.Equal(t, "build failed", targets[0].ErrorDetail)
}

// TestActivities_RecordTargetState_IdempotentRetry proves NFR3: calling
// RecordTargetState twice with the same desired terminal state (simulating
// Temporal's at-least-once activity redelivery) is a no-op the second time,
// not a "cannot transition" error.
func TestActivities_RecordTargetState_IdempotentRetry(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))
	// Redelivered retry of the exact same final call.
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))

	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, targets[0].State)
}

// TestActivities_RecordTargetState_DefensiveNoOpOnContradictingTerminalState
// proves RecordTargetState never crashes the workflow when told to move a
// target that is already terminal to a *different* terminal state -- see
// that method's doc comment.
func TestActivities_RecordTargetState_DefensiveNoOpOnContradictingTerminalState(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateFailed, "", "too late"))

	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.Equal(t, repository.ReleaseRunTargetStateSucceeded, targets[0].State, "the first terminal write must stick")
}
