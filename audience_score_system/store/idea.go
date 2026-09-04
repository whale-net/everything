package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// IdeaSummary is an Idea plus its research_note count and whether it has
// at least one viability_verdict row yet -- exactly what list_ideas
// (mcp/tools/research.go, issue #1577) renders.
type IdeaSummary struct {
	Idea
	NoteCount  int
	HasVerdict bool
}

// IdeaStore covers `idea` (migration 002) -- the stable identity LB3
// requires. See ARCHITECTURE.md / issue #1569 for the full
// idea -> verdict -> schedule -> outcome chain.
type IdeaStore interface {
	// Create inserts a new idea row for channelID, authored by
	// createdByPersonID. Unlike FindOrCreate, this always inserts -- kept
	// for callers (tests, future tools) that need an unconditional new
	// Idea rather than natural-key convergence.
	Create(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error)

	// FindOrCreate returns the existing Idea on channelID whose title
	// matches title case/whitespace-insensitively, or creates one
	// otherwise -- the create_idea MCP tool's natural-key upsert on
	// (channel_id, lower(title)) (issue #1577), so a retrying agent
	// converges on one Idea rather than forking identity.
	FindOrCreate(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error)

	// GetByID returns the Idea for id, or an error if none exists.
	GetByID(ctx context.Context, id uuid.UUID) (Idea, error)

	// ListByChannel returns every Idea for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]Idea, error)

	// ListByChannelWithStats returns Ideas for channelID (same
	// created_at-ascending ordering as ListByChannel) alongside their
	// research_note count and whether each has a viability_verdict yet --
	// one round trip via two LEFT JOINs rather than N+1 queries per Idea.
	// Bounded by since (inclusive lower bound on created_at, nil = no
	// bound) and limit (<=0 = unbounded; truncated reports whether more
	// matching rows exist beyond it) -- list_ideas' since/limit pagination
	// entirely in this layer (issue #1813's follow-up). Backs list_ideas
	// (mcp/tools/research.go, issue #1577).
	ListByChannelWithStats(ctx context.Context, channelID uuid.UUID, since *time.Time, limit int) (summaries []IdeaSummary, truncated bool, err error)
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

// FindOrCreate normalizes title (trim, then compares case-insensitively)
// and looks it up on channelID before inserting. `idea` carries no
// UNIQUE(channel_id, lower(title)) constraint to ON CONFLICT against (see
// migration 002 -- adding one is out of this task's "no new schema"
// scope), so a single transaction plus a transaction-scoped Postgres
// advisory lock keyed on the natural key serializes concurrent creates for
// the same (channel, title) instead: a racing second caller blocks on the
// lock, then finds the first caller's row on lookup rather than inserting
// a duplicate.
func (s ideaStore) FindOrCreate(ctx context.Context, channelID uuid.UUID, title string, createdByPersonID uuid.UUID) (Idea, error) {
	trimmed := strings.TrimSpace(title)

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Idea{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	lockKey := channelID.String() + ":" + strings.ToLower(trimmed)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
		return Idea{}, fmt.Errorf("lock idea natural key: %w", err)
	}

	existing, err := scanIdea(tx.QueryRow(ctx, `
		SELECT `+ideaColumns+`
		FROM idea
		WHERE channel_id = $1 AND lower(title) = lower($2)
		LIMIT 1
	`, channelID, trimmed))
	switch {
	case err == nil:
		if err := tx.Commit(ctx); err != nil {
			return Idea{}, fmt.Errorf("commit: %w", err)
		}
		return existing, nil
	case errors.Is(err, pgx.ErrNoRows):
		// No existing Idea with this natural key -- fall through to insert.
	default:
		return Idea{}, fmt.Errorf("lookup idea by natural key: %w", err)
	}

	idea, err := scanIdea(tx.QueryRow(ctx, `
		INSERT INTO idea (channel_id, title, created_by_person_id)
		VALUES ($1, $2, $3)
		RETURNING `+ideaColumns,
		channelID, trimmed, createdByPersonID))
	if err != nil {
		return Idea{}, fmt.Errorf("insert idea: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Idea{}, fmt.Errorf("commit: %w", err)
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

// ListByChannelWithStats returns Ideas for channelID (same created_at
// ordering as ListByChannel) alongside their research_note count and
// whether each has at least one viability_verdict row -- computed with two
// LEFT JOINs so this is one round trip rather than N+1 queries per Idea,
// bounded by since/limit (NULL-safe SQL parameters, see fetchLimit/
// paginate in pagination.go). Backs list_ideas (mcp/tools/research.go,
// issue #1577).
func (s ideaStore) ListByChannelWithStats(ctx context.Context, channelID uuid.UUID, since *time.Time, limit int) ([]IdeaSummary, bool, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			i.id, i.channel_id, i.title, i.created_by_person_id, i.created_at,
			COUNT(DISTINCT rn.id) AS note_count,
			COUNT(DISTINCT vv.id) > 0 AS has_verdict
		FROM idea i
		LEFT JOIN research_note rn ON rn.idea_id = i.id
		LEFT JOIN viability_verdict vv ON vv.idea_id = i.id
		WHERE i.channel_id = $1
		  AND ($2::timestamptz IS NULL OR i.created_at >= $2)
		GROUP BY i.id
		ORDER BY i.created_at
		LIMIT $3
	`, channelID, since, fetchLimit(limit))
	if err != nil {
		return nil, false, fmt.Errorf("list ideas with stats by channel: %w", err)
	}
	defer rows.Close()

	var summaries []IdeaSummary
	for rows.Next() {
		var s2 IdeaSummary
		if err := rows.Scan(&s2.ID, &s2.ChannelID, &s2.Title, &s2.CreatedByPersonID, &s2.CreatedAt, &s2.NoteCount, &s2.HasVerdict); err != nil {
			return nil, false, fmt.Errorf("scan idea with stats: %w", err)
		}
		summaries = append(summaries, s2)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list ideas with stats by channel: %w", err)
	}
	summaries, truncated := paginate(summaries, limit)
	return summaries, truncated, nil
}
