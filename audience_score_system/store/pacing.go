package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PacingStore covers `pacing_policy` (migration 002, FR17). Natural key =
// Channel (channel_id UNIQUE, migration 002), so Upsert converges on
// repeated calls with identical values (NFR2 by construction).
type PacingStore interface {
	// Upsert finds or creates the PacingPolicy row for channelID, writing
	// p's fields either way.
	Upsert(ctx context.Context, channelID uuid.UUID, p PacingPolicy) (PacingPolicy, error)

	// Get returns the PacingPolicy for channelID, and false if none has
	// been set yet.
	Get(ctx context.Context, channelID uuid.UUID) (PacingPolicy, bool, error)
}

// pacingStore implements PacingStore against `pacing_policy` (migration
// 002).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type pacingStore struct{ pool *pgxpool.Pool }

var _ PacingStore = pacingStore{}

func (s pacingStore) Upsert(ctx context.Context, channelID uuid.UUID, p PacingPolicy) (PacingPolicy, error) {
	return PacingPolicy{}, errors.New("not implemented")
}

func (s pacingStore) Get(ctx context.Context, channelID uuid.UUID) (PacingPolicy, bool, error) {
	return PacingPolicy{}, false, errors.New("not implemented")
}
