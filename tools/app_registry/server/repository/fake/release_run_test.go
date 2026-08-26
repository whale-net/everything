package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// newReleaseRun mirrors postgres's newReleaseRun test helper
// (postgres_integration_release_run_test.go) -- a minimal valid
// repository.ReleaseRun for CreateReleaseRun.
func newReleaseRun(workflowID string) repository.ReleaseRun {
	return repository.ReleaseRun{
		TriggeredBy:        "unit-test",
		RequestedScope:     "acme",
		ResolvedPlan:       []byte(`{"targets":["image:acme-widget"]}`),
		TemporalWorkflowID: workflowID,
	}
}

func createReleaseRunTx(t *testing.T, r *Registry, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	t.Helper()
	var outRun *repository.ReleaseRun
	var outTargets []repository.ReleaseRunTarget
	err := r.WithTx(context.Background(), func(ctx context.Context, reg repository.Registry) error {
		var ferr error
		outRun, outTargets, ferr = reg.ReleaseRuns().CreateReleaseRun(ctx, run, targets)
		return ferr
	})
	return outRun, outTargets, err
}

func TestReleaseRunFake_CreateReleaseRun_CreatesRunAndQueuedTargets(t *testing.T) {
	r := New()

	run, targets, err := createReleaseRunTx(t, r, newReleaseRun("wf-create-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
		{OwnerFullName: "acme-widget-chart", Kind: repository.ArtifactKindChart},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if run.ReleaseRunID == "" {
		t.Fatalf("expected a generated ReleaseRunID")
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	for _, target := range targets {
		if target.State != repository.ReleaseRunTargetStateQueued {
			t.Fatalf("target %s state = %q, want queued", target.OwnerFullName, target.State)
		}
	}
}

func TestReleaseRunFake_CreateReleaseRun_NoTargetsRejected(t *testing.T) {
	r := New()
	_, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-empty"), nil)
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for zero targets, got: %v", err)
	}
}

func TestReleaseRunFake_CreateReleaseRun_DuplicateWorkflowIDRejected(t *testing.T) {
	r := New()
	targets := []repository.ReleaseRunTarget{{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage}}
	if _, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-dup"), targets); err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	_, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-dup"), targets)
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists for duplicate workflow id, got: %v", err)
	}
}

// TestReleaseRunFake_CreateReleaseRun_WorkflowIDReusableAfterTerminal
// mirrors postgres's identically-named integration test (issue #889,
// migration 017, FR11): a workflow id may be reused by a fresh
// release_run once every target of the prior release_run under that id
// has reached a terminal state.
func TestReleaseRunFake_CreateReleaseRun_WorkflowIDReusableAfterTerminal(t *testing.T) {
	r := New()
	targets := []repository.ReleaseRunTarget{{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage}}
	first, firstTargets, err := createReleaseRunTx(t, r, newReleaseRun("wf-reuse"), targets)
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	ctx := context.Background()
	if err := r.ReleaseRuns().UpdateTargetState(ctx, firstTargets[0].ReleaseRunTargetID, repository.ReleaseRunTargetStateBuilding, "", ""); err != nil {
		t.Fatalf("advance to building: %v", err)
	}
	if err := r.ReleaseRuns().UpdateTargetState(ctx, firstTargets[0].ReleaseRunTargetID, repository.ReleaseRunTargetStateFailed, "", "boom"); err != nil {
		t.Fatalf("advance to failed: %v", err)
	}

	second, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-reuse"), targets)
	if err != nil {
		t.Fatalf("second CreateReleaseRun after first went terminal: %v", err)
	}
	if second.ReleaseRunID == first.ReleaseRunID {
		t.Fatalf("expected a distinct release_run row, got the same id back")
	}
}

