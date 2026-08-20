package handlers

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"google.golang.org/grpc/codes"
)

func newReleaseFixture() (*ReleaseServer, repository.Registry) {
	repo := fake.New()
	return NewReleaseServer(repo), repo
}

func triggerReq(scope string, targets ...*pb.ReleaseTargetInput) *pb.TriggerReleaseRequest {
	return &pb.TriggerReleaseRequest{RequestedScope: scope, Targets: targets}
}

func target(owner string, kind pb.ArtifactKind, digest string) *pb.ReleaseTargetInput {
	return &pb.ReleaseTargetInput{OwnerFullName: owner, Kind: kind, Digest: digest}
}

// TestTriggerRelease_Authorization mirrors authz_test.go's pattern:
// TriggerRelease requires exactly the promoter-dev role Promote already
// requires (FR4/NFR5) -- no new role.
func TestTriggerRelease_Authorization(t *testing.T) {
	req := triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))

	t.Run("correct role allowed", func(t *testing.T) {
		srv, _ := newReleaseFixture()
		if _, err := srv.TriggerRelease(ctxWithRoles(auth.RolePromoterDev), req); err != nil {
			t.Fatalf("expected promoter-dev to be allowed, got %v", err)
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		srv, _ := newReleaseFixture()
		_, err := srv.TriggerRelease(ctxWithRoles(auth.RoleBuilder), req)
		requireCode(t, err, codes.PermissionDenied, "TriggerRelease")
	})

	t.Run("no claims is Unauthenticated", func(t *testing.T) {
		srv, _ := newReleaseFixture()
		_, err := srv.TriggerRelease(context.Background(), req)
		requireCode(t, err, codes.Unauthenticated, "TriggerRelease")
	})
}

// TestTriggerRelease_Success confirms FR3 (both IMAGE and CHART targets
// accepted through the same RPC) and FR2 (digest input persisted, non-empty
// digest recorded, build-fresh targets leave no digest entry).
func TestTriggerRelease_Success(t *testing.T) {
	srv, repo := newReleaseFixture()

	resp, err := srv.TriggerRelease(authedCtx(), triggerReq("demo",
		target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "sha256:pinned"),
		target("demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, ""),
	))
	if err != nil {
		t.Fatalf("TriggerRelease: %v", err)
	}
	if resp.ReleaseRunId == "" {
		t.Fatalf("expected a generated release_run_id")
	}
	if resp.TemporalWorkflowId == "" {
		t.Fatalf("expected a generated temporal_workflow_id")
	}

	run, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), resp.ReleaseRunId)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if run.RequestedScope != "demo" {
		t.Fatalf("expected requested_scope %q, got %q", "demo", run.RequestedScope)
	}
	if run.TriggeredBy != "test-user" {
		t.Fatalf("expected triggered_by %q, got %q", "test-user", run.TriggeredBy)
	}
	if len(targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(targets))
	}
	for _, tg := range targets {
		if tg.State != repository.ReleaseRunTargetStateQueued {
			t.Fatalf("target %s: expected state queued, got %s", tg.OwnerFullName, tg.State)
		}
	}

	var digests map[string]string
	if err := json.Unmarshal(run.DigestInput, &digests); err != nil {
		t.Fatalf("unmarshal digest_input: %v", err)
	}
	if digests["demo-svc"] != "sha256:pinned" {
		t.Fatalf("expected demo-svc digest sha256:pinned, got %q", digests["demo-svc"])
	}
	if _, ok := digests["demo-achart"]; ok {
		t.Fatalf("expected no digest entry for build-fresh target demo-achart")
	}
}

// TestTriggerRelease_BuildFresh_NoDigestInput confirms FR2: when no target
// pins a digest, DigestInput is nil (build fresh), not an empty map.
func TestTriggerRelease_BuildFresh_NoDigestInput(t *testing.T) {
	srv, repo := newReleaseFixture()

	resp, err := srv.TriggerRelease(authedCtx(), triggerReq("demo",
		target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""),
	))
	if err != nil {
		t.Fatalf("TriggerRelease: %v", err)
	}
	run, _, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), resp.ReleaseRunId)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if run.DigestInput != nil {
		t.Fatalf("expected nil digest_input for an all-build-fresh batch, got %q", run.DigestInput)
	}
}

