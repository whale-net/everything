package release

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
)

// fakeReleaseRunPublisher records every PublishReleaseRun call for testing
// purposes, mirroring worker/writeback/argosync_test.go's FakePublisher.
// Implements events.PublisherInterface (assigned directly to
// Activities.Publisher below) via structural typing -- no explicit import
// of the events package needed here since nothing in this file references
// its types by name.
type fakeReleaseRunPublisher struct {
	events []releaseRunPublishedEvent
}

type releaseRunPublishedEvent struct {
	ReleaseRunID string
	EventKind    string
	EventStatus  string
}

func newFakeReleaseRunPublisher() *fakeReleaseRunPublisher {
	return &fakeReleaseRunPublisher{}
}

func (f *fakeReleaseRunPublisher) Publish(promotionID, eventKind, eventStatus string) {}

func (f *fakeReleaseRunPublisher) PublishReleaseRun(releaseRunID, eventKind, eventStatus string) {
	f.events = append(f.events, releaseRunPublishedEvent{
		ReleaseRunID: releaseRunID,
		EventKind:    eventKind,
		EventStatus:  eventStatus,
	})
}

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

// seedCLIApp reconciles one app_type "cli" app (binary deploy_unit, no
// image ever built for it -- matching how tools-app-registry and
// tools-release_helper_go are actually registered) and returns its AppID.
func seedCLIApp(t *testing.T, repo *fake.Registry, domain, name string) string {
	t.Helper()
	ctx := context.Background()
	_, err := repo.Reconcile(ctx, []*appmetapb.AppManifest{
		{Domain: domain, Name: name, AppType: "cli", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
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

// TestActivities_VerifyPublished_CLIBinaryTarget_LooksUpUnderBinaryKind is a
// regression test for a real prod incident: release_run_target rows for
// tools-app-registry/tools-release_helper_go always carry Kind
// ArtifactKindImage (ui/release_scope.go's targetsFromAppsAndCharts tags
// every app target IMAGE at the TriggerRelease layer, regardless of
// AppType -- these two are app_type "cli"), but recordCLIBinaryArtifact
// (finalize.go) actually records their published artifact under
// ArtifactKindBinary. Before this fix, VerifyPublished looked up t.Kind
// (IMAGE) verbatim, which these apps never have and never will (build-app
// skips the image build entirely for app_type "cli") -- reporting "no
// published artifact found" on every release, even when the binary
// genuinely published successfully. See finalize.go's publishCLIBinaries
// doc comment for the original incident this describes.
func TestActivities_VerifyPublished_CLIBinaryTarget_LooksUpUnderBinaryKind(t *testing.T) {
	repo := newTestRegistry(t)
	appID := seedCLIApp(t, repo, "tools", "app-registry")
	repo.SeedArtifact(repository.Artifact{
		Kind:    repository.ArtifactKindBinary,
		AppID:   appID,
		Version: "v0.9.0",
		Digest:  "sha256:cli-binary",
		State:   repository.ArtifactStatePublished,
	})

	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "tools-app-registry", Kind: repository.ArtifactKindImage},
	})
	expectedVersions := map[string]string{
		repository.TargetKey(repository.ArtifactKindImage, "tools-app-registry"): "v0.9.0",
	}

	a := &Activities{Registry: repo}
	result, err := a.VerifyPublished(context.Background(), run.ReleaseRunID, expectedVersions)
	require.NoError(t, err)
	require.True(t, result.AllPublished, "cli-binary target must be verified against its actual ArtifactKindBinary record, not the release_run_target's ArtifactKindImage")
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

// TestActivities_RecordTargetState_BackwardsNonTerminalRequest_NoOp is FR5's
// direct regression test (issue #1701): a caller requesting a state BEHIND
// the row's current non-terminal state (e.g. FinalizePublish's own
// Publishing call racing/arriving after FR1's Building loop -- or, as here,
// a retried Publishing request after Recording already landed) is a no-op,
// not an error. Unlike TestActivities_RecordTargetState_DefensiveNoOpOnContradictingTerminalState
// (which covers the already-terminal short-circuit), this exercises
// RecordTargetState's endIdx < startIdx walk-skip directly: Recording ->
// Publishing is a real backwards request between two NON-terminal states.
func TestActivities_RecordTargetState_BackwardsNonTerminalRequest_NoOp(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})

	a := &Activities{Registry: repo}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateBuilding, "", ""))
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStatePublishing, "", ""))
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateRecording, "", ""))

	// Backwards request: the row is already at Recording, a step further
	// than the Publishing this call asks for.
	err := a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStatePublishing, "", "")
	require.NoError(t, err, "a backwards request between two non-terminal states must be a no-op, not an error (FR5)")

	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), run.ReleaseRunID)
	require.NoError(t, err)
	require.Equal(t, repository.ReleaseRunTargetStateRecording, targets[0].State, "the backwards request must not regress the row's state")
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

