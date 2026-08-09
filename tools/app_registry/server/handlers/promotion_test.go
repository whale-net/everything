package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository/fake"
	appmetapb "github.com/whale-net/everything/tools/appmeta/proto"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// promotionFixture wires one shared fake registry through all four servers,
// with one app per DeployUnit (mirroring artifact_test.go's setup) plus a
// chart pinning the chart-deployed app's image, and dev(rank 0)/stage(rank
// 10) environments -- everything ARCHITECTURE.md's "Promotability" and
// "Authorization" sections need to be exercised.
type promotionFixture struct {
	app   *AppServer
	art   *ArtifactServer
	env   *EnvironmentServer
	promo *PromotionServer

	chartImageDigest string // the digest the chart pins for chart-app
}

func newPromotionFixture(t *testing.T) *promotionFixture {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artSrv := NewArtifactServer(repo)
	envSrv := NewEnvironmentServer(repo)
	promoSrv := NewPromotionServer(repo)
	ctx := authedCtx()

	if _, err := appSrv.ReconcileApps(ctx, &pb.ReconcileAppsRequest{
		Manifests: manifestSet([]*appmetapb.AppManifest{
			{Domain: "demo", Name: "chart-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_CHART},
			{Domain: "demo", Name: "image-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_IMAGE},
			{Domain: "demo", Name: "none-app", DeployUnit: appmetapb.DeployUnit_DEPLOY_UNIT_NONE},
		}, []*appmetapb.ChartManifest{
			{Domain: "demo", Name: "achart", Apps: []string{"chart-app"}},
		}),
		IdempotencyKey: "fixture-reconcile",
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	build, err := artSrv.RecordBuild(ctx, &pb.RecordBuildRequest{GitSha: "abc123", WorkflowRunId: "run-1", IdempotencyKey: "fixture-build"})
	if err != nil {
		t.Fatalf("record build: %v", err)
	}

	mustRecordArtifact(t, artSrv, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-chart-app", Digest: "sha256:chartapp-v1", Version: "v1.0.0",
		IdempotencyKey: "fixture-artifact-chartapp-image",
	})
	mustRecordArtifact(t, artSrv, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:imageapp-v1", Version: "v1.0.0",
		IdempotencyKey: "fixture-artifact-imageapp",
	})
	mustRecordArtifact(t, artSrv, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-none-app", Digest: "sha256:noneapp-v1", Version: "v1.0.0",
		IdempotencyKey: "fixture-artifact-noneapp",
	})
	mustRecordArtifact(t, artSrv, &pb.RecordArtifactRequest{
		BuildId: build.Build.BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:achart-v1", Version: "v1.0.0",
		Contains: []*pb.ContainedImage{
			{AppFullName: "demo-chart-app", Repository: "ghcr.io/demo/chart-app", Version: "v1.0.0", Digest: "sha256:chartapp-v1"},
		},
		IdempotencyKey: "fixture-artifact-achart",
	})

	for _, e := range []struct {
		key  string
		rank int32
	}{{"dev", 0}, {"stage", 10}} {
		if _, err := envSrv.UpsertEnvironment(ctx, &pb.UpsertEnvironmentRequest{Key: e.key, Rank: e.rank}); err != nil {
			t.Fatalf("upsert environment %s: %v", e.key, err)
		}
	}

	return &promotionFixture{app: appSrv, art: artSrv, env: envSrv, promo: promoSrv, chartImageDigest: "sha256:chartapp-v1"}
}

func mustRecordArtifact(t *testing.T, srv *ArtifactServer, req *pb.RecordArtifactRequest) *pb.Artifact {
	t.Helper()
	resp, err := srv.RecordArtifact(authedCtx(), req)
	if err != nil {
		t.Fatalf("RecordArtifact %s: %v", req.OwnerFullName, err)
	}
	return resp.Artifact
}

func promoteReq(env, ownerFullName string, kind pb.ArtifactKind, key string, opts ...func(*pb.PromoteRequest)) *pb.PromoteRequest {
	req := &pb.PromoteRequest{
		EnvironmentKey: env, OwnerFullName: ownerFullName, Kind: kind, Version: "v1.0.0", IdempotencyKey: key,
	}
	for _, o := range opts {
		o(req)
	}
	return req
}

func withReason(reason string) func(*pb.PromoteRequest) {
	return func(r *pb.PromoteRequest) { r.Reason = reason }
}
func withOverride() func(*pb.PromoteRequest) {
	return func(r *pb.PromoteRequest) { r.AllowOverride = true }
}

// TestPromote_NotPromotable_Rejected covers ARCHITECTURE.md's promotability
// table: a DEPLOY_UNIT_NONE app's image is never a legal promotion target.
func TestPromote_NotPromotable_Rejected(t *testing.T) {
	f := newPromotionFixture(t)
	req := promoteReq("dev", "demo-none-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-none")
	_, err := f.promo.Promote(authedCtx(), req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition for a not-promotable artifact, got %v", err)
	}
}

// TestPromote_ViaChartImage_RequiresAllowOverride covers the override rule:
// promoting a VIA_CHART image directly is rejected unless allow_override is
// set, and when set the promotion is stored with is_override=true.
func TestPromote_ViaChartImage_RequiresAllowOverride(t *testing.T) {
	f := newPromotionFixture(t)

	req := promoteReq("dev", "demo-chart-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-viachart-1")
	_, err := f.promo.Promote(authedCtx(), req)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition without allow_override, got %v", err)
	}

	req2 := promoteReq("dev", "demo-chart-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-viachart-2", withOverride())
	resp, err := f.promo.Promote(authedCtx(), req2)
	if err != nil {
		t.Fatalf("expected allow_override to succeed, got %v", err)
	}
	if !resp.Promotion.IsOverride {
		t.Fatalf("expected is_override=true when allow_override was used, got %+v", resp.Promotion)
	}
}

