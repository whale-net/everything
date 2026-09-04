package postgres

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// promotion_sync_event (migration 020, issue #1028, FR6, NFR4, NFR5) is the
// append-only ArgoCD sync/health observation log -- see that migration's
// doc comment for why it is a separate table from promotion_event rather
// than a new `action` enum value there. RecordSyncEvent/ListSyncEvents are
// implemented on *promotionRepo (promotion.go) alongside RecordEvent/
// ListEvents, since both tables share the promotion_id foreign key and this
// package's convention (see AppBuildLogRepository/artifact.go) is one
// receiver type per repository.*Repository interface, not per table.

const promotionSyncEventColumns = `sync_event_id, promotion_id, source, sync_status, health_status, operation_phase, occurred_at`

func scanPromotionSyncEvent(row pgx.Row) (repository.PromotionSyncEvent, error) {
	var e repository.PromotionSyncEvent
	if err := row.Scan(&e.SyncEventID, &e.PromotionID, &e.Source, &e.SyncStatus, &e.HealthStatus, &e.OperationPhase, &e.OccurredAt); err != nil {
		return repository.PromotionSyncEvent{}, err
	}
	return e, nil
}

// RecordSyncEvent implements repository.PromotionRepository.RecordSyncEvent
// -- append-only, mirroring RecordEvent's shape exactly (see that method):
// never an UPDATE or DELETE against promotion_sync_event (NFR4). An unknown
// e.PromotionID trips the promotion_id foreign key, translated via
// translatePgError into an error wrapping repository.ErrFailedPrecondition
// -- the same mapping every other FK-backed write path in this package uses
// (see postgres/errors.go's translatePgError doc comment).
func (r *promotionRepo) RecordSyncEvent(ctx context.Context, e repository.PromotionSyncEvent) (*repository.PromotionSyncEvent, error) {
	row := r.ex.QueryRow(ctx, `
		INSERT INTO promotion_sync_event (promotion_id, source, sync_status, health_status, operation_phase)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+promotionSyncEventColumns,
		e.PromotionID, e.Source, e.SyncStatus, e.HealthStatus, e.OperationPhase)
	created, err := scanPromotionSyncEvent(row)
	if err != nil {
		if de, ok := translatePgError(err, fmt.Sprintf("promotion sync event for promotion %s", e.PromotionID)); ok {
			return nil, de
		}
		return nil, fmt.Errorf("record promotion sync event for %s: %w", e.PromotionID, err)
	}
	return &created, nil
}

// ListSyncEvents implements
// repository.PromotionRepository.ListSyncEvents -- every row for
// promotionID, ordered occurred_at ASC (chronological transition history),
// no pagination (unlike ListEvents): this reads one promotion's transition
// timeline in full, not a cross-promotion audit feed, so the row count per
// call is bounded by how many times ArgoCD has been polled for a single
// promotion, not by overall system activity.
func (r *promotionRepo) ListSyncEvents(ctx context.Context, promotionID string) ([]repository.PromotionSyncEvent, error) {
	rows, err := r.ex.Query(ctx, `
		SELECT `+promotionSyncEventColumns+`
		FROM promotion_sync_event
		WHERE promotion_id = $1
		ORDER BY occurred_at ASC`, promotionID)
	if err != nil {
		return nil, fmt.Errorf("list promotion sync events for %s: %w", promotionID, err)
	}
	defer rows.Close()
	var out []repository.PromotionSyncEvent
	for rows.Next() {
		e, err := scanPromotionSyncEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
