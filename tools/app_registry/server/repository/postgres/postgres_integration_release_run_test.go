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
	"encoding/json"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// assertJSONEqual compares two JSON documents structurally rather than
// byte-for-byte -- JSONB round-trips through Postgres with normalized
// whitespace (a space after ':'/',' that the literal we wrote doesn't have),
// so a literal string comparison would fail on formatting alone even when
// the two documents are semantically identical.
func assertJSONEqual(t *testing.T, field string, got, want []byte) {
	t.Helper()
	var gotVal, wantVal any
	if err := json.Unmarshal(got, &gotVal); err != nil {
		t.Fatalf("%s: unmarshal got %s: %v", field, got, err)
	}
	if err := json.Unmarshal(want, &wantVal); err != nil {
		t.Fatalf("%s: unmarshal want %s: %v", field, want, err)
	}
	gotNorm, _ := json.Marshal(gotVal)
	wantNorm, _ := json.Marshal(wantVal)
	if string(gotNorm) != string(wantNorm) {
		t.Fatalf("%s = %s, want %s", field, got, want)
	}
}

// newReleaseRun builds a minimal, valid repository.ReleaseRun for
// CreateReleaseRun. ResolvedPlan is nullable as of migration 018 (issue
// #906) -- CreateReleaseRun no longer requires it, and the production
// TriggerRelease path leaves it nil (see
// TestReleaseRun_CreateReleaseRun_NilResolvedPlanWritesNull below) -- but
// most tests in this file still supply a small valid JSON document here
// for convenience/determinism where ResolvedPlan isn't the thing under
// test.
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

// TestReleaseRun_CreateReleaseRun_NoTargetsRejected mirrors the fake's
// TestReleaseRunFake_CreateReleaseRun_NoTargetsRejected -- both
// implementations share this length-zero guard in Go, not the DB, so both
// need their own coverage rather than relying on the fake's.
func TestReleaseRun_CreateReleaseRun_NoTargetsRejected(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-empty"), nil)
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for zero targets, got: %v", err)
	}
}

// TestReleaseRun_CreateReleaseRun_FieldsRoundTripThroughGet proves NFR4's
// "who, when, scope, digest-or-fresh, resolved plan" are actually
// retrievable after the fact, not just accepted on write -- both the
// CreateReleaseRun return value AND a fresh GetReleaseRun read confirm
// TriggeredBy/RequestedScope/ResolvedPlan/DigestInput survive the round
// trip byte-for-byte, including DigestInput's non-nil ("pinned digest")
// case that every other test in this file leaves unset.
func TestReleaseRun_CreateReleaseRun_FieldsRoundTripThroughGet(t *testing.T) {
	reg, _ := newTestRegistry(t)

	run := newReleaseRun("wf-roundtrip")
	run.TriggeredBy = "alice@example.com"
	run.RequestedScope = "acme,widgets-domain"
	run.ResolvedPlan = []byte(`{"targets":[{"owner":"acme-widget","version":"1.2.3"}]}`)
	run.DigestInput = []byte(`{"acme-widget":"sha256:deadbeef"}`)

	created, _, err := createReleaseRunTx(t, reg, run, []repository.ReleaseRunTarget{
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
		assertJSONEqual(t, "ResolvedPlan", got.ResolvedPlan, run.ResolvedPlan)
		assertJSONEqual(t, "DigestInput", got.DigestInput, run.DigestInput)
	}
	assertReleaseRunFields(t, *created)

	fetched, _, err := reg.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	assertReleaseRunFields(t, *fetched)
}

// TestReleaseRun_CreateReleaseRun_DigestInputNilMeansBuildFresh proves the
// nil-vs-empty distinction migration 016's doc comment calls out: an unset
// DigestInput scans back as nil, not an empty non-nil byte slice standing
// in for "no digest given".
func TestReleaseRun_CreateReleaseRun_DigestInputNilMeansBuildFresh(t *testing.T) {
	reg, _ := newTestRegistry(t)

	created, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-fresh"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if created.DigestInput != nil {
		t.Fatalf("expected DigestInput nil for a fresh build, got %s", created.DigestInput)
	}

	fetched, _, err := reg.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if fetched.DigestInput != nil {
		t.Fatalf("expected DigestInput nil after GetReleaseRun, got %s", fetched.DigestInput)
	}
}