// TestPromote_Promotable_Succeeds covers the direct-promotion path for a
// DEPLOY_UNIT_IMAGE app -- legal with or without allow_override.
func TestPromote_Promotable_Succeeds(t *testing.T) {
	f := newPromotionFixture(t)
	resp, err := f.promo.Promote(authedCtx(), promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-image"))
	if err != nil {
		t.Fatalf("expected a promotable image to succeed, got %v", err)
	}
	if resp.Promotion.IsOverride {
		t.Fatalf("expected is_override=false for a directly-promotable artifact, got %+v", resp.Promotion)
	}
	if resp.Promotion.ValidTo != 0 {
		t.Fatalf("expected the new promotion to be current (valid_to == 0), got %+v", resp.Promotion)
	}
}

// TestPromote_ReasonRequiredAboveRankZero covers ARCHITECTURE.md
// "Authorization": "reason is required on promotions to any environment
// above rank 0." dev is rank 0 and needs no reason; stage is rank 10.
func TestPromote_ReasonRequiredAboveRankZero(t *testing.T) {
	f := newPromotionFixture(t)

	// dev (rank 0): no reason needed.
	if _, err := f.promo.Promote(authedCtx(), promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-dev-noreason")); err != nil {
		t.Fatalf("expected dev promotion without reason to succeed, got %v", err)
	}

	// stage (rank 10): reason required.
	_, err := f.promo.Promote(authedCtx(), promoteReq("stage", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-stage-noreason"))
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected InvalidArgument when promoting to stage without a reason, got %v", err)
	}

	if _, err := f.promo.Promote(authedCtx(), promoteReq("stage", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-stage-reason", withReason("shipping"))); err != nil {
		t.Fatalf("expected stage promotion with a reason to succeed, got %v", err)
	}
}

// TestPromote_PromoteAgain_ExactlyOneCurrentRow proves the SCD2
// close-and-open behaviour end to end through the handler: promoting a
// second version supersedes the first, and ListPromotions(include_history)
// shows exactly one current row plus the closed one.
func TestPromote_PromoteAgain_ExactlyOneCurrentRow(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	first, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-first"))
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}
	if first.Superseded != nil {
		t.Fatalf("expected no superseded row on the first-ever promotion, got %+v", first.Superseded)
	}

	mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId: mustRecordBuild(t, f.art, "run-2").BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:imageapp-v2", Version: "v2.0.0",
		IdempotencyKey: "fixture-artifact-imageapp-v2",
	})

	req := promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-second")
	req.Version = "v2.0.0"
	second, err := f.promo.Promote(ctx, req)
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if second.Superseded == nil || second.Superseded.PromotionId != first.Promotion.PromotionId {
		t.Fatalf("expected the second promote to supersede the first, got %+v", second.Superseded)
	}
	if second.Superseded.ValidTo == 0 {
		t.Fatalf("expected superseded.valid_to to be set, got %+v", second.Superseded)
	}
	if second.Promotion.ValidTo != 0 {
		t.Fatalf("expected the new promotion to be current, got %+v", second.Promotion)
	}

	hist, err := f.promo.ListPromotions(ctx, &pb.ListPromotionsRequest{EnvironmentKey: "dev", OwnerFullName: "demo-image-app", IncludeHistory: true})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist.Promotions) != 2 {
		t.Fatalf("expected 2 rows (1 current + 1 superseded) with include_history, got %d: %+v", len(hist.Promotions), hist.Promotions)
	}

	current, err := f.promo.ListPromotions(ctx, &pb.ListPromotionsRequest{EnvironmentKey: "dev", OwnerFullName: "demo-image-app"})
	if err != nil {
		t.Fatalf("list current: %v", err)
	}
	if len(current.Promotions) != 1 || current.Promotions[0].PromotionId != second.Promotion.PromotionId {
		t.Fatalf("expected exactly 1 current row (the second promotion), got %+v", current.Promotions)
	}
}

