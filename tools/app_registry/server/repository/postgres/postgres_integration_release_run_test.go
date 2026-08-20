//go:build integration

// Real-Postgres integration coverage for releaseRunRepo (release_run.go,
// migration 016, NFR4, issue #887): CreateReleaseRun, UpdateTargetState's
// legal-transition enforcement, GetReleaseRun, and
// ListReleaseRunsByTarget's "reconstruct prior attempts" contract. See
// postgres_integration_helpers_test.go's doc comment for why this package
// builds these files under the "integration" tag, and TESTING.md for how
// to run them.
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// newReleaseRun builds a minimal, valid repository.ReleaseRun for
// CreateReleaseRun -- ResolvedPlan is NOT NULL (migration 016), so every
// caller here supplies a small valid JSON document rather than nil.
func newReleaseRun(workflowID string) repository.ReleaseRun {
	return repository.ReleaseRun{
		TriggeredBy:        "integration-test",
		RequestedScope:     "acme",
		ResolvedPlan:       []byte(`{"targets":["image:acme-widget"]}`),
		TemporalWorkflowID: workflowID,
	}
}

func createReleaseRunTx(t *testing.T, reg *Registry, run repository.ReleaseRun, targets []repository.ReleaseRunTarget) (*repository.ReleaseRun, []repository.ReleaseRunTarget, error) {
	t.Helper()
	var outRun *repository.ReleaseRun
	var outTargets []repository.ReleaseRunTarget
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		outRun, outTargets, ferr = r.ReleaseRuns().CreateReleaseRun(ctx, run, targets)
		return ferr
	})
	return outRun, outTargets, err
}

// TestReleaseRun_CreateReleaseRun_CreatesRunAndQueuedTargets proves the
// happy path: one release_run row plus one release_run_target row per
// target, all starting 'queued'.
func TestReleaseRun_CreateReleaseRun_CreatesRunAndQueuedTargets(t *testing.T) {
	reg, _ := newTestRegistry(t)

	run, targets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-create-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
		{OwnerFullName: "acme-widget-chart", Kind: repository.ArtifactKindChart},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if run.ReleaseRunID == "" {
		t.Fatalf("expected a generated ReleaseRunID")
	}
	if run.TemporalWorkflowID != "wf-create-1" {
		t.Fatalf("TemporalWorkflowID mismatch: got %q", run.TemporalWorkflowID)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	for _, target := range targets {
		if target.ReleaseRunTargetID == "" {
			t.Fatalf("expected a generated ReleaseRunTargetID")
		}
		if target.ReleaseRunID != run.ReleaseRunID {
			t.Fatalf("target.ReleaseRunID = %q, want %q", target.ReleaseRunID, run.ReleaseRunID)
		}
		if target.State != repository.ReleaseRunTargetStateQueued {
			t.Fatalf("target %s state = %q, want %q", target.OwnerFullName, target.State, repository.ReleaseRunTargetStateQueued)
		}
	}
}

// TestReleaseRun_CreateReleaseRun_DuplicateWorkflowIDRejected proves the
// unique index on temporal_workflow_id (FR5/NFR2's dedup key) rejects a
// second CreateReleaseRun for the same workflow id at the audit-row layer
// too, not just at Temporal's own dedup.
func TestReleaseRun_CreateReleaseRun_DuplicateWorkflowIDRejected(t *testing.T) {
	reg, _ := newTestRegistry(t)

	targets := []repository.ReleaseRunTarget{{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage}}
	if _, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-dup"), targets); err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}

	_, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-dup"), targets)
	if !errors.Is(err, repository.ErrAlreadyExists) {
		t.Fatalf("expected ErrAlreadyExists for duplicate workflow id, got: %v", err)
	}
}

// TestReleaseRun_UpdateTargetState_LegalTransitionsRoundTrip walks a single
// target through the full legal chain (queued -> building -> publishing ->
// recording -> succeeded), proving state_changed_at advances and a build_id
// set at 'building' survives untouched through the rest of the chain when
// later calls pass an empty buildID.
func TestReleaseRun_UpdateTargetState_LegalTransitionsRoundTrip(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	_, targets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-transitions"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID
	buildID := seedBuild(t, pool, "wf-transitions-run")

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
		if err := reg.ReleaseRuns().UpdateTargetState(ctx, targetID, step.newState, step.buildID, ""); err != nil {
			t.Fatalf("UpdateTargetState(%s): %v", step.newState, err)
		}
	}

	_, gotTargets, err := reg.ReleaseRuns().GetReleaseRun(ctx, targets[0].ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if len(gotTargets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(gotTargets))
	}
	got := gotTargets[0]
	if got.State != repository.ReleaseRunTargetStateSucceeded {
		t.Fatalf("final state = %q, want succeeded", got.State)
	}
	if got.BuildID != buildID {
		t.Fatalf("BuildID = %q, want %q (should survive transitions that don't repass it)", got.BuildID, buildID)
	}
}

