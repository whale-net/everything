package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func manifestSet(apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *appmetapb.AppManifestSet {
	return &appmetapb.AppManifestSet{Apps: apps, Charts: charts}
}

func oneApp(domain, name string) *appmetapb.AppManifest {
	return &appmetapb.AppManifest{
		Domain: domain, Name: name, Description: "d", Language: "go", AppType: "worker",
		DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE,
	}
}

// TestReconcileApps_FullLifecycle walks create -> update -> absent-marks-MISSING
// -> reappear-recovers-ACTIVE, matching the AR-2a scope's required coverage.
func TestReconcileApps_FullLifecycle(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	// 1. create
	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-1-1",
	})
	if err != nil {
		t.Fatalf("create reconcile: %v", err)
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 created ACTIVE app, got %+v", resp.CreatedApps)
	}
	appID := resp.CreatedApps[0].AppId

	// 2. update (same app, different description)
	am := oneApp("demo", "svc")
	am.Description = "updated"
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{am}, nil),
		IdempotencyKey: "run-1-2",
	})
	if err != nil {
		t.Fatalf("update reconcile: %v", err)
	}
	if len(resp.UpdatedApps) != 1 || resp.UpdatedApps[0].Description != "updated" {
		t.Fatalf("expected 1 updated app with new description, got %+v", resp.UpdatedApps)
	}

	// 3. absent -> MISSING
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "run-1-3",
	})
	if err != nil {
		t.Fatalf("absent reconcile: %v", err)
	}
	if len(resp.NewlyMissingApps) != 1 || resp.NewlyMissingApps[0].AppId != appID {
		t.Fatalf("expected app %s newly missing, got %+v", appID, resp.NewlyMissingApps)
	}

	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: appID})
	if err != nil {
		t.Fatalf("get app after marking missing: %v", err)
	}
	if getResp.App.Status != pb.AppStatus_APP_STATUS_MISSING {
		t.Fatalf("expected MISSING, got %v", getResp.App.Status)
	}

	// 4. reappear -> recovered ACTIVE
	resp, err = srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-1-4",
	})
	if err != nil {
		t.Fatalf("recover reconcile: %v", err)
	}
	if len(resp.RecoveredApps) != 1 || resp.RecoveredApps[0].Status != pb.AppStatus_APP_STATUS_ACTIVE {
		t.Fatalf("expected 1 recovered ACTIVE app, got %+v", resp.RecoveredApps)
	}
}

// TestReconcileApps_DryRunWritesNothing proves dry_run computes a diff
// without mutating the registry: a subsequent real reconcile still sees the
// app as newly created.
func TestReconcileApps_DryRunWritesNothing(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	dryResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		DryRun:    true,
	})
	if err != nil {
		t.Fatalf("dry run reconcile: %v", err)
	}
	if !dryResp.DryRun || len(dryResp.CreatedApps) != 1 {
		t.Fatalf("expected dry_run diff showing 1 created app, got %+v", dryResp)
	}

	listResp, err := srv.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(listResp.Apps) != 0 {
		t.Fatalf("dry_run must write nothing; found %d apps", len(listResp.Apps))
	}

	// A real reconcile afterwards should still see this as a fresh create.
	realResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-2-1",
	})
	if err != nil {
		t.Fatalf("real reconcile after dry run: %v", err)
	}
	if len(realResp.CreatedApps) != 1 {
		t.Fatalf("expected the real reconcile to still create the app, got %+v", realResp)
	}
}

// TestReconcileApps_IdempotencyReplaysWithoutDoubleWrite proves a repeated
// call with the same idempotency_key replays the original response and does
// not re-execute the write — asserted by row count, per AR-2a's required
// coverage.
func TestReconcileApps_IdempotencyReplaysWithoutDoubleWrite(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	req := &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "same-key",
	}

	first, err := srv.ReconcileApps(ctx, req)
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	if len(first.CreatedApps) != 1 {
		t.Fatalf("expected 1 created app, got %+v", first.CreatedApps)
	}

	second, err := srv.ReconcileApps(ctx, req)
	if err != nil {
		t.Fatalf("replayed reconcile: %v", err)
	}
	if len(second.CreatedApps) != 1 || second.CreatedApps[0].AppId != first.CreatedApps[0].AppId {
		t.Fatalf("replayed response should be byte-identical to the first, got %+v vs %+v", second, first)
	}

	listResp, err := srv.ListApps(ctx, &pb.ListAppsRequest{})
	if err != nil {
		t.Fatalf("list apps: %v", err)
	}
	if len(listResp.Apps) != 1 {
		t.Fatalf("idempotent replay must not double-write; expected 1 app row, got %d", len(listResp.Apps))
	}
}

