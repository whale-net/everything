package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
type ideaStore struct{ pool *pgxpool.Pool }

var _ IdeaStore = ideaStore{}

const ideaColumns = `id, channel_id, title, created_by_person_id, created_at`

func scanIdea(row pgx.Row) (Idea, error) {
	var i Idea
	err := row.Scan(&i.ID, &i.ChannelID, &i.Title, &i.CreatedByPersonID, &i.CreatedAt)
	return i, err
}

func (s ideaStore) Create(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error) {
	idea, err := scanIdea(s.pool.QueryRow(ctx, `
		INSERT INTO idea (channel_id, title, created_by_person_id)
		VALUES ($1, $2, $3)
		RETURNING `+ideaColumns,
		channelID, title, createdByPersonID))
	if err != nil {
		return Idea{}, fmt.Errorf("insert idea: %w", err)
	}
	return idea, nil
}

func (s ideaStore) GetByID(ctx context.Context, id uuid.UUID) (Idea, error) {
	idea, err := scanIdea(s.pool.QueryRow(ctx, `SELECT `+ideaColumns+` FROM idea WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Idea{}, pgx.ErrNoRows
		}
		return Idea{}, fmt.Errorf("get idea by id: %w", err)
	}
	return idea, nil
}

func (s ideaStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]Idea, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+ideaColumns+` FROM idea WHERE channel_id = $1 ORDER BY created_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list ideas by channel: %w", err)
	}
	defer rows.Close()

	var ideas []Idea
	for rows.Next() {
		i, err := scanIdea(rows)
		if err != nil {
			return nil, fmt.Errorf("scan idea: %w", err)
		}
		ideas = append(ideas, i)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list ideas by channel: %w", err)
	}
	return ideas, nil
}
