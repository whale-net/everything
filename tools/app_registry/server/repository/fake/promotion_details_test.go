package fake

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestPromotionFake_GetDetails_AssemblesFullLifecycle proves the full FR7-
// FR9 assembly: promotion, request event (requester/request time),
// from/to version (first-ever promotion, so from_version == ""), writeback
// location/commit SHA, and the full sync_events history with its derived
// current status/outcome.
func TestPromotionFake_GetDetails_AssemblesFullLifecycle(t *testing.T) {
	r := New()
	ctx := context.Background()

	promotion := r.SeedPromotion(repository.Promotion{
		EnvironmentID: "env-1", TargetKey: "image:acme-widget", ArtifactID: "art-1", Version: "v1.0.0",
	})
	r.SeedPromotionEvent(repository.PromotionEvent{
		PromotionID: promotion.PromotionID, Action: repository.PromotionActionPromote, Actor: "alice", OccurredAt: promotion.ValidFrom,
	})

	outbox, err := r.Writeback().Enqueue(ctx, repository.WritebackOutbox{
		PromotionID: promotion.PromotionID, EnvironmentID: "env-1", EnvironmentKey: "dev", EventID: "evt-1", StateHash: "hash-1",
	})
	if err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := r.Writeback().RecordResult(ctx, outbox.OutboxID, promotion.PromotionID, "acme/widget/versions/dev.yaml", "deadbeef"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	if _, err := r.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourceRefreshTriggered,
	}); err != nil {
		t.Fatalf("RecordSyncEvent (refresh): %v", err)
	}
	if _, err := r.Promotions().RecordSyncEvent(ctx, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourcePollObserved, SyncStatus: "Synced", HealthStatus: "Healthy", OperationPhase: "Succeeded",
	}); err != nil {
		t.Fatalf("RecordSyncEvent (poll): %v", err)
	}

	details, err := r.Promotions().GetDetails(ctx, promotion.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if details.Promotion.PromotionID != promotion.PromotionID {
		t.Errorf("Promotion.PromotionID = %q, want %q", details.Promotion.PromotionID, promotion.PromotionID)
	}
	if details.RequestEvent.Actor != "alice" || details.RequestEvent.Action != repository.PromotionActionPromote {
		t.Errorf("RequestEvent = %+v, want actor=alice action=promote", details.RequestEvent)
	}
	if details.FromVersion != "" {
		t.Errorf("FromVersion = %q, want \"\" for a target's first-ever promotion", details.FromVersion)
	}
	if details.ToVersion != "v1.0.0" {
		t.Errorf("ToVersion = %q, want v1.0.0", details.ToVersion)
	}
	if details.WritebackLocation != "acme/widget/versions/dev.yaml" {
		t.Errorf("WritebackLocation = %q, want acme/widget/versions/dev.yaml", details.WritebackLocation)
	}
	if details.WritebackCommitSHA != "deadbeef" {
		t.Errorf("WritebackCommitSHA = %q, want deadbeef", details.WritebackCommitSHA)
	}
	if len(details.SyncEvents) != 2 {
		t.Fatalf("expected 2 sync events, got %d", len(details.SyncEvents))
	}
	if details.CurrentSyncStatus != "Synced" || details.CurrentHealthStatus != "Healthy" {
		t.Errorf("current status = (%q, %q), want (Synced, Healthy)", details.CurrentSyncStatus, details.CurrentHealthStatus)
	}
	if details.Outcome != repository.PromotionSyncOutcomeSyncedHealthy {
		t.Errorf("Outcome = %v, want SyncedHealthy", details.Outcome)
	}
}

// TestPromotionFake_GetDetails_WritebackNotYetPublished proves "" is the
// zero value for WritebackLocation/WritebackCommitSHA when no
// writeback_outbox row exists yet for the promotion (not an error).
func TestPromotionFake_GetDetails_WritebackNotYetPublished(t *testing.T) {
	r := New()
	promotion := r.SeedPromotion(repository.Promotion{EnvironmentID: "env-1", TargetKey: "image:acme-widget", Version: "v1.0.0"})

	details, err := r.Promotions().GetDetails(context.Background(), promotion.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if details.WritebackLocation != "" || details.WritebackCommitSHA != "" {
		t.Errorf("expected empty writeback location/commit sha before any publish, got (%q, %q)", details.WritebackLocation, details.WritebackCommitSHA)
	}
	if details.Outcome != repository.PromotionSyncOutcomePending {
		t.Errorf("Outcome = %v, want Pending with zero sync events", details.Outcome)
	}
}

// TestPromotionFake_GetDetails_FromVersion_ScopedToHistoricalPromotion
// proves FromVersion resolution is correct for a HISTORICAL (already-
// superseded) promotion_id, not just the current row -- the property that
// distinguishes GetDetails' from_version query from GetPrevious (which
// always answers "most recently superseded", regardless of which
// promotion_id was asked about).
func TestPromotionFake_GetDetails_FromVersion_ScopedToHistoricalPromotion(t *testing.T) {
	r := New()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	v1 := r.SeedPromotion(repository.Promotion{
		EnvironmentID: "env-1", TargetKey: "image:acme-widget", Version: "v1.0.0",
		ValidFrom: base, ValidTo: timePtr(base.Add(time.Hour)),
	})
	v2 := r.SeedPromotion(repository.Promotion{
		EnvironmentID: "env-1", TargetKey: "image:acme-widget", Version: "v2.0.0",
		ValidFrom: base.Add(time.Hour), ValidTo: timePtr(base.Add(2 * time.Hour)),
	})
	v3 := r.SeedPromotion(repository.Promotion{
		EnvironmentID: "env-1", TargetKey: "image:acme-widget", Version: "v3.0.0",
		ValidFrom: base.Add(2 * time.Hour),
	})

	// v1 is the target's first-ever promotion: "" even though v2/v3 exist
	// later in the same target's history.
	d1, err := r.Promotions().GetDetails(context.Background(), v1.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v1): %v", err)
	}
	if d1.FromVersion != "" {
		t.Errorf("v1.FromVersion = %q, want \"\" (first-ever promotion)", d1.FromVersion)
	}

	// v2 (itself now historical/superseded by v3) must resolve from_version
	// to v1 -- the promotion immediately before IT, not "whatever was most
	// recently superseded overall" (which would be v2 itself from v3's
	// perspective, an infinite-loop-shaped wrong answer if this used
	// GetPrevious).
	d2, err := r.Promotions().GetDetails(context.Background(), v2.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v2): %v", err)
	}
	if d2.FromVersion != "v1.0.0" {
		t.Errorf("v2.FromVersion = %q, want v1.0.0", d2.FromVersion)
	}

	// v3 (current) must resolve from_version to v2, not v1.
	d3, err := r.Promotions().GetDetails(context.Background(), v3.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v3): %v", err)
	}
	if d3.FromVersion != "v2.0.0" {
		t.Errorf("v3.FromVersion = %q, want v2.0.0", d3.FromVersion)
	}
}

// TestPromotionFake_GetDetails_UnknownPromotionID_NotFound proves an
// unknown promotion_id fails with repository.ErrNotFound, the error
// server/handlers/promotion.go's mapRepoErr translates to codes.NotFound.
func TestPromotionFake_GetDetails_UnknownPromotionID_NotFound(t *testing.T) {
	r := New()
	_, err := r.Promotions().GetDetails(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected an error for an unknown promotion_id, got nil")
	}
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}

func timePtr(t time.Time) *time.Time { return &t }
