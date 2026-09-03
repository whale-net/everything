package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MatchStore covers `video_schedule_match` (migration 002, FR22/FR23) --
// the outcome link between a SyncedVideo and the ScheduleEntry it
// fulfilled.
type MatchStore interface {
	// Record inserts m (typically an 'auto' or 'pending' match produced by
	// the sync/matching worker).
	Record(ctx context.Context, m VideoScheduleMatch) error

	// ListPending returns every VideoScheduleMatch for channelID with
	// state MatchStatePending, awaiting human resolution.
	ListPending(ctx context.Context, channelID uuid.UUID) ([]VideoScheduleMatch, error)

	// Resolve sets matchID's state to MatchStateConfirmed (confirm==true)
	// or MatchStateRejected (confirm==false), stamping
	// resolved_by_person_id/resolved_at (FR23).
	Resolve(ctx context.Context, matchID, byPersonID uuid.UUID, confirm bool) error
}

// matchStore implements MatchStore against `video_schedule_match`
// (migration 002).
type matchStore struct{ pool *pgxpool.Pool }

var _ MatchStore = matchStore{}

// videoScheduleMatchColumns is qualified with the vsm. alias ListPending
// joins in -- Record/Resolve don't need a SELECT, so this is the only
// column list this file needs.
const videoScheduleMatchColumns = `vsm.id, vsm.synced_video_id, vsm.schedule_entry_id, vsm.confidence, vsm.state, vsm.resolved_by_person_id, vsm.resolved_at, vsm.created_at`

func scanVideoScheduleMatch(row pgx.Row) (VideoScheduleMatch, error) {
	var m VideoScheduleMatch
	err := row.Scan(&m.ID, &m.SyncedVideoID, &m.ScheduleEntryID, &m.Confidence, &m.State, &m.ResolvedByPersonID, &m.ResolvedAt, &m.CreatedAt)
	return m, err
}

// Record lets `id`/`created_at` take their column defaults rather than
// trusting a caller-supplied VideoScheduleMatch.ID -- the matching worker
// never needs the generated id back, only ListPending/Resolve do.
func (s matchStore) Record(ctx context.Context, m VideoScheduleMatch) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, schedule_entry_id, confidence, state, resolved_by_person_id, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, m.SyncedVideoID, m.ScheduleEntryID, m.Confidence, m.State, m.ResolvedByPersonID, m.ResolvedAt); err != nil {
		return fmt.Errorf("insert video_schedule_match: %w", err)
	}
	return nil
}

// ListPending joins synced_video for the channel_id filter --
// video_schedule_match itself carries no channel_id column.
func (s matchStore) ListPending(ctx context.Context, channelID uuid.UUID) ([]VideoScheduleMatch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+videoScheduleMatchColumns+`
		FROM video_schedule_match vsm
		JOIN synced_video sv ON sv.id = vsm.synced_video_id
		WHERE sv.channel_id = $1 AND vsm.state = $2
		ORDER BY vsm.created_at
	`, channelID, MatchStatePending)
	if err != nil {
		return nil, fmt.Errorf("list pending video_schedule_match: %w", err)
	}
	defer rows.Close()

	var matches []VideoScheduleMatch
	for rows.Next() {
		m, err := scanVideoScheduleMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan video_schedule_match: %w", err)
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending video_schedule_match: %w", err)
	}
	return matches, nil
}

func (s matchStore) Resolve(ctx context.Context, matchID, byPersonID uuid.UUID, confirm bool) error {
	state := MatchStateRejected
	if confirm {
		state = MatchStateConfirmed
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE video_schedule_match
		SET state = $1, resolved_by_person_id = $2, resolved_at = NOW()
		WHERE id = $3
	`, state, byPersonID, matchID)
	if err != nil {
		return fmt.Errorf("resolve video_schedule_match: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video_schedule_match %s: %w", matchID, pgx.ErrNoRows)
	}
	return nil
}
