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

// ResearchNoteWithAuthor is a ResearchNote plus its author's display name
// (joined from `person`) -- the shape list_research_notes
// (mcp/tools/research.go, issue #1577) renders, since a Person's display
// name is not a column on research_note itself.
type ResearchNoteWithAuthor struct {
	ResearchNote
	AuthorDisplayName string
}

// ResearchStore covers `research_note` (migration 002, FR9/FR10).
type ResearchStore interface {
	// SaveNote inserts a research_note row, honouring IdempotencyKey
	// (NFR2): a replayed (author, key) pair must not create a duplicate
	// row.
	SaveNote(ctx context.Context, in SaveNoteInput) (ResearchNote, error)

	// GetByID returns the ResearchNote for id, or an error if none exists.
	// Backs save_research_note's WriteRender step (mcp/tools/research.go),
	// which always re-reads from Postgres rather than caching what SaveNote
	// returned (LB4 -- see RegisterWrite's doc, ../mcp/server/registry.go).
	GetByID(ctx context.Context, id uuid.UUID) (ResearchNote, error)

	// ListByChannel returns every ResearchNote for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ResearchNote, error)

	// ListFiltered returns every ResearchNote for channelID, most-recent
	// first, each joined to its author's display name, optionally narrowed
	// to a single ideaID (nil = no filter) and/or partitioned by cited
	// (source_url IS NOT NULL, cited=true) vs uncited (source_url IS NULL,
	// cited=false, FR10). cited=nil means no cited/uncited filter. Backs
	// list_research_notes (mcp/tools/research.go, issue #1577).
	ListFiltered(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID, cited *bool) ([]ResearchNoteWithAuthor, error)
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

// GetByID returns the ResearchNote for id, or an error if none exists.
func (s researchStore) GetByID(ctx context.Context, id uuid.UUID) (ResearchNote, error) {
	note, err := scanResearchNote(s.pool.QueryRow(ctx, `SELECT `+researchNoteColumns+` FROM research_note WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResearchNote{}, pgx.ErrNoRows
		}
		return ResearchNote{}, fmt.Errorf("get research_note by id: %w", err)
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

// researchNoteWithAuthorColumns mirrors researchNoteColumns, qualified
// with the rn. alias ListFiltered's JOIN requires, plus the author's
// display name from `person`.
const researchNoteWithAuthorColumns = `rn.id, rn.channel_id, rn.idea_id, rn.text, rn.source_url, rn.author_person_id, rn.created_at, COALESCE(rn.idempotency_key, ''), COALESCE(p.display_name, '')`

func scanResearchNoteWithAuthor(row pgx.Row) (ResearchNoteWithAuthor, error) {
	var n ResearchNoteWithAuthor
	err := row.Scan(&n.ID, &n.ChannelID, &n.IdeaID, &n.Text, &n.SourceURL, &n.AuthorPersonID, &n.CreatedAt, &n.IdempotencyKey, &n.AuthorDisplayName)
	return n, err
}

// ListFiltered joins research_note to person for the author's display
// name, filters by channelID and optionally ideaID/cited, and orders
// most-recent first. ideaID nil means no Idea filter; cited nil means no
// cited/uncited filter -- callers (list_research_notes,
// mcp/tools/research.go) reject a request that sets both cited_only and
// uncited_only before calling this, so cited here is never ambiguous.
func (s researchStore) ListFiltered(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID, cited *bool) ([]ResearchNoteWithAuthor, error) {
	query := `
		SELECT ` + researchNoteWithAuthorColumns + `
		FROM research_note rn
		JOIN person p ON p.id = rn.author_person_id
		WHERE rn.channel_id = $1`
	args := []any{channelID}

	if ideaID != nil {
		args = append(args, *ideaID)
		query += fmt.Sprintf(" AND rn.idea_id = $%d", len(args))
	}
	if cited != nil {
		if *cited {
			query += " AND rn.source_url IS NOT NULL"
		} else {
			query += " AND rn.source_url IS NULL"
		}
	}
	query += " ORDER BY rn.created_at DESC"

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list research notes filtered: %w", err)
	}
	defer rows.Close()

	var notes []ResearchNoteWithAuthor
	for rows.Next() {
		n, err := scanResearchNoteWithAuthor(rows)
		if err != nil {
			return nil, fmt.Errorf("scan research_note with author: %w", err)
		}
		notes = append(notes, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list research notes filtered: %w", err)
	}
	return notes, nil
}
