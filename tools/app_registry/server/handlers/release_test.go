package handlers

import (
	"context"
	"encoding/json"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/auth"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	"github.com/whale-net/everything/tools/app_registry/worker/release"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"google.golang.org/grpc/codes"
)

// newReleaseFixture builds a ReleaseServer with no Temporal client (nil) --
// see ReleaseServer.temporal's doc comment: TriggerRelease treats a nil
// client as "skip starting the workflow" so every test in this file that
// only cares about the repository-write half of TriggerRelease (dedup
// rejection, persisted rows, etc.) doesn't need a real or fake Temporal
// server.
func newReleaseFixture() (*ReleaseServer, repository.Registry) {
	repo := fake.New()
	return NewReleaseServer(repo, nil), repo
}

func triggerReq(scope string, targets ...*pb.ReleaseTargetInput) *pb.TriggerReleaseRequest {
	return &pb.TriggerReleaseRequest{RequestedScope: scope, Targets: targets}
}

func target(owner string, kind pb.ArtifactKind, digest string) *pb.ReleaseTargetInput {
	return &pb.ReleaseTargetInput{OwnerFullName: owner, Kind: kind, Digest: digest}
}

// targetWithVersion mirrors target but also sets version_selection --
// issue #889 follow-up's per-target Draft-page picker input. A distinct
// helper rather than adding a parameter to target() keeps every existing
// call site (which never cares about version_selection) unchanged.
func targetWithVersion(owner string, kind pb.ArtifactKind, versionSelection string) *pb.ReleaseTargetInput {
	return &pb.ReleaseTargetInput{OwnerFullName: owner, Kind: kind, VersionSelection: versionSelection}
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
		{"garbage version_selection", triggerReq("demo", targetWithVersion("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "not-a-version"))},
		{"version_selection missing v prefix", triggerReq("demo", targetWithVersion("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "1.2.3"))},
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

// TestTriggerRelease_VersionSelection_ValidValuesAccepted proves every
// legal version_selection shape (issue #889 follow-up: the release-trigger
// UI's per-target Draft-page picker) -- a bump keyword, a hardcoded
// version, and the empty default -- is accepted, not just rejected on
// garbage input (TestTriggerRelease_InvalidArgument covers that side).
func TestTriggerRelease_VersionSelection_ValidValuesAccepted(t *testing.T) {
	cases := []struct {
		name             string
		versionSelection string
	}{
		{"empty (default patch-bump)", ""},
		{"major", "major"},
		{"minor", "minor"},
		{"patch", "patch"},
		{"explicit version", "v3.4.5"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, _ := newReleaseFixture()
			_, err := srv.TriggerRelease(authedCtx(), triggerReq("demo",
				targetWithVersion("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, tc.versionSelection),
			))
			if err != nil {
				t.Fatalf("TriggerRelease with version_selection %q: %v", tc.versionSelection, err)
			}
		})
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

// TestListReleaseAttempts_UnscopedAcrossOwners covers the release-history
// admin page's query: unlike ListReleases, owner_full_name is optional --
// an empty request lists every owner's release attempts, most-recent-first.
func TestListReleaseAttempts_UnscopedAcrossOwners(t *testing.T) {
	srv, _ := newReleaseFixture()

	first, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}
	second, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-other", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("second TriggerRelease: %v", err)
	}

	resp, err := srv.ListReleaseAttempts(context.Background(), &pb.ListReleaseAttemptsRequest{})
	if err != nil {
		t.Fatalf("ListReleaseAttempts: %v", err)
	}
	if len(resp.Releases) != 2 {
		t.Fatalf("expected 2 release attempts across both owners, got %d", len(resp.Releases))
	}
	ids := map[string]bool{}
	for _, r := range resp.Releases {
		ids[r.ReleaseRunId] = true
	}
	if !ids[first.ReleaseRunId] || !ids[second.ReleaseRunId] {
		t.Fatalf("expected both release runs, got %v", ids)
	}
	if resp.Page == nil {
		t.Fatalf("expected a non-nil Page in the response")
	}
}

// TestListReleaseAttempts_OwnerFilter confirms owner_full_name, when set,
// narrows the result the same way ListReleases does.
func TestListReleaseAttempts_OwnerFilter(t *testing.T) {
	srv, _ := newReleaseFixture()

	first, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-svc", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "")))
	if err != nil {
		t.Fatalf("first TriggerRelease: %v", err)
	}
	if _, err := srv.TriggerRelease(authedCtx(), triggerReq("demo", target("demo-other", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, ""))); err != nil {
		t.Fatalf("second TriggerRelease: %v", err)
	}

	resp, err := srv.ListReleaseAttempts(context.Background(), &pb.ListReleaseAttemptsRequest{OwnerFullName: "demo-svc"})
	if err != nil {
		t.Fatalf("ListReleaseAttempts(demo-svc): %v", err)
	}
	if len(resp.Releases) != 1 || resp.Releases[0].ReleaseRunId != first.ReleaseRunId {
		t.Fatalf("expected exactly [%s] for demo-svc, got %+v", first.ReleaseRunId, resp.Releases)
	}
}

// fakeTemporalSignal captures one SignalWorkflow call's identity payload.
type fakeTemporalSignal struct {
	WorkflowID string
	RunID      string
	SignalName string
	Arg        interface{}
}

// fakeTemporal is a client.Client whose only implemented method is
// SignalWorkflow -- everything else (ExecuteWorkflow, CancelWorkflow, ...)
// would call through the embedded nil interface and panic, which is the
// point: NotifyBuildComplete must not call anything else, and any test that
// makes it would panic loudly rather than silently pass. (Same shape as
// whagent_net/api/handlers' fake temporal client.)
type fakeTemporal struct {
	client.Client
	signals []fakeTemporalSignal
	err     error
}

func (f *fakeTemporal) SignalWorkflow(ctx context.Context, workflowID, runID, signalName string, arg interface{}) error {
	if f.err != nil {
		return f.err
	}
	f.signals = append(f.signals, fakeTemporalSignal{
		WorkflowID: workflowID,
		RunID:      runID,
		SignalName: signalName,
		Arg:        arg,
	})
	return nil
}

// notifyFixture seeds a release run (TemporalWorkflowID set, one target)
// and returns the server wired to a recording fakeTemporal, the fake
// registry (for SetBuildRef), and the generated release_run_id.
func notifyFixture(t *testing.T) (*ReleaseServer, repository.Registry, *fakeTemporal, string) {
	t.Helper()
	repo := fake.New()
	temporal := &fakeTemporal{}
	srv := NewReleaseServer(repo, temporal)

	created, _, err := repo.ReleaseRuns().CreateReleaseRun(context.Background(), repository.ReleaseRun{
		TriggeredBy:        "test-user",
		RequestedScope:     "demo",
		TemporalWorkflowID: "release-workflow-1",
	}, []repository.ReleaseRunTarget{{
		OwnerFullName: "demo-svc",
		Kind:          repository.ArtifactKindImage,
	}})
	if err != nil {
		t.Fatalf("seed release run: %v", err)
	}
	return srv, repo, temporal, created.ReleaseRunID
}

func notifyReq(releaseRunID string, githubRunID int64, succeeded bool, detail string) *pb.NotifyBuildCompleteRequest {
	return &pb.NotifyBuildCompleteRequest{
		ReleaseRunId: releaseRunID,
		GithubRunId:  githubRunID,
		Succeeded:    succeeded,
		Detail:       detail,
	}
}

// TestNotifyBuildComplete_Authorization mirrors authz_test.go's pattern:
// NotifyBuildComplete requires exactly the builder role every other
// build-job-side write already requires -- no new role.
func TestNotifyBuildComplete_Authorization(t *testing.T) {
	srv, _, _, runID := notifyFixture(t)

	t.Run("correct role allowed", func(t *testing.T) {
		srv2, _, _, runID2 := notifyFixture(t)
		resp, err := srv2.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID2, 42, true, ""))
		if err != nil {
			t.Fatalf("expected builder to be allowed, got %v", err)
		}
		if !resp.Signaled {
			t.Fatalf("expected Signaled=true on the happy path")
		}
	})

	t.Run("wrong role is PermissionDenied", func(t *testing.T) {
		_, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RolePromoterDev), notifyReq(runID, 42, true, ""))
		requireCode(t, err, codes.PermissionDenied, "NotifyBuildComplete")
	})
}

// TestNotifyBuildComplete_Validation pins the request validation order:
// a missing release_run_id is InvalidArgument, and the nil-temporal guard
// is FailedPrecondition (a server misconfiguration, not the caller's or
// the workflow's fault).
func TestNotifyBuildComplete_Validation(t *testing.T) {
	srv, _, _, runID := notifyFixture(t)

	if _, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq("", 42, true, "")); err != nil {
		requireCode(t, err, codes.InvalidArgument, "NotifyBuildComplete")
	}

	srvNoTemporal, _ := newReleaseFixture()
	_, err := srvNoTemporal.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID, 42, true, ""))
	requireCode(t, err, codes.FailedPrecondition, "NotifyBuildComplete (nil temporal client)")
}

