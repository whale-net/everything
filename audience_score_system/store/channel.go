package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ChannelStore covers `channel` (migration 001).
type ChannelStore interface {
	// Create inserts a channel row plus its role=creator channel_person
	// row for creatorPersonID, atomically (one transaction) -- the only
	// way a Channel ever gets a creator (FR3, LB2).
	Create(ctx context.Context, youtubeChannelID, title string, creatorPersonID uuid.UUID) (Channel, error)

	// GetByID returns the Channel for id, or an error if none exists.
	GetByID(ctx context.Context, id uuid.UUID) (Channel, error)

	// SetConnectionState updates connection_state and stamps
	// connection_state_changed_at (FR4).
	SetConnectionState(ctx context.Context, channelID uuid.UUID, state ConnectionState) error

	// ListConnected returns every Channel with ConnectionState ==
	// ConnectionStateConnected, e.g. for the worker's per-Channel sync
	// schedule.
	ListConnected(ctx context.Context) ([]Channel, error)
}

// channelStore implements ChannelStore against `channel` and
// `channel_person` (migration 001).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1568's "Implementation" scope).
type channelStore struct{ pool *pgxpool.Pool }

var _ ChannelStore = channelStore{}

func (s channelStore) Create(ctx context.Context, youtubeChannelID, title string, creatorPersonID uuid.UUID) (Channel, error) {
	return Channel{}, errors.New("not implemented")
}

func (s channelStore) GetByID(ctx context.Context, id uuid.UUID) (Channel, error) {
	return Channel{}, errors.New("not implemented")
}

func (s channelStore) SetConnectionState(ctx context.Context, channelID uuid.UUID, state ConnectionState) error {
	return errors.New("not implemented")
}

func (s channelStore) ListConnected(ctx context.Context) ([]Channel, error) {
	return nil, errors.New("not implemented")
}