// TestTriggerRelease_AlreadyReleasing_Rejected covers FR5: triggering a
// target that already has a non-terminal release_run_target row is
// rejected with a distinct, identifiable FailedPrecondition (not a generic
// error, not silent double-queueing).
func TestTriggerRelease_AlreadyReleasing_Rejected(t *testing.T) {
	srv, _ := newReleaseFixture()

	if _, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))); err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}

	_, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	requireCode(t, err, codes.FailedPrecondition, "TriggerRelease (already releasing)")
}

// TestTriggerRelease_AlreadyReleasing_DifferentKindAllowed confirms the
// in-flight check is scoped to owner+kind, not owner alone -- an IMAGE
// release for an owner does not block a CHART release of the same
// owner_full_name.
func TestTriggerRelease_AlreadyReleasing_DifferentKindAllowed(t *testing.T) {
	srv, _ := newReleaseFixture()

	if _, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-thing", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))); err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}
	if _, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-thing", pb.ArtifactKind_ARTIFACT_KIND_CHART, ""))); err != nil {
		t.Fatalf("second TriggerRelease (different kind): expected success, got %v", err)
	}
}

// TestTriggerRelease_AlreadyReleasing_AfterTerminal_Allowed confirms a
// terminal (succeeded/failed) prior release_run_target does not block a new
// trigger for the same owner+kind -- only non-terminal state does.
func TestTriggerRelease_AlreadyReleasing_AfterTerminal_Allowed(t *testing.T) {
	srv, repo := newReleaseFixture()

	first, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}
	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), first.ReleaseRunId)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID
	for _, step := range []repository.ReleaseRunTargetState{
		repository.ReleaseRunTargetStateBuilding,
		repository.ReleaseRunTargetStatePublishing,
		repository.ReleaseRunTargetStateRecording,
		repository.ReleaseRunTargetStateSucceeded,
	} {
		if err := repo.ReleaseRuns().UpdateTargetState(context.Background(), targetID, step, "", ""); err != nil {
			t.Fatalf("UpdateTargetState(%s): %v", step, err)
		}
	}

	if _, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))); err != nil {
		t.Fatalf("second TriggerRelease after terminal state: expected success, got %v", err)
	}
}

// TestTriggerRelease_InvalidArgument covers the request-shape validation
// this handler owns (targets required, owner_full_name required, kind
// restricted to IMAGE/CHART -- see ReleaseRunTarget's doc comment).
func TestTriggerRelease_InvalidArgument(t *testing.T) {
	cases := []struct {
		name string
		req  *pb.TriggerReleaseRequest
	}{
		{"no targets", triggerReq("demo")},
		{"missing owner", triggerReq("demo", target("", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))},
		{"unsupported kind", triggerReq("demo", target("demo-fw", pb.ArtifactKind_ARTIFACT_KIND_FIRMWARE, ""))},
		{"unspecified kind", triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_UNSPECIFIED, ""))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newReleaseFixture()
			_, err := srv.TriggerRelease(authedCtx(), tc.req)
			requireCode(t, err, codes.InvalidArgument, "TriggerRelease")
		})
	}
}

// TestTriggerRelease_DuplicateTargetInBatch_Rejected confirms FR5's "at
// most one non-terminal release per target" can't be violated by a single
// batch request naming the same owner+kind twice -- rejectIfAlreadyReleasing
// alone can't catch this (neither occurrence is persisted when the other is
// checked), so this must be validated up front against the request itself.
func TestTriggerRelease_DuplicateTargetInBatch_Rejected(t *testing.T) {
	srv, repo := newReleaseFixture()

	_, err := srv.TriggerRelease(authedCtx(), triggerReq("demo",
		target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""),
		target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""),
	))
	requireCode(t, err, codes.InvalidArgument, "TriggerRelease (duplicate target in batch)")

	runs, lerr := repo.ReleaseRuns().ListReleaseRunsByTarget(context.Background(), "demo-svc")
	if lerr != nil {
		t.Fatalf("ListReleaseRunsByTarget: %v", lerr)
	}
	if len(runs) != 0 {
		t.Fatalf("expected no release_run to be created for a rejected batch, got %d", len(runs))
	}
}