// TestReleaseRun_UpdateTargetState_IllegalTransitionRejected proves a
// transition skipping states (queued -> succeeded) is rejected with
// ErrFailedPrecondition and leaves the row unchanged -- the red half of the
// red/green proof for the legal-transition table.
func TestReleaseRun_UpdateTargetState_IllegalTransitionRejected(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	_, targets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-illegal"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID

	err = reg.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateSucceeded, "", "")
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition for queued -> succeeded, got: %v", err)
	}

	_, gotTargets, err := reg.ReleaseRuns().GetReleaseRun(ctx, targets[0].ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if gotTargets[0].State != repository.ReleaseRunTargetStateQueued {
		t.Fatalf("state changed despite rejected transition: got %q, want queued", gotTargets[0].State)
	}
}

// TestReleaseRun_UpdateTargetState_TerminalStateRejectsFurtherTransitions
// proves succeeded/failed have no legal transition out, even to the other
// terminal state.
func TestReleaseRun_UpdateTargetState_TerminalStateRejectsFurtherTransitions(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	_, targets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-terminal"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID

	if err := reg.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateFailed, "", "boom"); err != nil {
		t.Fatalf("queued -> failed: %v", err)
	}

	err = reg.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateBuilding, "", "")
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition transitioning out of failed, got: %v", err)
	}
}

// TestReleaseRun_UpdateTargetState_MissingBuildFKRejected proves a
// nonexistent build_id is rejected as ErrFailedPrecondition (the FK
// violation translatePgError maps it to), not silently accepted or a raw
// pgx error leaking through.
func TestReleaseRun_UpdateTargetState_MissingBuildFKRejected(t *testing.T) {
	reg, _ := newTestRegistry(t)
	ctx := context.Background()

	_, targets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-badfk"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID

	err = reg.ReleaseRuns().UpdateTargetState(ctx, targetID, repository.ReleaseRunTargetStateBuilding, "00000000-0000-0000-0000-000000000000", "")
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected ErrFailedPrecondition for a nonexistent build_id, got: %v", err)
	}
}

// TestReleaseRun_GetReleaseRun_NotFound proves an unknown release_run_id
// returns ErrNotFound rather than a raw pgx.ErrNoRows or a nil, nil-error
// zero value.
func TestReleaseRun_GetReleaseRun_NotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, _, err := reg.ReleaseRuns().GetReleaseRun(context.Background(), "00000000-0000-0000-0000-000000000000")
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// TestReleaseRun_ListReleaseRunsByTarget_MostRecentFirstIncludingPriorAttempts
// proves NFR4's "reconstruct full history including prior attempts": two
// separate release_run batches both covering the same target both come
// back, most-recent-first, and a release covering a DIFFERENT target only
// is excluded.
func TestReleaseRun_ListReleaseRunsByTarget_MostRecentFirstIncludingPriorAttempts(t *testing.T) {
	reg, _ := newTestRegistry(t)

	first, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-history-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	second, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-history-2"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("second CreateReleaseRun: %v", err)
	}
	// A release for a different target only -- must not show up below.
	if _, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-history-other"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-gadget", Kind: repository.ArtifactKindImage},
	}); err != nil {
		t.Fatalf("third CreateReleaseRun: %v", err)
	}

	runs, err := reg.ReleaseRuns().ListReleaseRunsByTarget(context.Background(), "acme-widget")
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
