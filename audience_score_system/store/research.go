package store

import (
	"context"
	"errors"

	"github.com/google/uuid"
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
//
// Scaffold only -- every method below is a stub. Full implementation lands
// in the Implementation phase (issue #1569's "Implementation" scope).
type researchStore struct{ pool *pgxpool.Pool }

var _ ResearchStore = researchStore{}

func (s researchStore) SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error) {
	return ResearchNote{}, errors.New("not implemented")
}

func (s researchStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error) {
	return nil, errors.New("not implemented")
}