// TestNotifyBuildComplete_UnknownReleaseRun pins NotFound for a
// release_run_id that doesn't exist (mapRepoErr's ErrNotFound mapping).
func TestNotifyBuildComplete_UnknownReleaseRun(t *testing.T) {
	srv, _, _, _ := notifyFixture(t)

	_, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq("does-not-exist", 42, true, ""))
	requireCode(t, err, codes.NotFound, "NotifyBuildComplete")
}

// TestNotifyBuildComplete_SignalsWorkflow pins the success path: the
// handler resolves the run, cross-checks the run id against the
// persisted build_ref, and signals the run's TemporalWorkflowID with
// release.SignalBuildCompleted carrying release.BuildCompletedSignal.
func TestNotifyBuildComplete_SignalsWorkflow(t *testing.T) {
	srv, repo, temporal, runID := notifyFixture(t)
	if err := repo.ReleaseRuns().SetBuildRef(context.Background(), runID, "410", "https://run/410"); err != nil {
		t.Fatalf("SetBuildRef: %v", err)
	}

	// github_run_id 0 is normalized to "" (unverifiable, passed through)
	// and an empty detail falls back to the handler's default line.
	resp, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID, 0, true, ""))
	if err != nil {
		t.Fatalf("NotifyBuildComplete: %v", err)
	}
	if !resp.Signaled {
		t.Fatalf("expected Signaled=true")
	}
	if len(temporal.signals) != 1 {
		t.Fatalf("expected exactly 1 SignalWorkflow call, got %d", len(temporal.signals))
	}
	sig := temporal.signals[0]
	if sig.WorkflowID != "release-workflow-1" || sig.RunID != "" || sig.SignalName != release.SignalBuildCompleted {
		t.Fatalf("unexpected signal identity: %+v", sig)
	}
	payload, ok := sig.Arg.(release.BuildCompletedSignal)
	if !ok {
		t.Fatalf("expected BuildCompletedSignal payload, got %T", sig.Arg)
	}
	if payload.GitHubRunID != "" || !payload.Succeeded {
		t.Fatalf("unexpected payload: %+v", payload)
	}
	if payload.Detail != "run 0 notified build completion via NotifyBuildComplete" {
		t.Fatalf("expected default detail line, got %q", payload.Detail)
	}

	// An explicit github_run_id matching the persisted build_ref is
	// threaded through, and an explicit detail wins over the default.
	temporal.signals = nil
	resp, err = srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID, 410, false, "build job reported failure"))
	if err != nil {
		t.Fatalf("NotifyBuildComplete (explicit run id): %v", err)
	}
	if !resp.Signaled {
		t.Fatalf("expected Signaled=true for the matching run id")
	}
	payload = temporal.signals[0].Arg.(release.BuildCompletedSignal)
	if payload.GitHubRunID != "410" || payload.Succeeded || payload.Detail != "build job reported failure" {
		t.Fatalf("unexpected payload: %+v", payload)
	}
}