// --- RecordTargetState publish (FR7, issue #1702) ---

// TestActivities_RecordTargetState_PublishesOncePerLandedWrite drives a
// QUEUED->BUILDING->PUBLISHING->RECORDING->SUCCEEDED progression for one
// target, one RecordTargetState call per adjacent step (the shape
// ReleaseWorkflow/FinalizePublish actually use -- see that method's doc
// comment), and asserts exactly one PublishReleaseRun call lands per step,
// each carrying the run's id, in order.
func TestActivities_RecordTargetState_PublishesOncePerLandedWrite(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	pub := newFakeReleaseRunPublisher()
	a := &Activities{Registry: repo, Publisher: pub}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}

	// Image targets' full progression includes Built/Pushed
	// between Building and Publishing -- see releaseRunTargetStateOrderImage.
	progression := []repository.ReleaseRunTargetState{
		repository.ReleaseRunTargetStateBuilding,
		repository.ReleaseRunTargetStateBuilt,
		repository.ReleaseRunTargetStatePushed,
		repository.ReleaseRunTargetStatePublishing,
		repository.ReleaseRunTargetStateRecording,
		repository.ReleaseRunTargetStateSucceeded,
	}
	for _, state := range progression {
		require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, state, "build-1", ""))
	}

	require.Len(t, pub.events, len(progression), "expected exactly one publish per landed write")
	for i, state := range progression {
		require.Equal(t, run.ReleaseRunID, pub.events[i].ReleaseRunID)
		require.Equal(t, "release_target_"+string(state), pub.events[i].EventKind)
	}
	require.Equal(t, "succeeded", pub.events[len(pub.events)-1].EventStatus)
}

// TestActivities_RecordTargetState_MultiStepWalk_PublishesOncePerStep
// covers the other call shape: a single RecordTargetState call that must
// walk several intermediate states to catch up (e.g. VerifyPublished's
// terminal call after Building was the last state explicitly recorded) --
// each landed UpdateTargetState still gets its own publish, not one for
// the whole call.
func TestActivities_RecordTargetState_MultiStepWalk_PublishesOncePerStep(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	pub := newFakeReleaseRunPublisher()
	a := &Activities{Registry: repo, Publisher: pub}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}

	// One call straight from Queued to Succeeded walks Building, Built,
	// Pushed, Publishing, Recording, Succeeded (image target's fuller
	// releaseRunTargetStateOrderImage) -- six landed writes.
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))

	require.Len(t, pub.events, 6)
	wantKinds := []string{
		"release_target_building",
		"release_target_built",
		"release_target_pushed",
		"release_target_publishing",
		"release_target_recording",
		"release_target_succeeded",
	}
	for i, kind := range wantKinds {
		require.Equal(t, kind, pub.events[i].EventKind)
		require.Equal(t, run.ReleaseRunID, pub.events[i].ReleaseRunID)
	}
}

// TestActivities_RecordTargetState_FailedPublishesOnce covers the direct
// Failed transition (skips the walk entirely).
func TestActivities_RecordTargetState_FailedPublishesOnce(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	pub := newFakeReleaseRunPublisher()
	a := &Activities{Registry: repo, Publisher: pub}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}

	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateFailed, "", "build failed"))

	require.Len(t, pub.events, 1)
	require.Equal(t, run.ReleaseRunID, pub.events[0].ReleaseRunID)
	require.Equal(t, "release_target_failed", pub.events[0].EventKind)
	require.Equal(t, "failed", pub.events[0].EventStatus)
}