func mustRecordBuild(t *testing.T, srv *ArtifactServer, runID string) *pb.Build {
	t.Helper()
	resp, err := srv.RecordBuild(authedCtx(), &pb.RecordBuildRequest{GitSha: "abc123", WorkflowRunId: runID, IdempotencyKey: runID + "-build"})
	if err != nil {
		t.Fatalf("record build %s: %v", runID, err)
	}
	return resp.Build
}

// TestPromote_AlreadyPromoted_ShortCircuits covers the AlreadyPromoted flag:
// promoting the same artifact twice does not create a second SCD2 row.
func TestPromote_AlreadyPromoted_ShortCircuits(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	first, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-again-1"))
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}

	second, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-again-2"))
	if err != nil {
		t.Fatalf("second promote of the same artifact: %v", err)
	}
	if !second.AlreadyPromoted {
		t.Fatalf("expected already_promoted=true, got %+v", second)
	}
	if second.Promotion.PromotionId != first.Promotion.PromotionId {
		t.Fatalf("expected re-promoting the same artifact to be a no-op (same promotion_id), got %s vs %s", second.Promotion.PromotionId, first.Promotion.PromotionId)
	}

	hist, err := f.promo.ListPromotions(ctx, &pb.ListPromotionsRequest{EnvironmentKey: "dev", OwnerFullName: "demo-image-app", IncludeHistory: true})
	if err != nil {
		t.Fatalf("list history: %v", err)
	}
	if len(hist.Promotions) != 1 {
		t.Fatalf("expected no new row from the already-promoted call, got %d: %+v", len(hist.Promotions), hist.Promotions)
	}
}

// TestPromote_DryRun_DoesNotWrite proves dry_run computes the transition
// without touching state.
func TestPromote_DryRun_DoesNotWrite(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	req := promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "promo-dryrun")
	req.DryRun = true
	resp, err := f.promo.Promote(ctx, req)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !resp.DryRun || resp.Promotion == nil {
		t.Fatalf("expected a dry_run response describing the candidate promotion, got %+v", resp)
	}

	list, err := f.promo.ListPromotions(ctx, &pb.ListPromotionsRequest{EnvironmentKey: "dev", OwnerFullName: "demo-image-app", IncludeHistory: true})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list.Promotions) != 0 {
		t.Fatalf("expected dry_run to write nothing, found %d promotion(s)", len(list.Promotions))
	}
}

