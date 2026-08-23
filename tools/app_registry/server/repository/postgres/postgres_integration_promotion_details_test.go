//go:build integration

// Real-Postgres integration coverage for GetDetails (FR7-FR9, issue
// #1031): promotion_details.go. See postgres_integration_helpers_test.go's
// doc comment for why this package builds these files under the
// "integration" tag, and TESTING.md for how to run them.
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// TestPromotionDetails_AssemblesFullLifecycle proves the full FR7-FR9
// assembly against real Postgres: promotion, request event (requester/
// request time), from/to version (first-ever promotion, so from_version ==
// ""), writeback location/commit SHA (#1029), and the full sync_events
// history (#1028) with its derived current status/outcome.
func TestPromotionDetails_AssemblesFullLifecycle(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "details-widget", "image")
	buildID := seedBuild(t, pool, "run-details")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:details-v1", "v1.0.0")

	promotion, outbox, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: "image:acme-details-widget", ArtifactID: artifactID,
	}, "acme", "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}

	if err := reg.Writeback().RecordResult(ctx, outbox.OutboxID, promotion.PromotionID, "acme/details-widget/versions/dev.yaml", "deadbeefcafe"); err != nil {
		t.Fatalf("RecordResult: %v", err)
	}

	if _, err := recordSyncEventTx(t, reg, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourceRefreshTriggered,
	}); err != nil {
		t.Fatalf("RecordSyncEvent (refresh): %v", err)
	}
	if _, err := recordSyncEventTx(t, reg, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourcePollObserved,
		SyncStatus: "Synced", HealthStatus: "Healthy",
	}); err != nil {
		t.Fatalf("RecordSyncEvent (poll): %v", err)
	}

	details, err := reg.Promotions().GetDetails(ctx, promotion.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if details.Promotion.PromotionID != promotion.PromotionID {
		t.Errorf("Promotion.PromotionID = %q, want %q", details.Promotion.PromotionID, promotion.PromotionID)
	}
	if details.RequestEvent.Action != repository.PromotionActionPromote {
		t.Errorf("RequestEvent.Action = %v, want Promote", details.RequestEvent.Action)
	}
	if details.RequestEvent.Actor != "integration-test" {
		t.Errorf("RequestEvent.Actor = %q, want integration-test", details.RequestEvent.Actor)
	}
	if details.FromVersion != "" {
		t.Errorf("FromVersion = %q, want \"\" for a target's first-ever promotion", details.FromVersion)
	}
	if details.ToVersion != "v1.0.0" {
		t.Errorf("ToVersion = %q, want v1.0.0", details.ToVersion)
	}
	if details.WritebackLocation != "acme/details-widget/versions/dev.yaml" {
		t.Errorf("WritebackLocation = %q, want acme/details-widget/versions/dev.yaml", details.WritebackLocation)
	}
	if details.WritebackCommitSHA != "deadbeefcafe" {
		t.Errorf("WritebackCommitSHA = %q, want deadbeefcafe", details.WritebackCommitSHA)
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

// TestPromotionDetails_WritebackNotYetPublished proves "" is the correct
// state for WritebackLocation/WritebackCommitSHA (and PENDING for Outcome)
// immediately after Promote, before RecordResult/any ArgoCD activity has
// run -- the outbox row exists (Promote's own transaction enqueues it) but
// carries no location/commit_sha yet.
func TestPromotionDetails_WritebackNotYetPublished(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "pending-widget", "image")
	buildID := seedBuild(t, pool, "run-pending")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:pending-v1", "v1.0.0")

	promotion, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: "image:acme-pending-widget", ArtifactID: artifactID,
	}, "acme", "")
	if err != nil {
		t.Fatalf("promote+enqueue: %v", err)
	}

	details, err := reg.Promotions().GetDetails(ctx, promotion.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails: %v", err)
	}
	if details.WritebackLocation != "" || details.WritebackCommitSHA != "" {
		t.Errorf("expected empty writeback location/commit sha before RecordResult, got (%q, %q)", details.WritebackLocation, details.WritebackCommitSHA)
	}
	if details.Outcome != repository.PromotionSyncOutcomePending {
		t.Errorf("Outcome = %v, want Pending with zero sync events", details.Outcome)
	}
}

// TestPromotionDetails_FromVersion_ScopedToHistoricalPromotion proves
// FromVersion resolution is correct for a HISTORICAL (already-superseded)
// promotion_id against real Postgres, not just the current row -- the
// property that distinguishes GetDetails' from_version query from
// GetPrevious (see promotion_details.go's doc comment).
func TestPromotionDetails_FromVersion_ScopedToHistoricalPromotion(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "history-widget", "image")
	buildID := seedBuild(t, pool, "run-history")
	art1 := seedArtifact(t, pool, appID, buildID, "sha256:history-v1", "v1.0.0")
	art2 := seedArtifact(t, pool, appID, buildID, "sha256:history-v2", "v2.0.0")
	art3 := seedArtifact(t, pool, appID, buildID, "sha256:history-v3", "v3.0.0")
	targetKey := "image:acme-history-widget"

	v1, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art1}, "acme", "")
	if err != nil {
		t.Fatalf("promote v1: %v", err)
	}
	v2, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art2}, "acme", "")
	if err != nil {
		t.Fatalf("promote v2: %v", err)
	}
	v3, _, err := promoteWithOutboxTx(t, reg, repository.Promotion{EnvironmentID: envID, TargetKey: targetKey, ArtifactID: art3}, "acme", "")
	if err != nil {
		t.Fatalf("promote v3: %v", err)
	}

	d1, err := reg.Promotions().GetDetails(ctx, v1.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v1): %v", err)
	}
	if d1.FromVersion != "" {
		t.Errorf("v1.FromVersion = %q, want \"\" (first-ever promotion)", d1.FromVersion)
	}

	// v2 is now historical (superseded by v3): its from_version must still
	// resolve to v1 -- the promotion immediately before IT -- not
	// GetPrevious' "most recently superseded overall" answer, which from
	// v3's perspective would incorrectly be v2 itself.
	d2, err := reg.Promotions().GetDetails(ctx, v2.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v2): %v", err)
	}
	if d2.FromVersion != "v1.0.0" {
		t.Errorf("v2.FromVersion = %q, want v1.0.0", d2.FromVersion)
	}

	d3, err := reg.Promotions().GetDetails(ctx, v3.PromotionID)
	if err != nil {
		t.Fatalf("GetDetails(v3): %v", err)
	}
	if d3.FromVersion != "v2.0.0" {
		t.Errorf("v3.FromVersion = %q, want v2.0.0", d3.FromVersion)
	}
}

// TestPromotionDetails_UnknownPromotionID_NotFound proves an unknown
// promotion_id fails with repository.ErrNotFound against real Postgres.
func TestPromotionDetails_UnknownPromotionID_NotFound(t *testing.T) {
	reg, _ := newTestRegistry(t)
	_, err := reg.Promotions().GetDetails(context.Background(), "00000000-0000-0000-0000-000000000000")
	if err == nil {
		t.Fatal("expected an error for an unknown promotion_id, got nil")
	}
	if !errors.Is(err, repository.ErrNotFound) {
		t.Fatalf("expected repository.ErrNotFound, got %v", err)
	}
}