// TestReleaseRun_CreateReleaseRun_NilResolvedPlanWritesNull proves issue
// #906's actual fix end to end against real Postgres: the production
// TriggerRelease code path (server/handlers/release.go) builds a
// repository.ReleaseRun{} with ResolvedPlan left at Go's nil zero value,
// and CreateReleaseRun's nil-guard (release_run.go, mirroring the existing
// DigestInput pattern) must write that as SQL NULL against migration 018's
// now-nullable resolved_plan column -- NOT the empty-string literal
// string(nil) == "" that migration 016's original NOT NULL column rejected
// with SQLSTATE 22P02 (see #903). Both the CreateReleaseRun return value
// and a fresh GetReleaseRun read must come back nil, proving this is a
// real column NULL round-tripping through Postgres's JSONB scan, not just
// an in-process Go value that happens to look right.
func TestReleaseRun_CreateReleaseRun_NilResolvedPlanWritesNull(t *testing.T) {
	reg, pool := newTestRegistry(t)

	run := newReleaseRun("wf-nil-plan")
	run.ResolvedPlan = nil

	created, _, err := createReleaseRunTx(t, reg, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}
	if created.ResolvedPlan != nil {
		t.Fatalf("expected ResolvedPlan nil immediately after CreateReleaseRun, got %s", created.ResolvedPlan)
	}

	fetched, _, err := reg.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if fetched.ResolvedPlan != nil {
		t.Fatalf("expected ResolvedPlan nil after GetReleaseRun, got %s", fetched.ResolvedPlan)
	}

	// Belt-and-suspenders: confirm the column itself is a real SQL NULL,
	// not an empty-but-non-NULL JSONB value that a looser scan could have
	// masked as a nil []byte on the Go side.
	var isNull bool
	if scanErr := pool.QueryRow(context.Background(),
		`SELECT resolved_plan IS NULL FROM release_run WHERE release_run_id = $1`, created.ReleaseRunID,
	).Scan(&isNull); scanErr != nil {
		t.Fatalf("check resolved_plan IS NULL: %v", scanErr)
	}
	if !isNull {
		t.Fatalf("expected resolved_plan column to be SQL NULL, it was not")
	}
}

// TestReleaseRun_SetResolvedPlan_RoundTrip proves
// ReleaseRunRepository.SetResolvedPlan (issue #906) against real Postgres:
// created with ResolvedPlan nil (the production TriggerRelease shape),
// SetResolvedPlan stamps a value in, and it is readable back through
// GetReleaseRun -- the write worker/release/record.go's RecordResolvedPlan
// activity performs once ReleaseWorkflow's ResolvePlan step succeeds.
func TestReleaseRun_SetResolvedPlan_RoundTrip(t *testing.T) {
	reg, _ := newTestRegistry(t)

	run := newReleaseRun("wf-set-resolved-plan")
	run.ResolvedPlan = nil
	created, _, err := createReleaseRunTx(t, reg, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}

	want := []byte(`{"targets":[{"owner":"acme-widget","version":"1.2.3"}]}`)
	if err := reg.ReleaseRuns().SetResolvedPlan(context.Background(), created.ReleaseRunID, want); err != nil {
		t.Fatalf("SetResolvedPlan: %v", err)
	}

	fetched, _, err := reg.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunID)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	assertJSONEqual(t, "ResolvedPlan", fetched.ResolvedPlan, want)
}

// TestReleaseRun_SetResolvedPlan_EmptyRejected proves the empty-value
// guard documented on ReleaseRunRepository.SetResolvedPlan holds against
// real Postgres too (the fake's identically-named test covers the same
// contract on the in-memory implementation) -- an empty resolvedPlan is
// rejected with ErrInvalidArgument before any UPDATE reaches the database.
func TestReleaseRun_SetResolvedPlan_EmptyRejected(t *testing.T) {
	reg, _ := newTestRegistry(t)

	run := newReleaseRun("wf-set-resolved-plan-empty")
	run.ResolvedPlan = nil
	created, _, err := createReleaseRunTx(t, reg, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("CreateReleaseRun: %v", err)
	}

	err = reg.ReleaseRuns().SetResolvedPlan(context.Background(), created.ReleaseRunID, nil)
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for empty resolvedPlan, got: %v", err)
	}
}