// TestRollback_RoundTrips covers RollbackRequest's doc comment: it
// re-promotes whatever was previously current.
func TestRollback_RoundTrips(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	v1, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "rollback-promo-1"))
	if err != nil {
		t.Fatalf("promote v1: %v", err)
	}

	mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId: mustRecordBuild(t, f.art, "run-rollback-2").BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:imageapp-rb-v2", Version: "v2.0.0",
		IdempotencyKey: "fixture-artifact-imageapp-rb-v2",
	})
	req := promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "rollback-promo-2")
	req.Version = "v2.0.0"
	v2, err := f.promo.Promote(ctx, req)
	if err != nil {
		t.Fatalf("promote v2: %v", err)
	}

	rollback, err := f.promo.Rollback(ctx, &pb.RollbackRequest{
		EnvironmentKey: "dev", OwnerFullName: "demo-image-app", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, IdempotencyKey: "rollback-1",
	})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}
	if rollback.Promotion.ArtifactId != v1.Promotion.ArtifactId {
		t.Fatalf("expected rollback to re-promote v1's artifact %s, got %s", v1.Promotion.ArtifactId, rollback.Promotion.ArtifactId)
	}
	if rollback.Superseded == nil || rollback.Superseded.PromotionId != v2.Promotion.PromotionId {
		t.Fatalf("expected rollback to supersede v2's promotion, got %+v", rollback.Superseded)
	}
}

// TestRollback_NoPriorPromotion_FailedPrecondition covers the "nothing to
// roll back to" case: a target promoted exactly once (or never) has no
// GetPrevious row.
func TestRollback_NoPriorPromotion_FailedPrecondition(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	if _, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "rb-none-1")); err != nil {
		t.Fatalf("promote: %v", err)
	}

	_, err := f.promo.Rollback(ctx, &pb.RollbackRequest{
		EnvironmentKey: "dev", OwnerFullName: "demo-image-app", Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE, IdempotencyKey: "rb-none-2",
	})
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("expected FailedPrecondition when there is no prior promotion, got %v", err)
	}
}

// TestGetEnvironmentState_ReportsDrift covers ARCHITECTURE.md's override
// drift reporting: promoting the chart, then separately promoting a
// different digest of the image it pins with allow_override, must surface
// as a DriftEntry on the chart's EnvironmentStateEntry.
func TestGetEnvironmentState_ReportsDrift(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	if _, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "drift-chart")); err != nil {
		t.Fatalf("promote chart: %v", err)
	}

	mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId: mustRecordBuild(t, f.art, "run-drift-hotfix").BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-chart-app", Digest: "sha256:chartapp-hotfix", Version: "v1.0.1",
		IdempotencyKey: "fixture-artifact-chartapp-hotfix",
	})
	req := promoteReq("dev", "demo-chart-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "drift-override", withOverride())
	req.Version = "v1.0.1"
	if _, err := f.promo.Promote(ctx, req); err != nil {
		t.Fatalf("promote override image: %v", err)
	}

	state, err := f.promo.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{EnvironmentKey: "dev"})
	if err != nil {
		t.Fatalf("get state: %v", err)
	}

	var chartEntry *pb.EnvironmentStateEntry
	for _, e := range state.Entries {
		if e.Artifact.Kind == pb.ArtifactKind_ARTIFACT_KIND_CHART {
			chartEntry = e
		}
	}
	if chartEntry == nil {
		t.Fatalf("expected a chart entry in the environment state, got %+v", state.Entries)
	}
	if len(chartEntry.Drift) != 1 {
		t.Fatalf("expected exactly 1 drift entry on the chart, got %+v", chartEntry.Drift)
	}
	d := chartEntry.Drift[0]
	if d.ChartPinnedDigest != f.chartImageDigest || d.PromotedDigest != "sha256:chartapp-hotfix" {
		t.Fatalf("unexpected drift entry: %+v", d)
	}
	if state.StateHash == "" {
		t.Fatalf("expected a non-empty state_hash")
	}
}

// The "GetEnvironmentState --at <T> returns correct historical state"
// exit criterion is deliberately proven at the repository layer against
// real Postgres (see postgres_integration_test.go's
// TestPromotionRepo_StateAt_HistoricalWindow), not here: wall-clock time at
// second resolution (Promotion.valid_from/valid_to are Unix seconds) makes
// a handler-level "promote twice, query between them" test flaky by
// construction -- two Promote calls a few microseconds apart routinely land
// in the same second.
