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
