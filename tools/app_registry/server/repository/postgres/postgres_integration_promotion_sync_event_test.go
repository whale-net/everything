//go:build integration

// Real-Postgres integration coverage for promotion_sync_event (migration
// 020, issue #1028, FR6, NFR4, NFR5): RecordSyncEvent/ListSyncEvents
// (promotion_sync_event.go). See postgres_integration_helpers_test.go's doc
// comment for why this package builds these files under the "integration"
// tag, and TESTING.md for how to run them.
package postgres

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// recordSyncEventTx runs Promotions().RecordSyncEvent inside a real WithTx
// transaction, mirroring how a caller (a later task's poll/retry activity)
// would invoke it in production.
func recordSyncEventTx(t *testing.T, reg *Registry, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	t.Helper()
	var out *repository.PromotionSyncEvent
	err := reg.WithTx(context.Background(), func(ctx context.Context, r repository.Registry) error {
		var ferr error
		out, ferr = r.Promotions().RecordSyncEvent(ctx, e)
		return ferr
	})
	return out, err
}

// TestPromotionSyncEvent_RecordAndList_ChronologicalOrder proves the
// insert+list round trip against real Postgres: RecordSyncEvent inserts a
// row scoped to a real promotion_id, and ListSyncEvents returns every row
// for that promotion in occurred_at ASC order across multiple inserts.
func TestPromotionSyncEvent_RecordAndList_ChronologicalOrder(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	envID := devEnvironmentID(t, reg)
	appID := seedApp(t, pool, "acme", "sync-widget", "image")
	buildID := seedBuild(t, pool, "run-sync-event")
	artifactID := seedArtifact(t, pool, appID, buildID, "sha256:sync-event", "v1.0.0")
	promotion, _, err := promoteTx(t, reg, repository.Promotion{
		EnvironmentID: envID, TargetKey: "image:acme-sync-widget", ArtifactID: artifactID,
	})
	if err != nil {
		t.Fatalf("seed promotion: %v", err)
	}

	first, err := recordSyncEventTx(t, reg, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID,
		Source:      repository.PromotionSyncEventSourceRefreshTriggered,
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent (first): %v", err)
	}
	if first.SyncEventID == "" {
		t.Fatalf("expected a generated SyncEventID")
	}

	second, err := recordSyncEventTx(t, reg, repository.PromotionSyncEvent{
		PromotionID:  promotion.PromotionID,
		Source:       repository.PromotionSyncEventSourcePollObserved,
		SyncStatus:   "Synced",
		HealthStatus: "Healthy",
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent (second): %v", err)
	}

	events, err := reg.Promotions().ListSyncEvents(ctx, promotion.PromotionID)
	if err != nil {
		t.Fatalf("ListSyncEvents: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("expected 2 sync events, got %d", len(events))
	}
	if events[0].SyncEventID != first.SyncEventID || events[1].SyncEventID != second.SyncEventID {
		t.Fatalf("expected chronological (occurred_at ASC) order [%s, %s], got [%s, %s]",
			first.SyncEventID, second.SyncEventID, events[0].SyncEventID, events[1].SyncEventID)
	}
	if events[1].SyncStatus != "Synced" || events[1].HealthStatus != "Healthy" {
		t.Fatalf("expected the second row's sync/health status to round-trip, got %+v", events[1])
	}

	// Row count must never exceed what was written by this test -- proves
	// nothing here has been touched by an UPDATE/DELETE (NFR4).
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM promotion_sync_event WHERE promotion_id = $1`, promotion.PromotionID).Scan(&count); err != nil {
		t.Fatalf("count promotion_sync_event rows: %v", err)
	}
	if count != 2 {
		t.Fatalf("expected exactly 2 committed promotion_sync_event rows, found %d", count)
	}
}

// TestPromotionSyncEvent_RecordSyncEvent_UnknownPromotionFailsFK proves
// RecordSyncEvent against an unknown promotion_id fails the promotion_id
// foreign key, surfaced as an error wrapping repository.ErrFailedPrecondition
// -- this package's existing FK-violation mapping convention (see
// postgres/errors.go's translatePgError).
func TestPromotionSyncEvent_RecordSyncEvent_UnknownPromotionFailsFK(t *testing.T) {
	reg, pool := newTestRegistry(t)
	ctx := context.Background()

	_, err := recordSyncEventTx(t, reg, repository.PromotionSyncEvent{
		PromotionID: "00000000-0000-0000-0000-000000000000",
		Source:      repository.PromotionSyncEventSourcePollObserved,
	})
	if err == nil {
		t.Fatal("expected the insert (nonexistent promotion_id) to fail on the promotion_id FK, got nil error")
	}
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected repository.ErrFailedPrecondition, got %v", err)
	}

	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM promotion_sync_event`).Scan(&count); err != nil {
		t.Fatalf("count promotion_sync_event rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("expected zero promotion_sync_event rows after the rejected insert, found %d", count)
	}
}
