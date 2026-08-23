package fake

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// RecordSyncEvent/ListSyncEvents mirror postgres's promotionRepo (see
// postgres/promotion_sync_event.go). Scaffold only -- both methods below
// are stubs. Full implementation lands in the Implementation phase (issue
// #1028): RecordSyncEvent must check e.PromotionID against
// f.r.state.Promotions and fail with repository.ErrFailedPrecondition for
// an unknown promotion (the fake's stand-in for the real promotion_id
// foreign key postgres enforces for free); ListSyncEvents returns every
// PromotionSyncEvents row for promotionID sorted occurred_at ASC.

func (f promotionFake) RecordSyncEvent(ctx context.Context, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	return nil, errors.New("not implemented")
}

func (f promotionFake) ListSyncEvents(ctx context.Context, promotionID string) ([]repository.PromotionSyncEvent, error) {
	return nil, errors.New("not implemented")
}