// TestReconcileApps_UnknownChartAppFailsWholeReconcile covers "resolve
// ChartManifest.apps (bare app names) to app IDs and fail the reconcile if
// any is unknown."
func TestReconcileApps_UnknownChartAppFailsWholeReconcile(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	_, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(nil, []*appmetapb.ChartManifest{
			{Domain: "demo", Name: "chart", Apps: []string{"nonexistent"}},
		}),
		IdempotencyKey: "run-3-1",
	})
	if err == nil {
		t.Fatal("expected an error for a chart referencing an unknown app")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument, got %v", st.Code())
	}

	// Nothing should have been written.
	listResp, _ := srv.ListCharts(ctx, &pb.ListChartsRequest{})
	if len(listResp.Charts) != 0 {
		t.Fatalf("failed reconcile must not write a partial chart, got %d charts", len(listResp.Charts))
	}
}

// TestReconcileApps_ChartComposesApps covers the happy path of resolving a
// chart's bare app names to app_ids.
func TestReconcileApps_ChartComposesApps(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet(
			[]*appmetapb.AppManifest{oneApp("demo", "svc")},
			[]*appmetapb.ChartManifest{{Domain: "demo", Name: "chart", Apps: []string{"svc"}}},
		),
		IdempotencyKey: "run-4-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if len(resp.CreatedCharts) != 1 || len(resp.CreatedCharts[0].AppIds) != 1 {
		t.Fatalf("expected 1 chart composing 1 app, got %+v", resp.CreatedCharts)
	}
	if resp.CreatedCharts[0].AppIds[0] != resp.CreatedApps[0].AppId {
		t.Fatalf("chart's app_ids should resolve to the created app's id")
	}
}

// ============================================================================
// Reconcile watermark (issue #545) -- fake-backed handler coverage.
// See postgres_integration_test.go's "Reconcile watermark" section for the
// same scenarios exercised against real Postgres (real FOR UPDATE locking,
// real transaction rollback). These fake-backed versions exist so the
// business logic (repository.ShouldApplyReconcile and its wiring through
// the handler/repository layers) is covered by the fast unit tier too, not
// only the manual Postgres tier.
// ============================================================================

func manifestSetWithSource(gitSha string, sourceCommittedAt, discoveredAt int64, apps []*appmetapb.AppManifest, charts []*appmetapb.ChartManifest) *appmetapb.AppManifestSet {
	return &appmetapb.AppManifestSet{
		GitSha: gitSha, SourceCommittedAt: sourceCommittedAt, DiscoveredAt: discoveredAt,
		Apps: apps, Charts: charts,
	}
}

// TestReconcileApps_StaleCallSkippedWithoutError covers the headline
// scenario from issue #545: an older commit's call lands after a newer
// one's and must be a no-op SUCCESS (not an error), naming the commit it
// lost to, and leaving the newer commit's state (including a MISSING flag)
// untouched.
func TestReconcileApps_StaleCallSkippedWithoutError(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	first, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("newer-sha", 2000, 2000, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "wm-1",
	})
	if err != nil {
		t.Fatalf("first reconcile: %v", err)
	}
	svcID := first.CreatedApps[0].AppId

	// A newer commit drops "svc" -- correctly MISSING.
	second, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("newest-sha", 3000, 3000, nil, nil),
		IdempotencyKey: "wm-2",
	})
	if err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	if len(second.NewlyMissingApps) != 1 || second.NewlyMissingApps[0].AppId != svcID {
		t.Fatalf("expected svc newly missing, got %+v", second.NewlyMissingApps)
	}

	// A STALE call -- an older commit re-running -- must not revert that.
	stale, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("older-rerun-sha", 2500, 2500, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "wm-3",
	})
	if err != nil {
		t.Fatalf("stale reconcile must be a no-op SUCCESS, not an error: %v", err)
	}
	if !stale.SkippedStale {
		t.Fatalf("expected SkippedStale=true for an older-commit call, got %+v", stale)
	}
	if stale.CurrentWatermarkGitSha != "newest-sha" {
		t.Fatalf("expected current_watermark_git_sha=newest-sha, got %q", stale.CurrentWatermarkGitSha)
	}
	if len(stale.CreatedApps)+len(stale.UpdatedApps)+len(stale.RecoveredApps) != 0 {
		t.Fatalf("expected an empty result for a skipped-stale call, got %+v", stale)
	}

	// svc must STILL be MISSING -- the stale call did not revert it.
	getResp, err := srv.GetApp(ctx, &pb.GetAppRequest{AppId: svcID})
	if err != nil {
		t.Fatalf("get app after stale call: %v", err)
	}
	if getResp.App.Status != pb.AppStatus_APP_STATUS_MISSING {
		t.Fatalf("stale call reverted svc's MISSING flag -- now %v", getResp.App.Status)
	}
}

// TestReconcileApps_EqualOrderingKeyDifferentGitShaApplies proves the
// deliberate equal-timestamp tie-break: two different commits at the same
// ordering key must not block each other.
func TestReconcileApps_EqualOrderingKeyDifferentGitShaApplies(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("sha-a", 5000, 5000, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "tie-1",
	}); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	resp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("sha-b", 5000, 5000,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "svc2")}, nil),
		IdempotencyKey: "tie-2",
	})
	if err != nil {
		t.Fatalf("tied-timestamp reconcile: %v", err)
	}
	if resp.SkippedStale {
		t.Fatalf("expected an equal-timestamp, different-git_sha call to apply, got SkippedStale=true")
	}
	if len(resp.CreatedApps) != 1 || resp.CreatedApps[0].FullName != "demo-svc2" {
		t.Fatalf("expected svc2 to be newly created, got %+v", resp.CreatedApps)
	}
}

