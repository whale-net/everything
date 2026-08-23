package fake

import (
	"context"
	"errors"
	"testing"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// recordSyncEventTx runs Promotions().RecordSyncEvent inside a real WithTx
// transaction, mirroring how a caller (a later task's poll/retry activity)
// would invoke it in production.
func recordSyncEventTx(t *testing.T, r *Registry, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	t.Helper()
	var out *repository.PromotionSyncEvent
	err := r.WithTx(context.Background(), func(ctx context.Context, reg repository.Registry) error {
		var ferr error
		out, ferr = reg.Promotions().RecordSyncEvent(ctx, e)
		return ferr
	})
	return out, err
}

// TestPromotionSyncEventFake_RecordAndList_ChronologicalOrder proves the
// insert+list round trip: RecordSyncEvent writes a row scoped to a real
// promotion_id, and ListSyncEvents returns every row for that promotion in
// occurred_at ASC order across multiple inserts -- the read shape a later
// task's Promotion Details path depends on.
func TestPromotionSyncEventFake_RecordAndList_ChronologicalOrder(t *testing.T) {
	r := New()
	promotion := r.SeedPromotion(repository.Promotion{EnvironmentID: "env-1", TargetKey: "image:acme-widget"})

	first, err := recordSyncEventTx(t, r, repository.PromotionSyncEvent{
		PromotionID: promotion.PromotionID,
		Source:      repository.PromotionSyncEventSourceRefreshTriggered,
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent (first): %v", err)
	}
	if first.SyncEventID == "" {
		t.Fatalf("expected a generated SyncEventID")
	}

	second, err := recordSyncEventTx(t, r, repository.PromotionSyncEvent{
		PromotionID:  promotion.PromotionID,
		Source:       repository.PromotionSyncEventSourcePollObserved,
		SyncStatus:   "Synced",
		HealthStatus: "Healthy",
	})
	if err != nil {
		t.Fatalf("RecordSyncEvent (second): %v", err)
	}
	if second.SyncEventID == first.SyncEventID {
		t.Fatalf("expected a distinct SyncEventID for the second write")
	}
	// The write path must never mutate rows written earlier -- prove the
	// first row wasn't touched by the second insert (NFR4: append-only).
	if first.OccurredAt.After(second.OccurredAt) {
		t.Fatalf("expected first.OccurredAt (%v) to be before or equal to second.OccurredAt (%v)", first.OccurredAt, second.OccurredAt)
	}

	events, err := r.Promotions().ListSyncEvents(context.Background(), promotion.PromotionID)
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
}

// TestPromotionSyncEventFake_RecordSyncEvent_UnknownPromotionRejected mirrors
// the Postgres-backed promotion_id foreign key: a sync event referencing a
// promotion_id that was never promoted must fail with an error wrapping
// repository.ErrFailedPrecondition, matching this package's FK-violation
// mapping convention (see postgres/errors.go's translatePgError).
func TestPromotionSyncEventFake_RecordSyncEvent_UnknownPromotionRejected(t *testing.T) {
	r := New()

	_, err := recordSyncEventTx(t, r, repository.PromotionSyncEvent{
		PromotionID: "00000000-0000-0000-0000-000000000000",
		Source:      repository.PromotionSyncEventSourcePollObserved,
	})
	if err == nil {
		t.Fatal("expected an error for a sync event referencing an unknown promotion_id, got nil")
	}
	if !errors.Is(err, repository.ErrFailedPrecondition) {
		t.Fatalf("expected repository.ErrFailedPrecondition, got %v", err)
	}
}

// TestPromotionSyncEventFake_ListSyncEvents_DeterministicTieBreak proves
// ListSyncEvents' ordering is deterministic even when two rows share the
// exact same OccurredAt: the SyncEventID is the tie-break (ascending),
// mirroring postgres's own occurred_at ASC scan which has no stable order
// without a second sort key on a duplicate timestamp.
func TestPromotionSyncEventFake_ListSyncEvents_DeterministicTieBreak(t *testing.T) {
	r := New()
	promotion := r.SeedPromotion(repository.Promotion{EnvironmentID: "env-1", TargetKey: "image:acme-tie"})

	tie := promotion.ValidFrom
	// Seed directly (bypassing RecordSyncEvent's clock stamp) so both rows
	// share an identical OccurredAt -- the case a naive "insertion order"
	// implementation would get right by accident but a map-iteration-order
	// implementation (Go maps have no defined iteration order) would not.
	a := repository.PromotionSyncEvent{SyncEventID: "b-event", PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourcePollObserved, OccurredAt: tie}
	b := repository.PromotionSyncEvent{SyncEventID: "a-event", PromotionID: promotion.PromotionID, Source: repository.PromotionSyncEventSourcePollObserved, OccurredAt: tie}
	r.state.PromotionSyncEvents[a.SyncEventID] = a
	r.state.PromotionSyncEvents[b.SyncEventID] = b

	for i := 0; i < 5; i++ {
		events, err := r.Promotions().ListSyncEvents(context.Background(), promotion.PromotionID)
		if err != nil {
			t.Fatalf("ListSyncEvents (iteration %d): %v", i, err)
		}
		if len(events) != 2 {
			t.Fatalf("expected 2 sync events, got %d", len(events))
		}
		if events[0].SyncEventID != "a-event" || events[1].SyncEventID != "b-event" {
			t.Fatalf("iteration %d: expected tie-broken order [a-event, b-event], got [%s, %s]", i, events[0].SyncEventID, events[1].SyncEventID)
		}
	}
}
