package handlers

import (
	"testing"

	pb "github.com/whale-net/everything/tools/app_registry/protos"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestGetPromotionDetails_AssemblesFullLifecycle proves the handler's full
// FR7-FR9 wiring end to end: Promote (a CHART -- shouldEnqueueWriteback
// only enqueues writeback_outbox rows for CHART promotions, mirroring
// TestPromote_EnqueuesWritebackOutbox's own setup), populate the writeback
// outbox (#1029) and ArgoCD sync history (#1028) directly against the
// repository (standing in for RecordWritebackResult/TriggerArgoRefresh/
// PollArgoSyncStatus -- issue #1030's worker-side activities, out of scope
// here), then confirm GetPromotionDetails' response reflects all of it
// through promotionDetailsToPB.
func TestGetPromotionDetails_AssemblesFullLifecycle(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	promoted, err := f.promo.Promote(ctx, promoteReq("dev", "demo-achart", pb.ArtifactKind_ARTIFACT_KIND_CHART, "details-promo"))
	if err != nil {
		t.Fatalf("promote: %v", err)
	}

	rows := claimAllOutbox(t, f.repo)
	if len(rows) != 1 {
		t.Fatalf("expected exactly 1 outbox row, got %d", len(rows))
	}
	if err := f.repo.Writeback().RecordResult(ctx, rows[0].OutboxID, promoted.Promotion.PromotionId, "demo/achart/versions/dev.yaml", "cafed00d"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	if _, err := f.repo.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID: promoted.Promotion.PromotionId, Source: repository.PromotionSyncEventSourceRefreshTriggered,
	}); err != nil {
		t.Fatalf("RecordSyncEvent (refresh): %v", err)
	}
	if _, err := f.repo.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID: promoted.Promotion.PromotionId, Source: repository.PromotionSyncEventSourcePollObserved,
		SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded",
	}); err != nil {
		t.Fatalf("RecordSyncEvent (poll): %v", err)
	}

	resp, err := f.promo.GetPromotionDetails(ctx, &pb.GetPromotionDetailsRequest{PromotionId: promoted.Promotion.PromotionId})
	if err != nil {
		t.Fatalf("GetPromotionDetails: %v", err)
	}
	d := resp.Details
	if d.Promotion.PromotionId != promoted.Promotion.PromotionId {
		t.Errorf("Promotion.PromotionId = %q, want %q", d.Promotion.PromotionId, promoted.Promotion.PromotionId)
	}
	if d.RequestEvent == nil {
		t.Fatal("expected a non-nil RequestEvent")
	}
	if d.RequestEvent.Action != pb.PromotionAction_PROMOTION_ACTION_PROMOTE {
		t.Errorf("RequestEvent.Action = %v, want PROMOTE", d.RequestEvent.Action)
	}
	if d.FromVersion != "" {
		t.Errorf("FromVersion = %q, want \"\" for a target's first-ever promotion", d.FromVersion)
	}
	if d.ToVersion != promoted.Promotion.Version {
		t.Errorf("ToVersion = %q, want %q", d.ToVersion, promoted.Promotion.Version)
	}
	if d.WritebackLocation != "demo/achart/versions/dev.yaml" {
		t.Errorf("WritebackLocation = %q, want demo/achart/versions/dev.yaml", d.WritebackLocation)
	}
	if d.WritebackCommitSha != "cafed00d" {
		t.Errorf("WritebackCommitSha = %q, want cafed00d", d.WritebackCommitSha)
	}
	if len(d.SyncEvents) != 2 {
		t.Fatalf("expected 2 sync events, got %d", len(d.SyncEvents))
	}
	if d.SyncEvents[0].Source != repository.PromotionSyncEventSourceRefreshTriggered {
		t.Errorf("SyncEvents[0].Source = %q, want refresh_triggered (oldest first)", d.SyncEvents[0].Source)
	}
	if d.CurrentSyncStatus != "Synced" || d.CurrentHealthStatus != "Healthy" {
		t.Errorf("current status = (%q, %q), want (Synced, Healthy)", d.CurrentSyncStatus, d.CurrentHealthStatus)
	}
	if d.Outcome != pb.PromotionSyncOutcome_PROMOTION_SYNC_OUTCOME_SYNCED_HEALTHY {
		t.Errorf("Outcome = %v, want SYNCED_HEALTHY", d.Outcome)
	}
}

// TestGetPromotionDetails_FromVersion_SecondPromotion proves FromVersion
// resolves to the actually-superseded version through the full handler
// stack, mirroring TestPromote_PromoteAgain_ExactlyOneCurrentRow's setup.
func TestGetPromotionDetails_FromVersion_SecondPromotion(t *testing.T) {
	f := newPromotionFixture(t)
	ctx := authedCtx()

	first, err := f.promo.Promote(ctx, promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "details-from-1"))
	if err != nil {
		t.Fatalf("first promote: %v", err)
	}

	mustRecordArtifact(t, f.art, &pb.RecordArtifactRequest{
		BuildId: mustRecordBuild(t, f.art, "run-details-2").BuildId, Kind: pb.ArtifactKind_ARTIFACT_KIND_IMAGE,
		OwnerFullName: "demo-image-app", Digest: "sha256:imageapp-details-v2", Version: "v2.0.0",
		IdempotencyKey: "fixture-artifact-imageapp-details-v2",
	})
	req := promoteReq("dev", "demo-image-app", pb.ArtifactKind_ARTIFACT_KIND_IMAGE, "details-from-2")
	req.Version = "v2.0.0"
	second, err := f.promo.Promote(ctx, req)
	if err != nil {
		t.Fatalf("second promote: %v", err)
	}

	resp, err := f.promo.GetPromotionDetails(ctx, &pb.GetPromotionDetailsRequest{PromotionId: second.Promotion.PromotionId})
	if err != nil {
		t.Fatalf("GetPromotionDetails: %v", err)
	}
	if resp.Details.FromVersion != first.Promotion.Version {
		t.Errorf("FromVersion = %q, want %q (the superseded version)", resp.Details.FromVersion, first.Promotion.Version)
	}
	if resp.Details.ToVersion != "v2.0.0" {
		t.Errorf("ToVersion = %q, want v2.0.0", resp.Details.ToVersion)
	}
}

// TestGetPromotionDetails_UnknownPromotionID_NotFound proves mapRepoErr
// translates repository.ErrNotFound to codes.NotFound for this RPC.
func TestGetPromotionDetails_UnknownPromotionID_NotFound(t *testing.T) {
	f := newPromotionFixture(t)
	_, err := f.promo.GetPromotionDetails(authedCtx(), &pb.GetPromotionDetailsRequest{PromotionId: "nonexistent"})
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected codes.NotFound, got %v", err)
	}
}

// TestGetPromotionDetails_MissingPromotionID_InvalidArgument proves the
// request-validation guard added in the RPC's scaffold still holds now
// that the handler actually calls the repository.
func TestGetPromotionDetails_MissingPromotionID_InvalidArgument(t *testing.T) {
	f := newPromotionFixture(t)
	_, err := f.promo.GetPromotionDetails(authedCtx(), &pb.GetPromotionDetailsRequest{})
	if status.Code(err) != codes.InvalidArgument {
		t.Fatalf("expected codes.InvalidArgument, got %v", err)
	}
}