// TestActivities_RecordTargetState_NoopRetry_PublishesNothing is FR7's
// idempotency counterpart to TestActivities_RecordTargetState_IdempotentRetry:
// a retried call that lands on the no-op early return (already at the
// desired state) must not emit an event -- a retry that changes nothing
// must not tell the page anything changed.
func TestActivities_RecordTargetState_NoopRetry_PublishesNothing(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	pub := newFakeReleaseRunPublisher()
	a := &Activities{Registry: repo, Publisher: pub}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}

	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))
	require.Len(t, pub.events, len(releaseRunTargetStateOrderImage)-1, "sanity: the first call walked queued->succeeded (image target's fuller order)")

	before := len(pub.events)
	// Redelivered retry of the exact same final call -- the no-op early
	// return (row.State == newState).
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))
	require.Len(t, pub.events, before, "a no-op retry must not publish")
}

// failingUpdateStateReleaseRuns wraps a real repository.ReleaseRunRepository
// and injects a failure on the one UpdateTargetState call whose newState
// equals failState -- letting tests exercise "the repository write itself
// failed" without a real broken database.
type failingUpdateStateReleaseRuns struct {
	repository.ReleaseRunRepository
	failState repository.ReleaseRunTargetState
}

func (f failingUpdateStateReleaseRuns) UpdateTargetState(ctx context.Context, releaseRunTargetID string, newState repository.ReleaseRunTargetState, buildID, errorDetail string) error {
	if newState == f.failState {
		return fmt.Errorf("injected failure for %s", newState)
	}
	return f.ReleaseRunRepository.UpdateTargetState(ctx, releaseRunTargetID, newState, buildID, errorDetail)
}

// registryWithFailingReleaseRuns wraps a real repository.Registry, routing
// every other repository through unchanged and ReleaseRuns() through
// failingUpdateStateReleaseRuns.
type registryWithFailingReleaseRuns struct {
	repository.Registry
	failState repository.ReleaseRunTargetState
}

func (r registryWithFailingReleaseRuns) ReleaseRuns() repository.ReleaseRunRepository {
	return failingUpdateStateReleaseRuns{ReleaseRunRepository: r.Registry.ReleaseRuns(), failState: r.failState}
}

// TestActivities_RecordTargetState_RepositoryWriteFailure_PublishesNothing
// proves the NFR3 structural contract this task's issue calls for directly:
// with a real UpdateTargetState failure injected on the Building step, the
// walk returns an error and exactly zero PublishReleaseRun calls land --
// the publish call sits strictly after the write it announces, never
// before it and never on its error path.
func TestActivities_RecordTargetState_RepositoryWriteFailure_PublishesNothing(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
	})
	pub := newFakeReleaseRunPublisher()
	failing := registryWithFailingReleaseRuns{Registry: repo, failState: repository.ReleaseRunTargetStateBuilding}
	a := &Activities{Registry: failing, Publisher: pub}
	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}

	err := a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", "")
	require.Error(t, err)
	require.Empty(t, pub.events, "a failed repository write must not publish")
}

// TestActivities_RecordTargetState_NilPublisher_NoPanic proves NFR6: every
// RecordTargetState path (walk, direct Failed) runs to completion with a
// nil Publisher -- the default when RABBITMQ_URL is unset.
func TestActivities_RecordTargetState_NilPublisher_NoPanic(t *testing.T) {
	repo := newTestRegistry(t)
	run, _ := createTestReleaseRun(t, repo, []repository.ReleaseRunTarget{
		{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage},
		{OwnerFullName: "demo-gadget", Kind: repository.ArtifactKindImage},
	})
	a := &Activities{Registry: repo}

	target := ReleaseTarget{OwnerFullName: "demo-widget", Kind: repository.ArtifactKindImage}
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, target, repository.ReleaseRunTargetStateSucceeded, "build-1", ""))

	failTarget := ReleaseTarget{OwnerFullName: "demo-gadget", Kind: repository.ArtifactKindImage}
	require.NoError(t, a.RecordTargetState(context.Background(), run.ReleaseRunID, failTarget, repository.ReleaseRunTargetStateFailed, "", "boom"))
}