// TestGetRelease_ReadsBack confirms FR10: GetRelease returns real status
// data straight from the repository, including per-target state.
func TestGetRelease_ReadsBack(t *testing.T) {
	srv, repo := newReleaseFixture()

	created, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("TriggerRelease: %v", err)
	}
	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), created.ReleaseRunId)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	if err := repo.ReleaseRuns().UpdateTargetState(context.Background(), targets[0].ReleaseRunTargetID, repository.ReleaseRunTargetStateBuilding, "build-1", ""); err != nil {
		t.Fatalf("UpdateTargetState: %v", err)
	}

	got, err := srv.GetRelease(context.Background(), &pb.GetReleaseRequest{ReleaseRunId: created.ReleaseRunId})
	if err != nil {
		t.Fatalf("GetRelease: %v", err)
	}
	if got.ReleaseRunId != created.ReleaseRunId {
		t.Fatalf("expected release_run_id %q, got %q", created.ReleaseRunId, got.ReleaseRunId)
	}
	if len(got.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(got.Targets))
	}
	if got.Targets[0].State != pb.ReleaseRunTargetState_RELEASE_RUN_TARGET_STATE_BUILDING {
		t.Fatalf("expected state BUILDING, got %v", got.Targets[0].State)
	}
	if got.Targets[0].BuildId != "build-1" {
		t.Fatalf("expected build_id build-1, got %q", got.Targets[0].BuildId)
	}
}

// TestGetRelease_NotFound confirms a missing release_run_id maps to
// codes.NotFound (via mapRepoErr).
func TestGetRelease_NotFound(t *testing.T) {
	srv, _ := newReleaseFixture()
	_, err := srv.GetRelease(context.Background(), &pb.GetReleaseRequest{ReleaseRunId: "does-not-exist"})
	requireCode(t, err, codes.NotFound, "GetRelease")
}

// TestListReleases_FullHistory covers NFR4: ListReleases reconstructs the
// full history of attempts against a target, not just the current
// in-flight (or most recent) one.
func TestListReleases_FullHistory(t *testing.T) {
	srv, repo := newReleaseFixture()

	first, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}
	_, targets, err := repo.ReleaseRuns().GetReleaseRun(context.Background(), first.ReleaseRunId)
	if err != nil {
		t.Fatalf("GetReleaseRun: %v", err)
	}
	targetID := targets[0].ReleaseRunTargetID
	for _, step := range []repository.ReleaseRunTargetState{
		repository.ReleaseRunTargetStateBuilding,
		repository.ReleaseRunTargetStatePublishing,
		repository.ReleaseRunTargetStateRecording,
		repository.ReleaseRunTargetStateFailed,
	} {
		if err := repo.ReleaseRuns().UpdateTargetState(context.Background(), targetID, step, "", "boom"); err != nil {
			t.Fatalf("UpdateTargetState(%s): %v", step, err)
		}
	}

	second, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("second TriggerRelease: %v", err)
	}

	resp, err := srv.ListReleases(context.Background(), &pb.ListReleasesRequest{OwnerFullName: "demo-svc"})
	if err != nil {
		t.Fatalf("ListReleases: %v", err)
	}
	if len(resp.Releases) != 2 {
		t.Fatalf("expected 2 releases in history, got %d", len(resp.Releases))
	}
	ids := map[string]bool{}
	for _, r := range resp.Releases {
		ids[r.ReleaseRunId] = true
	}
	if !ids[first.ReleaseRunId] || !ids[second.ReleaseRunId] {
		t.Fatalf("expected both release runs in history, got %v", ids)
	}
}

// TestListReleases_InvalidArgument confirms owner_full_name is required.
func TestListReleases_InvalidArgument(t *testing.T) {
	srv, _ := newReleaseFixture()
	_, err := srv.ListReleases(context.Background(), &pb.ListReleasesRequest{})
	requireCode(t, err, codes.InvalidArgument, "ListReleases")
}