// TestReconcileApps_DryRunNeverConsultsOrAdvancesWatermark proves dry_run
// is unaffected by the watermark in either direction, mirroring
// postgres_integration_test.go's version of this proof against real
// Postgres.
func TestReconcileApps_DryRunNeverConsultsOrAdvancesWatermark(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSetWithSource("real-sha", 999999, 999999, []*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "dry-wm-1",
	}); err != nil {
		t.Fatalf("real reconcile: %v", err)
	}

	// A dry run carrying a far-older ordering key must still compute a
	// normal diff -- if it consulted the watermark, this would come back
	// SkippedStale with an empty diff instead.
	dryResp, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("dry-run-old-sha", 1, 1,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "new-app")}, nil),
		DryRun: true,
	})
	if err != nil {
		t.Fatalf("dry run reconcile: %v", err)
	}
	if dryResp.SkippedStale {
		t.Fatalf("dry run must never consult the watermark, got SkippedStale=true")
	}
	if len(dryResp.CreatedApps) != 1 || dryResp.CreatedApps[0].FullName != "demo-new-app" {
		t.Fatalf("expected dry run to compute a normal diff (1 created app), got %+v", dryResp.CreatedApps)
	}

	// And a REAL call carrying that same far-older key must still be
	// rejected as stale -- proving the dry run above did not advance the
	// watermark.
	afterDry, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSetWithSource("dry-run-old-sha", 1, 1,
			[]*appmetapb.AppManifest{oneApp("demo", "svc"), oneApp("demo", "new-app")}, nil),
		IdempotencyKey: "dry-wm-2",
	})
	if err != nil {
		t.Fatalf("post-dry-run real reconcile: %v", err)
	}
	if !afterDry.SkippedStale {
		t.Fatalf("expected the same far-older key to still be rejected as stale after the dry run, got SkippedStale=false -- the dry run must have advanced the watermark")
	}
}

// TestSetAppStatus_MissingToArchivedSucceeds covers the only transition a
// human triggers through this RPC: missing -> archived. missing -> active
// happens automatically via Reconcile's "recovered" path instead.
func TestSetAppStatus_MissingToArchivedSucceeds(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-5-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	// Drop "svc" from the manifest set so it becomes MISSING.
	if _, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet(nil, nil),
		IdempotencyKey: "run-5-2",
	}); err != nil {
		t.Fatalf("reconcile dropping svc: %v", err)
	}

	archived, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good",
	})
	if err != nil {
		t.Fatalf("archive: %v", err)
	}
	if archived.App.Status != pb.AppStatus_APP_STATUS_ARCHIVED {
		t.Fatalf("expected ARCHIVED, got %v", archived.App.Status)
	}

	// archived -> archived is an idempotent no-op success, not an error.
	archivedAgain, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "gone for good, again",
	})
	if err != nil {
		t.Fatalf("re-archive: %v", err)
	}
	if archivedAgain.App.Status != pb.AppStatus_APP_STATUS_ARCHIVED {
		t.Fatalf("expected ARCHIVED, got %v", archivedAgain.App.Status)
	}
}

// TestSetAppStatus_RejectsActiveToArchived covers the FailedPrecondition
// path: an app must be MISSING before it can be archived.
func TestSetAppStatus_RejectsActiveToArchived(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())

	created, err := srv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests:      manifestSet([]*appmetapb.AppManifest{oneApp("demo", "svc")}, nil),
		IdempotencyKey: "run-6-1",
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	svcID := created.CreatedApps[0].AppId

	_, err = srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: svcID, Status: pb.AppStatus_APP_STATUS_ARCHIVED, Reason: "should not work",
	})
	if err == nil {
		t.Fatal("expected SetAppStatus to fail archiving an ACTIVE app")
	}
	st, _ := status.FromError(err)
	if st.Code() != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition, got %v", st.Code())
	}
}

// TestSetAppStatus_RejectsActiveTarget covers "ACTIVE is not a legal target
// at all" — dropped along with the last_seen_at heuristic that used to guard
// it, since Reconcile's recovered path is the only way back to ACTIVE.
func TestSetAppStatus_RejectsActiveTarget(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{
		AppId: "x", Status: pb.AppStatus_APP_STATUS_ACTIVE, Reason: "manual reactivate",
	})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument for status ACTIVE, got %v", err)
	}
}

func TestSetAppStatus_RequiresReason(t *testing.T) {
	ctx := authedCtx()
	srv := NewAppServer(fake.New())
	_, err := srv.SetAppStatus(ctx, &pb.SetAppStatusRequest{AppId: "x", Status: pb.AppStatus_APP_STATUS_ARCHIVED})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when reason is missing, got %v", err)
	}
}
