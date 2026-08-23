package postgres

import (
	"context"
	"errors"

	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// promotion_sync_event (migration 020, issue #1028, FR6, NFR4, NFR5) is the
// append-only ArgoCD sync/health observation log -- see that migration's
// doc comment for why it is a separate table from promotion_event rather
// than a new `action` enum value there. RecordSyncEvent/ListSyncEvents are
// declared on *promotionRepo (promotion.go) alongside RecordEvent/
// ListEvents, since both tables share the promotion_id foreign key and this
// package's convention (see AppBuildLogRepository/artifact.go) is one
// receiver type per repository.*Repository interface, not per table.
//
// Scaffold only -- both methods below are stubs. Full implementation lands
// in the Implementation phase (issue #1028): an INSERT ... RETURNING
// mirroring RecordEvent's shape exactly, with an unknown promotion_id
// translated via translatePgError (errors.go) into
// repository.ErrFailedPrecondition; and a plain `SELECT ... ORDER BY
// occurred_at ASC` for ListSyncEvents (no pagination, unlike ListEvents --
// this reads one promotion's own transition timeline, not a cross-promotion
// audit feed).

func (r *promotionRepo) RecordSyncEvent(ctx context.Context, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	return nil, errors.New("not implemented")
}

func (r *promotionRepo) ListSyncEvents(ctx context.Context, promotionID string) ([]repository.PromotionSyncEvent, error) {
	return nil, errors.New("not implemented")
}