func TestReleaseRunFake_UpdateTargetState_LegalTransitionsRoundTrip(t *testing.T) {
	r := New()
	_, targets, err := createReleaseRunTx(t, r, newReleaseRun("wf-transitions"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID
	buildID := r.SeedBuild(repository.Build{WorkflowRunID: "wf-transitions-run"}).BuildID

	ctx := context.Background()
	steps := []struct {
		newState repository.ReleaseRunTargetState
		buildID  string
	}{
		{repository.ReleaseRunTargetStateBuilding, buildID},
		{repository.ReleaseRunTargetStatePublishing, ""},
		{repository.ReleaseRunTargetStateRecording, ""},
		{repository.ReleaseRunTargetStateSucceeded, ""},
	}
	for _, step := range steps {
		if err := r.ReleaseRuns().UpdateTargetState(ctx, targetID, step.newState, step.buildID, ""); err != nil {
			t.Fatalf("UpdateTargetState(%s): %v", step.newState, err)
		}
	}

	_, gotTargets, err := r.ReleaseRuns().GetReleaseRun(ctx, targets[0].ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	got := gotTargets[0]
	if got.State != repository.ReleaseRunTargetStateSucceeded {
		t.Fatalf("final state = %q, want succeeded", got.State)
	}
	if got.BuildID != buildID {
		t.Fatalf("BuildID = %q, want %q (should survive transitions that don't repass it)", got.BuildID, buildID)
	}
}

func TestReleaseRunFake_UpdateTargetState_IllegalTransitionRejected(t *testing.T) {
	r := New()
	_, targets, err := createReleaseRunTx(t, r, newReleaseRun("wf-illegal"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID

	ctx := context.Background()
	err = r.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateSucceeded, "", "")
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition for queued -> succeeded, got: %v", err)
	}

	_, gotTargets, err := r.ReleaseRuns().GetReleaseRun(ctx, targets[0].ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if gotTargets[0].State != repository.ReleaseRunTargetStateQueued {
		t.Fatalf("state changed despite rejected transition: got %q, want queued", gotTargets[0].State)
	}
}

// TestReleaseRunFake_UpdateTargetState_TerminalStateRejectsFurtherTransitions
// mirrors postgres's TestReleaseRun_UpdateTargetState_TerminalStateRejectsFurtherTransitions
// -- succeeded/failed have no legal transition out, even to the other
// terminal state, and this must hold in the fake too since handler unit
// tests rely on the fake enforcing the same transition table as postgres.
func TestReleaseRunFake_UpdateTargetState_TerminalStateRejectsFurtherTransitions(t *testing.T) {
	r := New()
	_, targets, err := createReleaseRunTx(t, r, newReleaseRun("wf-terminal"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID
	ctx := context.Background()

	if err := r.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateFailed, "", "boom"); err != nil {
		t.Fatalf("queued -> failed: %v", err)
	}

	err = r.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateBuilding, "", "")
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition transitioning out of failed, got: %v", err)
	}
}

// TestReleaseRunFake_CreateReleaseRun_FieldsRoundTripThroughGet mirrors
// postgres's TestReleaseRun_CreateReleaseRun_FieldsRoundTripThroughGet --
// same NFR4 "retrievable after the fact" contract, including a non-nil
// DigestInput, must hold for the fake since handler unit tests read these
// fields back through it.
func TestReleaseRunFake_CreateReleaseRun_FieldsRoundTripThroughGet(t *testing.T) {
	r := New()

	run := newReleaseRun("wf-roundtrip")
	run.TriggeredBy = "alice@example.com"
	run.RequestedScope = "acme,widgets-domain"
	run.ResolvedPlan = []byte(`{"targets":[{"owner":"acme-widget","version":"1.2.3"}]}`)
	run.DigestInput = []byte(`{"acme-widget":"sha256:deadbeef"}`)

	created, _, err := createReleaseRunTx(t, r, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	assertReleaseRunFields := func(t *testing.T, got repository.ReleaseRun) {
		t.Helper()
		if got.TriggeredBy != run.TriggeredBy {
			t.Fatalf("TriggeredBy = %q, want %q", got.TriggeredBy, run.TriggeredBy)
		}
		if got.RequestedScope != run.RequestedScope {
			t.Fatalf("RequestedScope = %q, want %q", got.RequestedScope, run.RequestedScope)
		}
		if string(got.ResolvedPlan) != string(run.ResolvedPlan) {
			t.Fatalf("ResolvedPlan = %s, want %s", got.ResolvedPlan, run.ResolvedPlan)
		}
		if string(got.DigestInput) != string(run.DigestInput) {
			t.Fatalf("DigestInput = %s, want %s", got.DigestInput, run.DigestInput)
		}
	}
	assertReleaseRunFields(t, *created)

	fetched, _, err := r.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	assertReleaseRunFields(t, *fetched)
}

// TestReleaseRunFake_CreateReleaseRun_NilResolvedPlanStaysNil proves the
// production TriggerRelease path (issue #906, validation finding #903):
// CreateReleaseRun called with ResolvedPlan left unset (nil, as
// server/handlers/release.go's TriggerRelease does) leaves it nil on both
// the CreateReleaseRun return value and a fresh GetReleaseRun read -- not
// some other zero-value stand-in that a real Postgres NOT NULL column
// would have rejected before migration 018.
func TestReleaseRunFake_CreateReleaseRun_NilResolvedPlanStaysNil(t *testing.T) {
	r := New()
	run := newReleaseRun("wf-nil-plan")
	run.ResolvedPlan = nil

	created, _, err := createReleaseRunTx(t, r, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if created.ResolvedPlan != nil {
		t.Fatalf("expected ResolvedPlan nil immediately after CreateReleaseRun, got %s", created.ResolvedPlan)
	}

	fetched, _, err := r.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if fetched.ResolvedPlan != nil {
		t.Fatalf("expected ResolvedPlan nil after GetReleaseRun, got %s", fetched.ResolvedPlan)
	}
}

// TestReleaseRunFake_SetResolvedPlan_RoundTrip proves
// ReleaseRunRepository.SetResolvedPlan (issue #906) -- the write
// worker/release/record.go's RecordResolvedPlan activity performs -- both
// stamps the value and makes it readable back through GetReleaseRun, for a
// release run created the way TriggerRelease actually creates one (nil
// ResolvedPlan at CreateReleaseRun time).
func TestReleaseRunFake_SetResolvedPlan_RoundTrip(t *testing.T) {
	r := New()
	run := newReleaseRun("wf-set-resolved-plan")
	run.ResolvedPlan = nil

	created, _, err := createReleaseRunTx(t, r, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}

	want := []byte(`{"targets":[{"owner":"acme-widget","version":"1.2.3"}]}`)
	if err := r.ReleaseRuns().SetResolvedPlan(context.Background(), created.ReleaseRunID, want); err != nil {
		t.Fatalf("SetResolvedPlan: %v", err)
	}

	fetched, _, err := r.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if string(fetched.ResolvedPlan) != string(want) {
		t.Fatalf("ResolvedPlan = %s, want %s", fetched.ResolvedPlan, want)
	}
}

// TestReleaseRunFake_SetResolvedPlan_EmptyRejected proves the
// empty/nil-value guard documented on ReleaseRunRepository.SetResolvedPlan:
// an empty resolvedPlan is rejected with ErrInvalidArgument rather than
// silently writing NULL back over an already-set value, or a real Postgres
// implementation reproducing #903's original SQLSTATE 22P02 the other way
// around (an empty string, not NULL).
func TestReleaseRunFake_SetResolvedPlan_EmptyRejected(t *testing.T) {
	r := New()
	run := newReleaseRun("wf-set-resolved-plan-empty")
	run.ResolvedPlan = nil
	created, _, err := createReleaseRunTx(t, r, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}

	err = r.ReleaseRuns().SetResolvedPlan(context.Background(), created.ReleaseRunID, nil)
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for empty resolvedPlan, got: %v", err)
	}
}

// TestReleaseRunFake_SetResolvedPlan_UnknownReleaseRunNotFound proves an
// unknown releaseRunID is rejected with ErrNotFound, matching
// UpdateTargetState/GetReleaseRun's existing not-found contract on this
// same fake.
func TestReleaseRunFake_SetResolvedPlan_UnknownReleaseRunNotFound(t *testing.T) {
	r := New()
	err := r.ReleaseRuns().SetResolvedPlan(context.Background(), "does-not-exist", []byte(`{"a":1}`))
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// TestReleaseRunFake_SetBuildRef_RoundTrip proves
// ReleaseRunRepository.SetBuildRef (migration 023) -- the write
// worker/release/activities.go's DispatchBuild performs once its own
// GitHub.Dispatch call succeeds -- both stamps the value and makes it
// readable back through GetReleaseRun.
func TestReleaseRunFake_SetBuildRef_RoundTrip(t *testing.T) {
	r := New()
	created, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-set-build-ref"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if created.BuildRefRunID != "" || created.BuildRefRunURL != "" {
		t.Fatalf("expected empty BuildRefRunID/URL immediately after CreateReleaseRun, got %q/%q", created.BuildRefRunID, created.BuildRefRunURL)
	}

	if err := r.ReleaseRuns().SetBuildRef(context.Background(), created.ReleaseRunID, "12345", "https://example/runs/12345"); err != nil {
		t.Fatalf("SetBuildRef: %v", err)
	}

	fetched, _, err := r.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if fetched.BuildRefRunID != "12345" || fetched.BuildRefRunURL != "https://example/runs/12345" {
		t.Fatalf("BuildRefRunID/URL = %q/%q, want %q/%q", fetched.BuildRefRunID, fetched.BuildRefRunURL, "12345", "https://example/runs/12345")
	}
}

// TestReleaseRunFake_SetBuildRef_EmptyRunIDRejected proves the empty-value
// guard documented on ReleaseRunRepository.SetBuildRef.
func TestReleaseRunFake_SetBuildRef_EmptyRunIDRejected(t *testing.T) {
	r := New()
	created, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-set-build-ref-empty"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}

	err = r.ReleaseRuns().SetBuildRef(context.Background(), created.ReleaseRunID, "", "https://example/runs/1")
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for empty runID, got: %v", err)
	}
}

// TestReleaseRunFake_SetBuildRef_UnknownReleaseRunNotFound proves an
// unknown releaseRunID is rejected with ErrNotFound.
func TestReleaseRunFake_SetBuildRef_UnknownReleaseRunNotFound(t *testing.T) {
	r := New()
	err := r.ReleaseRuns().SetBuildRef(context.Background(), "does-not-exist", "1", "https://example/runs/1")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestReleaseRunFake_UpdateTargetState_UnknownTargetNotFound(t *testing.T) {
	r := New()
	err := r.ReleaseRuns().UpdateTargetState(context.Background(), "does-not-exist", repository.ReleaseRunTargetStateBuilding, "", "")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestReleaseRunFake_GetReleaseRun_NotFound(t *testing.T) {
	r := New()
	_, _, err := r.ReleaseRuns().GetReleaseRun(context.Background(), "does-not-exist")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

func TestReleaseRunFake_ListReleaseRunsByTarget_MostRecentFirstIncludingPriorAttempts(t *testing.T) {
	r := New()
	first, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-history-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	second, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-history-2"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("second CreateReleaseRun: %v", err)
	}
	if _, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-history-other"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-gadget", Kind: repository.ArtifactKindImage},
	}); err != nil {
		t.Fatalf("third CreateReleaseRun: %v", err)
	}

	runs, err := r.ReleaseRuns().ListReleaseRunsByTarget(context.Background(), "acme-widget")
	if err != nil {
		t.Fatalf("ListReleaseRunsByTarget: %v", err)
	}
	if len(runs) != 2 {
		t.Fatalf("expected 2 runs for acme-widget, got %d", len(runs))
	}
	if runs[0].ReleaseRunID != second.ReleaseRunID || runs[1].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected most-recent-first order [%s, %s], got [%s, %s]",
			second.ReleaseRunID, first.ReleaseRunID, runs[0].ReleaseRunID, runs[1].ReleaseRunID)
	}
}