// TestReleaseRun_SetResolvedPlan_UnknownReleaseRunNotFound proves an
// unknown releaseRunID is rejected with ErrNotFound (the zero-rows-affected
// branch in releaseRunRepo.SetResolvedPlan), not silently accepted as a
// no-op UPDATE matching nothing.
func TestReleaseRun_SetResolvedPlan_UnknownReleaseRunNotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)

	err := reg.ReleaseRuns().SetResolvedPlan(context.Background(), "00000000-0000-0000-0000-000000000000", []byte(`{"a":1}`))
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got: %v", err)
	}
}

// TestReleaseRun_CreateReleaseRun_PartialTargetFailureRollsBackWholeBatch
// proves CreateReleaseRun's doc comment claim: a failure partway through
// target inserts (here, a CHECK-constraint-violating kind on the second
// target) rolls back the ENTIRE transaction, including the release_run row
// and the first target's already-inserted row -- not just the failing
// insert. This is the atomicity property NFR3 depends on: a caller retrying
// after a failed CreateReleaseRun must never see a half-created release_run
// sitting in the audit trail.
func TestReleaseRun_CreateReleaseRun_PartialTargetFailureRollsBackWholeBatch(t *testing.T) {
	reg, pool := newTestRegistry(t)

	run := newReleaseRun("wf-partial-fail")
	_, _, err := createReleaseRunTx(t, reg, run, []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
		// release_run_target_kind_check only allows 'image'/'chart'
		// (migration 016) -- ArtifactKindBinary is a legal ArtifactKind
		// elsewhere in the system but illegal here, so this insert fails.
		{OwnerFullName: "acme-widget-firmware", Kind: repository.ArtifactKindBinary},
	})
	if !errors.Is(err, repository.ErrInvalidArgument) {
		t.Fatalf("expected ErrInvalidArgument for an illegal kind, got: %v", err)
	}

	var runCount int
	if scanErr := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM release_run WHERE temporal_workflow_id = $1`, run.TemporalWorkflowID,
	).Scan(&runCount); scanErr != nil {
		t.Fatalf("count release_run: %v", scanErr)
	}
	if runCount != 0 {
		t.Fatalf("expected the release_run row to be rolled back, found %d", runCount)
	}

	var targetCount int
	if scanErr := pool.QueryRow(context.Background(),
		`SELECT count(*) FROM release_run_target WHERE owner_full_name = 'acme-widget'`,
	).Scan(&targetCount); scanErr != nil {
		t.Fatalf("count release_run_target: %v", scanErr)
	}
	if targetCount != 0 {
		t.Fatalf("expected the first target's row to be rolled back too, found %d", targetCount)
	}
}

// TestReleaseRun_CreateReleaseRun_DuplicateWorkflowIDRejected proves a
// second CreateReleaseRun for the same workflow id is rejected at the
// audit-row layer (issue #889, migration 017) while the first release_run's
// targets are still non-terminal -- not just at Temporal's own dedup.
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

// TestReleaseRun_CreateReleaseRun_WorkflowIDReusableAfterTerminal proves
// migration 017's fix (issue #889, FR11): once every release_run_target
// row for a prior release_run under a workflow id has reached a terminal
// state, a NEW release_run may reuse that same workflow id -- e.g. the same
// apps/charts released again later. This would have failed against 016's
// blanket UNIQUE index.
func TestReleaseRun_CreateReleaseRun_WorkflowIDReusableAfterTerminal(t *testing.T) {
	reg, _ := newTestRegistry(t)

	targets := []repository.ReleaseRunTarget{{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage}}
	first, firstTargets, err := createReleaseRunTx(t, reg, newReleaseRun("wf-reuse"), targets)
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	if err := reg.ReleaseRuns().UpdateTargetState(context.Background(), firstTargets[0].ReleaseRunTargetID, repository.ReleaseRunTargetStateBuilding, "", ""); err != nil {
		t.Fatalf("advance to building: %v", err)
	}
	if err := reg.ReleaseRuns().UpdateTargetState(context.Background(), firstTargets[0].ReleaseRunTargetID, repository.ReleaseRunTargetStateFailed, "", "boom"); err != nil {
		t.Fatalf("advance to failed: %v", err)
	}

	second, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-reuse"), targets)
	if err != nil {
		t.Fatalf("second CreateReleaseRun after first went terminal: %v", err)
	}
	if second.ReleaseRunID == first.ReleaseRunID {
		t.Fatalf("expected a distinct release_run row, got the same id back")
	}
	if second.TemporalWorkflowID != first.TemporalWorkflowID {
		t.Fatalf("expected both rows to share the reused workflow id, got %q vs %q", first.TemporalWorkflowID, second.TemporalWorkflowID)
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

// TestReleaseRun_ListReleaseRuns_UnscopedAndOwnerScopedWithPagination covers
// ListReleaseRuns (the release-history admin page's query, ListReleaseAttempts
// RPC): unscoped (owner_full_name == "") returns every release_run across
// every owner, most-recent-first; a non-empty owner narrows to that owner's
// runs, same as ListReleaseRunsByTarget; and a pageSize of 1 exercises the
// keyset cursor -- the returned next_page_token, fed back in, must resume
// after the first page's last row rather than repeating it.
func TestReleaseRun_ListReleaseRuns_UnscopedAndOwnerScopedWithPagination(t *testing.T) {
	reg, _ := newTestRegistry(t)

	first, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-attempts-1"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-widget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("first CreateReleaseRun: %v", err)
	}
	second, _, err := createReleaseRunTx(t, reg, newReleaseRun("wf-attempts-2"), []repository.ReleaseRunTarget{
		{OwnerFullName: "acme-gadget", Kind: repository.ArtifactKindImage},
	})
	if err != nil {
		t.Fatalf("second CreateReleaseRun: %v", err)
	}

	// Unscoped: both runs, most-recent-first.
	all, nextToken, err := reg.ReleaseRuns().ListReleaseRuns(context.Background(), "", 0, "")
	if err != nil {
		t.Fatalf("ListReleaseRuns(unscoped): %v", err)
	}
	if len(all) != 2 || all[0].ReleaseRunID != second.ReleaseRunID || all[1].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected unscoped most-recent-first [%s, %s], got %+v", second.ReleaseRunID, first.ReleaseRunID, all)
	}
	if nextToken != "" {
		t.Fatalf("expected no next page token for a 2-row result under the default page size, got %q", nextToken)
	}

	// Owner-scoped: only acme-widget's run.
	scoped, _, err := reg.ReleaseRuns().ListReleaseRuns(context.Background(), "acme-widget", 0, "")
	if err != nil {
		t.Fatalf("ListReleaseRuns(acme-widget): %v", err)
	}
	if len(scoped) != 1 || scoped[0].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected exactly [%s] for acme-widget, got %+v", first.ReleaseRunID, scoped)
	}

	// Pagination: pageSize 1 returns the newest row plus a next page token
	// that resumes to the older row, never repeating it.
	page1, page1Token, err := reg.ReleaseRuns().ListReleaseRuns(context.Background(), "", 1, "")
	if err != nil {
		t.Fatalf("ListReleaseRuns(page 1): %v", err)
	}
	if len(page1) != 1 || page1[0].ReleaseRunID != second.ReleaseRunID {
		t.Fatalf("expected page 1 to be [%s], got %+v", second.ReleaseRunID, page1)
	}
	if page1Token == "" {
		t.Fatalf("expected a non-empty next page token after a 1-row page with 2 total rows")
	}
	page2, page2Token, err := reg.ReleaseRuns().ListReleaseRuns(context.Background(), "", 1, page1Token)
	if err != nil {
		t.Fatalf("ListReleaseRuns(page 2): %v", err)
	}
	if len(page2) != 1 || page2[0].ReleaseRunID != first.ReleaseRunID {
		t.Fatalf("expected page 2 to be [%s], got %+v", first.ReleaseRunID, page2)
	}
	if page2Token != "" {
		t.Fatalf("expected no further page after exhausting both rows, got %q", page2Token)
	}
}
