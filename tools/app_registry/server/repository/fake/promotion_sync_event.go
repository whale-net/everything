package fake

import (
	"context"
	"fmt"
	"sort"

	"github.com/google/uuid"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// RecordSyncEvent mirrors postgres's promotionRepo.RecordSyncEvent
// (promotion_sync_event.go): append-only, never updates or deletes an
// existing entry. Unlike RecordEvent, this checks e.PromotionID against
// state.Promotions -- the postgres implementation gets that check for free
// from promotion_sync_event's promotion_id foreign key, so the fake has to
// do it explicitly to fail the same way (an error wrapping
// repository.ErrFailedPrecondition) for a promotion_id that was never
// promoted, matching the FK-violation mapping every other write path in
// this package's postgres sibling uses (see postgres/errors.go's
// translatePgError doc comment).
func (f promotionFake) RecordSyncEvent(ctx context.Context, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	if _, ok := f.r.state.Promotions[e.PromotionID]; !ok {
		return nil, fmt.Errorf("%w: promotion sync event references unknown promotion %s", repository.ErrFailedPrecondition, e.PromotionID)
	}
	e.SyncEventID = uuid.NewString()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = timeNow()
	}
	f.r.state.PromotionSyncEvents[e.SyncEventID] = e
	return &e, nil
}

// ListSyncEvents mirrors postgres's promotionRepo.ListSyncEvents: every row
// for promotionID, ordered occurred_at ASC -- see
// repository.PromotionRepository.ListSyncEvents's doc comment.
func (f promotionFake) ListSyncEvents(ctx context.Context, promotionID string) ([]repository.PromotionSyncEvent, error) {
	var out []repository.PromotionSyncEvent
	for _, e := range f.r.state.PromotionSyncEvents {
		if e.PromotionID != promotionID {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].OccurredAt.Equal(out[j].OccurredAt) {
			return out[i].OccurredAt.Before(out[j].OccurredAt)
		}
		return out[i].SyncEventID < out[j].SyncEventID
	})
	return out, nil
}
