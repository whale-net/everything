package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveNoteInput is the input to ResearchStore.SaveNote.
type SaveNoteInput struct {
	ChannelID      uuid.UUID
	IdeaID         *uuid.UUID // nil if the note predates an Idea (FR9).
	Text           string
	SourceURL      *string // nil = uncited (FR10), never an empty string.
	AuthorPersonID uuid.UUID
	IdempotencyKey string
}

// ResearchStore covers `research_note` (migration 002, FR9/FR10).
type ResearchStore interface {
	// SaveNote inserts a research_note row, honouring IdempotencyKey
	// (NFR2): a replayed (author, key) pair must not create a duplicate
	// row.
	SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error)

	// ListByChannel returns every ResearchNote for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error)
}

// researchStore implements ResearchStore against `research_note`
// (migration 002).
type researchStore struct{ pool *pgxpool.Pool }

var _ ResearchStore = researchStore{}

const researchNoteColumns = `id, channel_id, idea_id, text, source_url, author_person_id, created_at, COALESCE(idempotency_key, '')`

func scanResearchNote(row pgx.Row) (ResearchNote, error) {
	var n ResearchNote
	err := row.Scan(&n.ID, &n.ChannelID, &n.IdeaID, &n.Text, &n.SourceURL, &n.AuthorPersonID, &n.CreatedAt, &n.IdempotencyKey)
	return n, err
}

// SaveNote checks for a prior row with the same (channel, author,
// idempotency_key) triple before inserting -- a replayed call with a
// non-empty IdempotencyKey returns the original row unchanged rather than
// creating a duplicate (NFR2).
func (s researchStore) SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error) {
	if in.IdempotencyKey != "" {
		existing, err := scanResearchNote(s.pool.QueryRow(ctx, `
			SELECT `+researchNoteColumns+`
			FROM research_note
			WHERE channel_id = $1 AND author_person_id = $2 AND idempotency_key = $3
		`, in.ChannelID, in.AuthorPersonID, in.IdempotencyKey))
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ResearchNote{}, fmt.Errorf("lookup research_note by idempotency key: %w", err)
		}
	}

	note, err := scanResearchNote(s.pool.QueryRow(ctx, `
		INSERT INTO research_note (channel_id, idea_id, text, source_url, author_person_id, idempotency_key)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''))
		RETURNING `+researchNoteColumns,
		in.ChannelID, in.IdeaID, in.Text, in.SourceURL, in.AuthorPersonID, in.IdempotencyKey))
	if err != nil {
		return ResearchNote{}, fmt.Errorf("insert research_note: %w", err)
	}
	return note, nil
}

func (s researchStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+researchNoteColumns+` FROM research_note WHERE channel_id = $1 ORDER BY created_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list research notes by channel: %w", err)
	}
	defer rows.Close()

	var notes []ResearchNote
	for rows.Next() {
		n, err := scanResearchNote(rows)
		if err != nil {
			return nil, fmt.Errorf("scan research_note: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list research notes by channel: %w", err)
	}
	return notes, nil
}
