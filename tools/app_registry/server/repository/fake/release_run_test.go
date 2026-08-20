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
