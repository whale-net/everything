package fake

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// GetDetails mirrors postgres's promotionRepo.GetDetails (FR7-FR9, issue
// #1031) -- see repository.PromotionDetails' doc comment for the shape
// this assembles from f.r.state.
//
// Scaffold only -- a stub. Full implementation lands in the Implementation
// phase (issue #1031).
func (f promotionFake) GetDetails(ctx context.Context, promotionID string) (*repository.PromotionDetails, error) {
	return nil, errors.New("not implemented")
}
