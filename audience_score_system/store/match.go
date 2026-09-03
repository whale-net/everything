package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrMatchNotPending is returned by Resolve when matchID does not
// currently have state MatchStatePending -- either it does not exist, or a
// prior resolution (confirm or reject) already ran. Distinct from a plain
// "not found": resolving an already-resolved match must be rejected as a
// conflict (NFR2's replay-safety only covers the exact same idempotency
// key; a *different* call resolving an already-resolved match must never
// silently flip its state again).
var ErrMatchNotPending = errors.New("video_schedule_match: not pending (already resolved, or does not exist)")

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
	// resolved_by_person_id/resolved_at (FR23), and -- when scheduleEntryID
	// is non-nil -- overriding schedule_entry_id to it (a human confirming
	// against a different entry than the matcher's best guess). Returns
	// ErrMatchNotPending, with no state change, if matchID is not currently
	// MatchStatePending.
	Resolve(ctx context.Context, matchID, byPersonID uuid.UUID, confirm bool, scheduleEntryID *uuid.UUID) error

	// ListCandidates returns every MatchCandidate for channelID -- the
	// worker/sync matcher's candidate pool (FR22/FR23's "committed entries
	// with no existing live match").
	ListCandidates(ctx context.Context, channelID uuid.UUID) ([]MatchCandidate, error)

	// HasMatch reports whether syncedVideoID already has a
	// video_schedule_match row in ANY state (auto, pending, confirmed, OR
	// rejected). SyncOutcomes (worker/sync/outcomes.go) skips a video that
	// already has one -- matching is idempotent and never re-links or
	// duplicates; a rejected match does not by itself make its video
	// eligible for re-evaluation (default: rejected stays rejected, the
	// video stays unmatched, unless a future explicit re-queue tool clears
	// it -- not implemented in M1).
	HasMatch(ctx context.Context, syncedVideoID uuid.UUID) (bool, error)

	// GetByID returns the VideoScheduleMatch for id, or pgx.ErrNoRows if
	// none exists -- resolve_pending_match (mcp/tools/matches.go, issue
	// #1581) uses this both to validate a call's channel_id against the
	// match's video before Resolve runs, and to re-read the resolved row
	// for its response (WriteRender never trusts a cached value, per
	// mcp/server/registry.go's RegisterWrite).
	GetByID(ctx context.Context, id uuid.UUID) (VideoScheduleMatch, error)
}

// matchStore implements MatchStore against `video_schedule_match`
// (migration 002).
type matchStore struct{ pool *pgxpool.Pool }

var _ MatchStore = matchStore{}

// videoScheduleMatchColumns is qualified with the vsm. alias ListPending
// joins in -- Record/Resolve don't need a SELECT, so this is the only
// column list this file needs.
const videoScheduleMatchColumns = `vsm.id, vsm.synced_video_id, vsm.schedule_entry_id, vsm.confidence, vsm.state, vsm.resolved_by_person_id, vsm.resolved_at, vsm.created_at`

func scanVideoScheduleMatch(row pgx.Row) (VideoScheduleMatch, error) {
	var m VideoScheduleMatch
	err := row.Scan(&m.ID, &m.SyncedVideoID, &m.ScheduleEntryID, &m.Confidence, &m.State, &m.ResolvedByPersonID, &m.ResolvedAt, &m.CreatedAt)
	return m, err
}

