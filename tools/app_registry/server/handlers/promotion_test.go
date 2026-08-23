package handlers

import (
	"context"
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
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
	repo  repository.Registry // exposed so tests can assert on the outbox directly

	chartImageDigest string // the digest the chart pins for chart-app
}

func newPromotionFixture(t *testing.T) *promotionFixture {
	t.Helper()
	repo := fake.New()
	appSrv := NewAppServer(repo)
	artSrv := NewArtifactServer(repo)
	envSrv := NewEnvironmentServer(repo)
	promoSrv := NewPromotionServer(repo, nil)
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

	return &promotionFixture{app: appSrv, art: artSrv, env: envSrv, promo: promoSrv, repo: repo, chartImageDigest: "sha256:chartapp-v1"}
}

// mustRecordArtifact records a published artifact, via the mandatory
// BeginPublish -> RecordArtifact sequence (there is no direct-create
// fallback).
func mustRecordArtifact(t *testing.T, srv *ArtifactServer, req *pb.RecordArtifactRequest) *pb.Artifact {
	t.Helper()
	ctx := authedCtx()
	repo := req.Repository
	if repo == "" {
		repo = "ghcr.io/" + req.OwnerFullName
	}
	if _, err := srv.BeginPublish(ctx, &pb.BeginPublishRequest{
		Kind: req.Kind, OwnerFullName: req.OwnerFullName, Version: req.Version,
		BuildId: req.BuildId, Repository: repo,
		IdempotencyKey: req.IdempotencyKey + "-begin",
	}); err != nil {
		t.Fatalf("BeginPublish %s: %v", req.OwnerFullName, err)
	}
	resp, err := srv.RecordArtifact(ctx, req)
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
// real Postgres (see postgres_integration_promotion_test.go's
// TestPromotionRepo_StateAt_HistoricalWindow), not here: wall-clock time at
// second resolution (Promotion.valid_from/valid_to are Unix seconds) makes
// a handler-level "promote twice, query between them" test flaky by
// construction -- two Promote calls a few microseconds apart routinely land
// in the same second.

// TestPromote_EnqueuesWritebackOutbox covers AR-4b: a successful Promote of
// a CHART artifact writes one writeback_outbox row in the same call,
// carrying the same promotion id, the environment key, the promotion_event
// id, and a state_hash equal to what a subsequent GetEnvironmentState
// reports -- see server/handlers/promotion.go's enqueueWriteback and
// ARCHITECTURE.md "Writeback: outbox -> Temporal". Uses demo-achart (CHART
// kind), not demo-image-app -- see shouldEnqueueWriteback's doc comment
// (FR9/#881/#893): only a CHART promotion ever enqueues a writeback.
func TestPromote_EnqueuesWritebackOutbox(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	resp, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "outbox-promo"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 outbox row after one promotion, got %d: %+v", len(rows), rows)
	}
	row := rows[0]
	if row.PromotionID != resp.Promotion.PromotionId {
		t.Fatalf("expected outbox row to carry the new promotion id %s, got %s", resp.Promotion.PromotionId, row.PromotionID)
	}
	if row.EnvironmentKey != "dev" {
		t.Fatalf("expected outbox row environment_key=dev, got %q", row.EnvironmentKey)
	}
	if row.EventID != resp.Event.EventId {
		t.Fatalf("expected outbox row to carry promotion_event id %s, got %s", resp.Event.EventId, row.EventID)
	}
	if row.Status != repository.WritebackOutboxStatusClaimed {
		// claimAllOutbox claims everything it finds -- this just confirms
		// ClaimBatch actually flipped the status, i.e. the row really was
		// there to claim.
		t.Fatalf("expected the claimed row's status to be 'claimed', got %q", row.Status)
	}

	state, err := f.promo.GetEnvironmentState(ctx, &pb.GetEnvironmentStateRequest{EnvironmentKey: "dev"})
	if err != nil {
		t.Fatalf("get environment state: %v", err)
	}
	if row.StateHash != state.StateHash {
		t.Fatalf("expected the outbox row's state_hash (%s) to equal GetEnvironmentState's state_hash (%s) -- see enqueueWriteback's doc comment", row.StateHash, state.StateHash)
	}
}

// TestPromote_AlreadyPromoted_DoesNotEnqueueOutbox covers the flip side of
// TestPromote_AlreadyPromoted_ShortCircuits: a no-op re-promotion writes no
// promotion_event, so it must not write an outbox row either -- there is
// nothing new for a workflow to carry.
func TestPromote_AlreadyPromoted_DoesNotEnqueueOutbox(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	if _, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "outbox-again-1")); err != nil {
		t.Fatalf("first promote: %v", err)
	}
	second, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "outbox-again-2"))
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}
	if !second.AlreadyPromoted {
		t.Fatalf("expected already_promoted=true, got %+v", second)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 outbox row total (only the first promotion writes one), got %d: %+v", len(rows), rows)
	}
}

// TestPromote_DryRun_DoesNotEnqueueOutbox mirrors
// TestPromote_DryRun_DoesNotWrite for the outbox.
func TestPromote_DryRun_DoesNotEnqueueOutbox(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	req := promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "outbox-dryrun")
	req.DryRun = true
	if _, err := f.promo.Promote(ctx, req); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 0 {
		t.Fatalf("expected dry_run to enqueue no outbox row, found %d: %+v", len(rows), rows)
	}
}

