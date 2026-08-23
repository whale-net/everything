package postgres

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// GetDetails implements repository.PromotionRepository.GetDetails (FR7-
// FR9, issue #1031) -- see repository.PromotionDetails' doc comment for
// the join this composes: promotion -> promotion_event (the creating
// event) -> artifact (to-version; from-version via the superseded
// promotion) -> writeback_outbox (#1029) -> promotion_sync_event (#1028's
// ListSyncEvents).
//
// Scaffold only -- a stub. Full implementation lands in the Implementation
// phase (issue #1031).
func (r *promotionRepo) GetDetails(ctx context.Context, promotionID string) (*repository.PromotionDetails, error) {
	return nil, errors.New("not implemented")
}
