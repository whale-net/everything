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

// ThreadSummary is one research_thread plus its derived note count and
// most-recent note timestamp -- FR3's discovery shape. Mirrors
// IdeaSummary (idea.go).
type ThreadSummary struct {
	ResearchThread
	NoteCount    int
	LatestNoteAt *time.Time // nil when the thread has no notes yet.
}

// FindOrCreateThreadInput is the input to ThreadStore.FindOrCreate.
type FindOrCreateThreadInput struct {
	ChannelID         uuid.UUID
	IdeaID            *uuid.UUID // nil if the thread predates an Idea, same rule as ResearchNote.IdeaID.
	Title             string
	CreatedByPersonID uuid.UUID
}

// ThreadStore covers `research_thread` (migration 016, FR1; natural-key
// unique index added by migration 017, this task #1937).
type ThreadStore interface {
	// FindOrCreate converges on the natural key
	// (channel_id, idea_id, lower(trim(title))) -- FR4. Rejects (with an
	// error, no row created) if in.IdeaID is set but does not belong to
	// in.ChannelID -- the same cross-Channel rule video_script.go's
	// Propose enforces for VerdictID's idea (NFR2: one check, reused, not
	// a second copy).
	FindOrCreate(ctx context.Context, in FindOrCreateThreadInput) (ResearchThread, error)

	// GetByID returns the ResearchThread for id, or an error if none
	// exists.
	GetByID(ctx context.Context, id uuid.UUID) (ResearchThread, error)

	// ListByChannel returns FR3's discovery list for channelID,
	// most-recent-activity first (threads with no notes last), optionally
	// scoped to one Idea (ideaID nil = every thread on the Channel,
	// including idea_id IS NULL ones).
	ListByChannel(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID) ([]ThreadSummary, error)
}

// threadStore implements ThreadStore against `research_thread`
// (migration 016).
type threadStore struct{ pool *pgxpool.Pool }

var _ ThreadStore = threadStore{}

const threadColumns = `id, channel_id, idea_id, title, created_by_person_id, created_at`

func scanThread(row pgx.Row) (ResearchThread, error) {
	var t ResearchThread
	err := row.Scan(&t.ID, &t.ChannelID, &t.IdeaID, &t.Title, &t.CreatedByPersonID, &t.CreatedAt)
	return t, err
}

// FindOrCreate delegates to findOrCreateThreadTx against s.pool directly --
// see that function's doc comment for the natural-key convergence logic
// itself. Kept as a thin wrapper so ResearchStore.SaveNote (research.go,
// issue #1938) can call findOrCreateThreadTx against its OWN transaction
// instead (composing the thread find-or-create and the note INSERT into
// one atomic unit), which calling threadStore.FindOrCreate here -- bound
// to s.pool -- could never do.
func (s threadStore) FindOrCreate(ctx context.Context, in FindOrCreateThreadInput) (ResearchThread, error) {
	return findOrCreateThreadTx(ctx, s.pool, in)
}

// findOrCreateThreadTx normalizes title (trim, then compares case-insensitively)
// and converges on (channel_id, idea_id, lower(btrim(title))) via
// INSERT ... ON CONFLICT DO NOTHING against migration 017's partial
// unique index (research_thread_natural_key), unlike IdeaStore.
// FindOrCreate's advisory-lock workaround (idea.go) -- research_thread has
// a real unique index to upsert against, so no lock is needed here.
//
// The fallback lookup below (run only on conflict, i.e. only when the
// INSERT found a pre-existing row) deliberately uses
// idea_id IS NOT DISTINCT FROM $2, NOT idea_id = $2: research_thread.
// idea_id is nullable (a thread may predate an Idea), and SQL's
// `NULL = NULL` being unknown-not-true would otherwise make this lookup
// return pgx.ErrNoRows for the "note predates an Idea" bucket specifically
// -- even though the unique index itself (built on
// COALESCE(idea_id, <nil uuid>)) already correctly caught the conflict
// and prevented a duplicate row from ever being inserted. Losing that
// bucket's fallback lookup would surface as FindOrCreate returning a
// lookup error instead of the existing thread on the second and every
// subsequent call for the same (channel, NULL idea, title) -- silent
// breakage of convergence for a repeat caller, not a duplicate row. This
// is the single most likely thing to be silently regressed later; see the
// FindOrCreate test with IdeaID = nil for the regression coverage.
func findOrCreateThreadTx(ctx context.Context, q dbQueryRower, in FindOrCreateThreadInput) (ResearchThread, error) {
	trimmed := strings.TrimSpace(in.Title)
	if trimmed == "" {
		return ResearchThread{}, fmt.Errorf("title must not be empty")
	}

	if in.IdeaID != nil {
		var ideaChannelID uuid.UUID
		err := q.QueryRow(ctx, `SELECT channel_id FROM idea WHERE id = $1`, *in.IdeaID).Scan(&ideaChannelID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return ResearchThread{}, fmt.Errorf("idea %s does not exist", *in.IdeaID)
			}
			return ResearchThread{}, fmt.Errorf("lookup idea for thread: %w", err)
		}
		if ideaChannelID != in.ChannelID {
			return ResearchThread{}, fmt.Errorf("idea %s does not belong to channel %s", *in.IdeaID, in.ChannelID)
		}
	}

	thread, err := scanThread(q.QueryRow(ctx, `
		INSERT INTO research_thread (channel_id, idea_id, title, created_by_person_id)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (channel_id, COALESCE(idea_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(btrim(title)))
		DO NOTHING
		RETURNING `+threadColumns,
		in.ChannelID, in.IdeaID, trimmed, in.CreatedByPersonID))
	switch {
	case err == nil:
		return thread, nil
	case errors.Is(err, pgx.ErrNoRows):
		// A pre-existing row already occupies this natural key -- fall
		// through to the IS NOT DISTINCT FROM lookup above's doc comment.
	default:
		return ResearchThread{}, fmt.Errorf("insert research_thread: %w", err)
	}

	existing, err := scanThread(q.QueryRow(ctx, `
		SELECT `+threadColumns+`
		FROM research_thread
		WHERE channel_id = $1
		  AND idea_id IS NOT DISTINCT FROM $2
		  AND lower(btrim(title)) = lower(btrim($3))
		LIMIT 1
	`, in.ChannelID, in.IdeaID, trimmed))
	if err != nil {
		return ResearchThread{}, fmt.Errorf("lookup research_thread by natural key: %w", err)
	}
	return existing, nil
}