// TestRollback_EnqueuesWritebackOutbox covers Rollback's own
// enqueueWriteback call, symmetric with Promote's. Uses demo-achart (CHART
// kind) -- see TestPromote_EnqueuesWritebackOutbox's doc comment.
func TestRollback_EnqueuesWritebackOutbox(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	if _, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "rb-outbox-1")); err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId: mustRecordBuild(t, f.art, "run-rb-outbox").BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART,
		OwnerFullName: "demo-achart", Digest: "sha256:achart-rb-v2", Version: "v2.0.0",
		IdempotencyKey: "rb-outbox-artifact-v2",
	})
	v2Req := promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "rb-outbox-2")
	v2Req.Version = "v2.0.0"
	if _, err := f.promo.Promote(ctx, v2Req); err != nil {
		t.Fatalf("promote v2: %v", err)
	}

	// Drain the two outbox rows written by the promotions above to 'done'
	// (not just claimed -- a merely-claimed row is still reclaimable, see
	// ClaimBatch's staleness semantics) so the assertion below is scoped to
	// what Rollback itself writes.
	drainOutboxToDone(t, f.repo)

	rbResp, err := f.promo.Rollback(ctx, &pb.RollbackRequest{
		EnvironmentKey: "dev", OwnerFullName: "demo-achart", Kind: pb.ArtifactKind_ARTIFACT_KIND_CHART, IdempotencyKey: "rb-outbox-rollback",
	})
	if err != nil {
		t.Fatalf("rollback: %v", err)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 outbox row from the rollback call, got %d: %+v", len(rows), rows)
	}
	if rows[0].PromotionID != rbResp.Promotion.PromotionId {
		t.Fatalf("expected the rollback's outbox row to carry promotion id %s, got %s", rbResp.Promotion.PromotionId, rows[0].PromotionID)
	}
}

// TestPromote_ImageArtifact_DoesNotEnqueueOutbox proves FR9/#881's
// DeployUnit-aware writeback guard (folded into this plan per #893):
// promoting demo-image-app (deploy_unit=image, a direct-deploy app with no
// owning chart) must never enqueue a writeback_outbox row -- before this
// guard, this exact promotion produced a WritebackWorkflow execution that
// could only fail (no CHART entry exists anywhere in this environment's
// state) or, in a domain that also has a chart, write back an unrelated
// chart's state -- see shouldEnqueueWriteback's doc comment.
func TestPromote_ImageArtifact_DoesNotEnqueueOutbox(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	if _, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "no-outbox-image")); err != nil {
		t.Fatalf("promote: %v", err)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 0 {
		t.Fatalf("expected a direct-deploy IMAGE promotion to enqueue no outbox row, found %d: %+v", len(rows), rows)
	}
}

func TestPromote_BinaryArtifact(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	// Record a binary artifact for image-app (which has deploy_unit = image)
	build := recordBuild(t, f.art, "run-binary-promo")
	recArtifact := mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId:        build.BuildId,
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		OwnerFullName:  "demo-image-app",
		Version:        "v1.0.0",
		Digest:         "sha256:binary-digest-1",
		IdempotencyKey: "record-binary-promo",
	})
	if recArtifact.Promotability != pb.Promotability_PROMOTABILITY_PROMOTABLE {
		t.Fatalf("expected PROMOTABILITY_PROMOTABLE, got %v", recArtifact.Promotability)
	}

	// Promote binary to dev
	pResp, err := f.promo.Promote(ctx, &pb.PromoteRequest{
		EnvironmentKey: "dev",
		OwnerFullName:  "demo-image-app",
		Kind:           pb.ArtifactKind_ARTIFACT_KIND_BINARY,
		Version:        "v1.0.0",
		IdempotencyKey: "promo-binary-dev",
	})
	if err != nil {
		t.Fatalf("Promote(binary): %v", err)
	}
	if pResp.Promotion.Version != "v1.0.0" {
		t.Fatalf("expected v1.0.0, got %s", pResp.Promotion.Version)
	}
	if pResp.Promotion.Digest != "sha256:binary-digest-1" {
		t.Fatalf("expected sha256:binary-digest-1, got %s", pResp.Promotion.Digest)
	}
}

// claimAllOutbox drains every pending/claimable outbox row from repo via
// the same WritebackRepository.ClaimBatch a real worker uses -- there is no
// dedicated "list" RPC for the outbox (it is an internal work queue, not a
// public API), so tests observe it exactly as the worker would. It leaves
// claimed rows in status 'claimed', not 'done' -- see drainOutboxToDone for
// a version that fully retires them.
func claimAllOutbox(t *testing.T, repo repository.Registry) []repository.WritebackOutbox {
	t.Helper()
	rows, err := repo.Writeback().ClaimBatch(context.Background(), "test-inspector", 100, 0)
	if err != nil {
		t.Fatalf("claim outbox batch: %v", err)
	}
	return rows
}

// drainOutboxToDone claims every outstanding outbox row and immediately
// marks each done, simulating a worker that started (and completed) a
// WritebackWorkflow for it -- used to reset the outbox to empty between
// test phases so a later assertion counts only rows written after this
// call.
func drainOutboxToDone(t *testing.T, repo repository.Registry) []repository.WritebackOutbox {
	t.Helper()
	ctx := context.Background()
	rows := claimAllOutbox(t, repo)
	for _, row := range rows {
		if err := repo.Writeback().MarkDone(ctx, row.OutboxID, row.PromotionID, "test-run"); err != nil {
			t.Fatalf("mark outbox row %s done: %v", row.OutboxID, err)
		}
	}
	return rows
}