// TestNotifyBuildComplete_RunIDMismatch pins the stale-notification
// defense: a notification claiming a DIFFERENT Actions run's outcome than
// the one PersistBuildRef recorded must be rejected (FailedPrecondition),
// not signaled.
func TestNotifyBuildComplete_RunIDMismatch(t *testing.T) {
	srv, repo, temporal, runID := notifyFixture(t)
	if err := repo.ReleaseRuns().SetBuildRef(context.Background(), runID, "410", "https://run/410"); err != nil {
		t.Fatalf("SetBuildRef: %v", err)
	}

	_, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID, 411, true, ""))
	requireCode(t, err, codes.FailedPrecondition, "NotifyBuildComplete (run id mismatch)")
	if len(temporal.signals) != 0 {
		t.Fatalf("a mismatched notification must not reach SignalWorkflow, got %d calls", len(temporal.signals))
	}
}

// TestNotifyBuildComplete_WorkflowNotFound pins the informational no-op:
// when Temporal doesn't know the workflow execution (already completed, or
// a stale id), the response is Signaled=false with NO error -- the notify
// job must not treat that as a failure and PollRun's polling remains the
// correctness fallback.
func TestNotifyBuildComplete_WorkflowNotFound(t *testing.T) {
	srv, _, temporal, runID := notifyFixture(t)
	temporal.err = serviceerror.NewNotFound("workflow execution not found")

	resp, err := srv.NotifyBuildComplete(ctxWithRoles(auth.RoleBuilder), notifyReq(runID, 42, true, ""))
	if err != nil {
		t.Fatalf("expected NotFound to be swallowed as Signaled=false, got %v", err)
	}
	if resp.Signaled {
		t.Fatalf("expected Signaled=false when the workflow execution is unknown")
	}
}