// TestReleaseRunFake_ListReleaseRuns_UnscopedAndOwnerScoped mirrors
// postgres's TestReleaseRun_ListReleaseRuns_UnscopedAndOwnerScopedWithPagination
// -- unscoped (owner_full_name == "") returns every release_run across every
// owner, most-recent-first; a non-empty owner narrows to that owner's runs.
func TestReleaseRunFake_ListReleaseRuns_UnscopedAndOwnerScoped(t *testing.T) {
	r := New()
	first, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-attempts-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	second, _, err := createReleaseRunTx(t, r, newReleaseRun("wf-attempts-2"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-gadget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("second CreateReleaseRun: %v", err)
	}

	all, _, err := r.ReleaseRuns().ListReleaseRuns(context.Background(), "", 0, "")
	if err != nil {
		t.Fatalf("ListReleaseRuns(unscoped): %v", err)
	}
	if len(all) != 2 || all[0].ReleaseRunID != second.ReleaseRunID || all[1].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected unscoped most-recent-first [%s, %s], got %+v", second.ReleaseRunID, first.ReleaseRunID, all)
	}

	scoped, _, err := r.ReleaseRuns().ListReleaseRuns(context.Background(), "acme-widget", 0, "")
	if err != nil {
		t.Fatalf("ListReleaseRuns(acme-widget): %v", err)
	}
	if len(scoped) != 1 || scoped[0].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected exactly [%s] for acme-widget, got %+v", first.ReleaseRunID, scoped)
	}
}
