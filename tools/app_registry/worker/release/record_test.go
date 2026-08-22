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
	expectedVersions := map[string]string{repository.TargetKey(repository.ArtifactKindImage, "demo-widget"): "v1.0.1"}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, expectedVersions)
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
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, nil)
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
	expectedVersions := map[string]string{
		repository.TargetKey(repository.ArtifactKindImage, "demo-widget"): "v1.0.1",
		repository.TargetKey(repository.ArtifactKindImage, "demo-gadget"): "v1.0.1",
	}

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, expectedVersions)
	require.NoError(t, err)
	require.False(t, result.AllPublished)
	require.NotContains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-widget"))
	require.Contains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-gadget"))
}

// TestActivities_VerifyPublished_VersionMismatch_ReportsFailed is the
// issue #973 regression test: FinalizePublish deliberately does not fail
// the workflow when a single target's finalize-app/finalize-chart call
// fails (e.g. a GHCR retag returning DENIED) -- that target simply never
// reaches RecordArtifact for the NEW version. If an OLDER version of the
// same target was already Published from a prior release run, the
// presence+state check alone (pre-#973 behavior) still found that old
// artifact and reported the target satisfied -- masking the real failure
// and letting the workflow record the target Succeeded despite the
// requested version never having been published. This asserts
// VerifyPublished now catches that mismatch instead. Unlike PR #976's
// first pass (which re-derived the expected version from
// release_run.resolved_plan), expectedVersions here is passed directly, as
// workflow.go's ReleaseWorkflow now does from FinalizeResult.Targets.
func TestActivities_VerifyPublished_VersionMismatch_ReportsFailed(t *testing.T) {
	repo := newTestRegistry(t)
	appID := seedApp(t, repo, "demo", "widget")
	// vOLD is still Published from a prior release run -- vNEW's
	// finalize-app call failed and never reached RecordArtifact.
	repo.SeedArtifact(repository.Artifact{
		Kind:    repository.ArtifactKindImage,
		AppID:   appID,
		Version: "v0.6.2",
		Digest:  "sha256:old",
		State:   repository.ArtifactStatePublished,
	})

	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	expectedVersions := map[string]string{repository.TargetKey(repository.ArtifactKindImage, "demo-widget"): "v0.6.3"}

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, expectedVersions)
	require.NoError(t, err)
	require.False(t, result.AllPublished, "a published-but-stale-version artifact must not satisfy VerifyPublished")
	require.Contains(t, result.Failed, repository.TargetKey(repository.ArtifactKindImage, "demo-widget"))
	require.Contains(t, result.Failed[repository.TargetKey(repository.ArtifactKindImage, "demo-widget")], "v0.6.2")
	require.Contains(t, result.Failed[repository.TargetKey(repository.ArtifactKindImage, "demo-widget")], "v0.6.3")
}

// TestActivities_VerifyPublished_NoExpectedVersionEntry_FallsBackToPresenceCheck
// proves the defensive fallback: a target with no entry in expectedVersions
// (should not happen for any target FinalizePublish actually processed --
// see FinalizeResult.Targets' doc comment -- but kept for old/test data or
// a future caller that omits it) still passes on presence+Published state
// alone, rather than hard-failing.
func TestActivities_VerifyPublished_NoExpectedVersionEntry_FallsBackToPresenceCheck(t *testing.T) {
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
	// No entry for demo-widget in expectedVersions.
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, map[string]string{})
	require.NoError(t, err)
	require.True(t, result.AllPublished)
	require.Empty(t, result.Failed)
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
