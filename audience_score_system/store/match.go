package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type matchStore struct{ pool *pgxpool.Pool }

var _ MatchStore = matchStore{}

func (s matchStore) Record(ctx context.Context, m VideoScheduleMatch) error {
	return errors.New("not implemented")
}

func (s matchStore) ListPending(ctx context.Context, channelID uuid.UUID) ([]VideoScheduleMatch, error) {
	return nil, errors.New("not implemented")
}

func (s matchStore) Resolve(ctx context.Context, matchID, byPersonID uuid.UUID, confirm bool) error {
	return errors.New("not implemented")
}