// GetByID returns the ResearchThread for id, or an error if none exists.
func (s threadStore) GetByID(ctx context.Context, id uuid.UUID) (ResearchThread, error) {
	return getThreadByIDTx(ctx, s.pool, id)
}

// getThreadByIDTx is GetByID's body, factored out so
// ResearchStore.SaveNote (research.go, issue #1938) can resolve a
// caller-supplied ThreadID inside its OWN transaction (q == the tx) --
// mirroring findOrCreateThreadTx's split above and isVideoScriptPublished's
// precedent (video_script.go).
func getThreadByIDTx(ctx context.Context, q dbQueryRower, id uuid.UUID) (ResearchThread, error) {
	thread, err := scanThread(q.QueryRow(ctx, `SELECT `+threadColumns+` FROM research_thread WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ResearchThread{}, pgx.ErrNoRows
		}
		return ResearchThread{}, fmt.Errorf("get research_thread by id: %w", err)
	}
	return thread, nil
}

// ListByChannel joins research_thread to research_note (via
// research_note.thread_id) in one LEFT JOIN/GROUP BY query -- no N+1
// query per thread -- to derive each thread's note count and most-recent
// note timestamp. ideaID nil returns every thread on channelID, including
// idea_id IS NULL ones; ideaID non-nil scopes to that Idea's threads only
// (a plain `=` is correct here, unlike FindOrCreate's lookup, because
// ideaID is always a concrete, non-NULL UUID in this branch -- there is no
// NULL-vs-NULL comparison to get wrong). Ordered most-recent-activity
// first (MAX(rn.created_at) DESC NULLS LAST), so a thread with no notes
// yet sorts last, tie-broken by the thread's own created_at.
func (s threadStore) ListByChannel(ctx context.Context, channelID uuid.UUID, ideaID *uuid.UUID) ([]ThreadSummary, error) {
	query := `
		SELECT
			rt.id, rt.channel_id, rt.idea_id, rt.title, rt.created_by_person_id, rt.created_at,
			COUNT(rn.id) AS note_count,
			MAX(rn.created_at) AS latest_note_at
		FROM research_thread rt
		LEFT JOIN research_note rn ON rn.thread_id = rt.id
		WHERE rt.channel_id = $1`
	args := []any{channelID}
	if ideaID != nil {
		args = append(args, *ideaID)
		query += fmt.Sprintf(" AND rt.idea_id = $%d", len(args))
	}
	query += `
		GROUP BY rt.id
		ORDER BY MAX(rn.created_at) DESC NULLS LAST, rt.created_at DESC`

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list research_thread by channel: %w", err)
	}
	defer rows.Close()

	var summaries []ThreadSummary
	for rows.Next() {
		var t ThreadSummary
		if err := rows.Scan(&t.ID, &t.ChannelID, &t.IdeaID, &t.Title, &t.CreatedByPersonID, &t.CreatedAt, &t.NoteCount, &t.LatestNoteAt); err != nil {
			return nil, fmt.Errorf("scan research_thread with stats: %w", err)
		}
		summaries = append(summaries, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list research_thread by channel: %w", err)
	}
	return summaries, nil
}
