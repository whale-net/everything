package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdeaStore covers `idea` (migration 002) -- the stable identity LB3
// requires. See ARCHITECTURE.md / issue #1569 for the full
// idea -> verdict -> schedule -> outcome chain.
type IdeaStore interface {
	// Create inserts a new idea row for channelID, authored by
	// createdByPersonID.
	Create(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error)

	// GetByID returns the Idea for id, or an error if none exists.
	GetByID(ctx context.Context, id uuid.UUID) (Idea, error)

	// ListByChannel returns every Idea for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]Idea, error)
}

// ideaStore implements IdeaStore against `idea` (migration 002).
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type ideaStore struct{ pool *pgxpool.Pool }

var _ IdeaStore = ideaStore{}

func (s ideaStore) Create(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error) {
	return Idea{}, errors.New("not implemented")
}

func (s ideaStore) GetByID(ctx context.Context, id uuid.UUID) (Idea, error) {
	return Idea{}, errors.New("not implemented")
}

func (s ideaStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]Idea, error) {
	return nil, errors.New("not implemented")
}