// Record lets `id`/`created_at` take their column defaults rather than
// trusting a caller-supplied VideoScheduleMatch.ID -- the matching worker
// never needs the generated id back, only ListPending/Resolve do.
func (s matchStore) Record(ctx context.Context, m VideoScheduleMatch) error {
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO video_schedule_match (synced_video_id, schedule_entry_id, confidence, state, resolved_by_person_id, resolved_at)
		VALUES ($1, $2, $3, $4, $5, $6)
	`, m.SyncedVideoID, m.ScheduleEntryID, m.Confidence, m.State, m.ResolvedByPersonID, m.ResolvedAt); err != nil {
		return fmt.Errorf("insert video_schedule_match: %w", err)
	}
	return nil
}

// ListPending joins synced_video for the channel_id filter --
// video_schedule_match itself carries no channel_id column.
func (s matchStore) ListPending(ctx context.Context, channelID uuid.UUID) ([]VideoScheduleMatch, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+videoScheduleMatchColumns+`
		FROM video_schedule_match vsm
		JOIN synced_video sv ON sv.id = vsm.synced_video_id
		WHERE sv.channel_id = $1 AND vsm.state = $2
		ORDER BY vsm.created_at
	`, channelID, MatchStatePending)
	if err != nil {
		return nil, fmt.Errorf("list pending video_schedule_match: %w", err)
	}
	defer rows.Close()

	var matches []VideoScheduleMatch
	for rows.Next() {
		m, err := scanVideoScheduleMatch(rows)
		if err != nil {
			return nil, fmt.Errorf("scan video_schedule_match: %w", err)
		}
		matches = append(matches, m)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list pending video_schedule_match: %w", err)
	}
	return matches, nil
}

// Resolve only ever transitions a MatchStatePending row (the `WHERE ...
// AND state = 'pending'` below) -- zero rows affected means matchID either
// doesn't exist or is already resolved, either of which is
// ErrMatchNotPending, never a silent no-op re-flip. A confirm with a
// non-nil scheduleEntryID overrides schedule_entry_id to the human's
// explicit choice rather than leaving whatever best-guess entry (or NULL)
// Record originally wrote; a reject always leaves schedule_entry_id
// untouched (FR23: a rejected match's video stays unmatched).
func (s matchStore) Resolve(ctx context.Context, matchID, byPersonID uuid.UUID, confirm bool, scheduleEntryID *uuid.UUID) error {
	state := MatchStateRejected
	if confirm {
		state = MatchStateConfirmed
	}

	var tag pgconn.CommandTag
	var err error
	if confirm && scheduleEntryID != nil {
		tag, err = s.pool.Exec(ctx, `
			UPDATE video_schedule_match
			SET state = $1, schedule_entry_id = $2, resolved_by_person_id = $3, resolved_at = NOW()
			WHERE id = $4 AND state = $5
		`, state, *scheduleEntryID, byPersonID, matchID, MatchStatePending)
	} else {
		tag, err = s.pool.Exec(ctx, `
			UPDATE video_schedule_match
			SET state = $1, resolved_by_person_id = $2, resolved_at = NOW()
			WHERE id = $3 AND state = $4
		`, state, byPersonID, matchID, MatchStatePending)
	}
	if err != nil {
		return fmt.Errorf("resolve video_schedule_match: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("video_schedule_match %s: %w", matchID, ErrMatchNotPending)
	}
	return nil
}

// ListCandidates returns every committed schedule_entry for channelID with
// no existing live (auto/confirmed) video_schedule_match, joined with its
// bound idea's title -- the matcher's candidate pool (FR22/FR23).
func (s matchStore) ListCandidates(ctx context.Context, channelID uuid.UUID) ([]MatchCandidate, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT se.id, i.title, se.proposed_publish_at
		FROM schedule_entry se
		JOIN idea i ON i.id = se.idea_id
		WHERE se.channel_id = $1
		  AND se.state = 'committed'
		  AND NOT EXISTS (
		      SELECT 1 FROM video_schedule_match vsm
		      WHERE vsm.schedule_entry_id = se.id AND vsm.state IN ('auto', 'confirmed')
		  )
		ORDER BY se.proposed_publish_at
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list match candidates: %w", err)
	}
	defer rows.Close()

	var candidates []MatchCandidate
	for rows.Next() {
		var c MatchCandidate
		if err := rows.Scan(&c.ScheduleEntryID, &c.IdeaTitle, &c.ProposedPublishAt); err != nil {
			return nil, fmt.Errorf("scan match candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list match candidates: %w", err)
	}
	return candidates, nil
}

// HasMatch reports whether syncedVideoID has any video_schedule_match row
// at all, in any state -- see the MatchStore.HasMatch doc comment.
func (s matchStore) HasMatch(ctx context.Context, syncedVideoID uuid.UUID) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (SELECT 1 FROM video_schedule_match WHERE synced_video_id = $1)
	`, syncedVideoID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check video_schedule_match exists for synced_video %s: %w", syncedVideoID, err)
	}
	return exists, nil
}

// GetByID returns the VideoScheduleMatch for id, or pgx.ErrNoRows if none
// exists.
func (s matchStore) GetByID(ctx context.Context, id uuid.UUID) (VideoScheduleMatch, error) {
	m, err := scanVideoScheduleMatch(s.pool.QueryRow(ctx, `
		SELECT `+videoScheduleMatchColumns+`
		FROM video_schedule_match vsm
		WHERE vsm.id = $1
	`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return VideoScheduleMatch{}, pgx.ErrNoRows
		}
		return VideoScheduleMatch{}, fmt.Errorf("get video_schedule_match by id: %w", err)
	}
	return m, nil
}
