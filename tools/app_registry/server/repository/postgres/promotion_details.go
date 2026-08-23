package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

// GetDetails implements repository.PromotionRepository.GetDetails (FR7-
// FR9, issue #1031) -- see repository.PromotionDetails' doc comment for
// the join this composes: promotion -> promotion_event (the creating
// event) -> artifact (to-version; from-version via the promotion this one
// superseded) -> writeback_outbox (#1029) -> promotion_sync_event (#1028's
// ListSyncEvents, reused directly below rather than re-querying).
//
// Every sub-read after the initial promotion lookup tolerates zero rows
// (pgx.ErrNoRows) as a legitimate, non-error state -- a promotion may not
// yet have a writeback_outbox row (Promote's enqueue happens in the same
// transaction, so in practice it always does, but this method must not
// assume that), and request_event is defensively tolerant of the same
// even though Promote/Rollback always write exactly one event alongside
// the promotion row.
func (r *promotionRepo) GetDetails(ctx context.Context, promotionID string) (*repository.PromotionDetails, error) {
	row := r.ex.QueryRow(ctx, promotionSelectBase+` WHERE p.promotion_id = $1`, promotionID)
	promotion, err := scanPromotion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, fmt.Errorf("%w: promotion %s", repository.ErrNotFound, promotionID)
		}
		return nil, fmt.Errorf("get promotion details for %s: read promotion: %w", promotionID, err)
	}

	// request_event: the promote/rollback event that created this
	// promotion row -- the earliest promotion_event row for this
	// promotion_id (approve/reject/retire events, if any exist in the
	// future, are always recorded strictly after the promotion itself was
	// created, never before).
	eventRow := r.ex.QueryRow(ctx, `SELECT `+promotionEventColumns+`
		FROM promotion_event pe
		WHERE pe.promotion_id = $1
		ORDER BY pe.occurred_at ASC LIMIT 1`, promotionID)
	requestEvent, err := scanPromotionEvent(eventRow)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get promotion details for %s: read request event: %w", promotionID, err)
	}

	// from_version: the version live for this same (environment_id,
	// target_key) immediately before THIS promotion's own valid_from --
	// correct regardless of whether promotionID itself is the current row
	// or a historical one, unlike GetPrevious (which always answers "most
	// recently superseded", not "superseded by promotionID specifically").
	// "" (zero rows) is a target's first-ever promotion, not an error.
	var fromVersion string
	if err := r.ex.QueryRow(ctx, `
		SELECT a.version
		FROM promotion p2
		JOIN artifact a ON a.artifact_id = p2.artifact_id
		WHERE p2.environment_id = $1 AND p2.target_key = $2 AND p2.valid_from < $3
		ORDER BY p2.valid_from DESC LIMIT 1`,
		promotion.EnvironmentID, promotion.TargetKey, promotion.ValidFrom).Scan(&fromVersion); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get promotion details for %s: read from_version: %w", promotionID, err)
	}

	// writeback_location/writeback_commit_sha: "" (zero rows, or an
	// unpublished/no-op row) is a legitimate "not yet published" state, not
	// an error -- see repository.PromotionDetails.WritebackCommitSHA's doc
	// comment (FR7a).
	var location, commitSHA string
	if err := r.ex.QueryRow(ctx, `
		SELECT location, commit_sha FROM writeback_outbox
		WHERE promotion_id = $1
		ORDER BY created_at DESC LIMIT 1`, promotionID).Scan(&location, &commitSHA); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, fmt.Errorf("get promotion details for %s: read writeback outbox: %w", promotionID, err)
	}

	syncEvents, err := r.ListSyncEvents(ctx, promotionID)
	if err != nil {
		return nil, fmt.Errorf("get promotion details for %s: list sync events: %w", promotionID, err)
	}
	outcome, currentSyncStatus, currentHealthStatus := repository.DerivePromotionSyncOutcome(syncEvents)

	return &repository.PromotionDetails{
		Promotion:           promotion,
		RequestEvent:        requestEvent,
		FromVersion:         fromVersion,
		ToVersion:           promotion.Version,
		WritebackLocation:   location,
		WritebackCommitSHA:  commitSHA,
		SyncEvents:          syncEvents,
		CurrentSyncStatus:   currentSyncStatus,
		CurrentHealthStatus: currentHealthStatus,
		Outcome:             outcome,
	}, nil
}
