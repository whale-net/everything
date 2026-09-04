package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SaveDraftInput is the input to ScheduleStore.SaveDraft.
type SaveDraftInput struct {
	ChannelID         uuid.UUID
	IdeaID            uuid.UUID
	VerdictID         uuid.UUID // must reference a VerdictViable row (FR16).
	ProposedPublishAt time.Time
	CreatedByPersonID uuid.UUID
	IdempotencyKey    string
}

// ErrScheduleEntryPublished is returned by Approve/Unapprove/Update once
// IsPublished holds for the target entry (FR20's freeze, issue #1580: a
// live match to a published video). Checked atomically, inside the same
// transaction as the attempted mutation, so a racing SyncStore write can
// never slip a mutation through between the check and the UPDATE.
var ErrScheduleEntryPublished = errors.New("schedule_entry is frozen: its video has already been published")

// ScheduleStore covers `schedule_entry` (migration 002, FR16-FR20). Every
// row's VerdictID is the FK to the specific Verdict version that judged
// the Idea viable (LB3) -- SaveDraft rejects any verdict that is not
// VerdictViable.
type ScheduleStore interface {
	// SaveDraft inserts a draft schedule_entry. Returns ErrVerdictNotViable
	// without writing anything if in.VerdictID's verdict is not
	// VerdictViable (FR16).
	SaveDraft(ctx context.Context, in SaveDraftInput) (ScheduleEntry, error)

	// Approve transitions entryID from draft to committed, stamping
	// approved_by_person_id/approved_at (FR19, requires CanApprove).
	// Returns ErrScheduleEntryPublished, with no state change, if
	// IsPublished already holds for entryID (FR20).
	Approve(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Unapprove reverses Approve, transitioning entryID back to draft and
	// clearing approved_by_person_id/approved_at (FR20). Returns
	// ErrScheduleEntryPublished, with no state change, if IsPublished
	// already holds for entryID (FR20's freeze).
	Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Update changes a draft entry's proposed_publish_at (FR18's pacing
	// adjustments, FR20's edit route). Returns ErrScheduleEntryPublished,
	// with no state change, if IsPublished already holds for entryID.
	Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error

	// ListByChannel returns every ScheduleEntry for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntry, error)

	// GetByID returns the ScheduleEntry for id, or pgx.ErrNoRows if none
	// exists. Backs save_schedule_draft's WriteRender step
	// (mcp/tools/schedule_draft.go, issue #1579), which always re-reads
	// from Postgres rather than caching what SaveDraft returned (LB4).
	GetByID(ctx context.Context, id uuid.UUID) (ScheduleEntry, error)

	// IsPublished reports whether entryID has a live (auto or confirmed)
	// video_schedule_match row whose synced_video.published_at is
	// non-null -- FR20's single, reusable "recorded as published" freeze
	// predicate (issue #1580, C8). Approve/Unapprove/Update above consult
	// this internally (atomically, in the same transaction as the
	// mutation); `web`'s schedule page also calls it directly to decide
	// whether to render the un-approve/edit affordances at all. A match
	// that exists but is `pending` or `rejected`, or one whose video has
	// not published yet, does NOT freeze the entry -- only a live match
	// to an actually-published video does.
	IsPublished(ctx context.Context, entryID uuid.UUID) (bool, error)

	// ListDetailByChannel returns every ScheduleEntryDetail for
	// channelID, ordered by proposed_publish_at -- `web`'s GET
	// /channels/{id}/schedule read (issue #1580, C8: FR19/FR20).
	ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntryDetail, error)
}

// scheduleStore implements ScheduleStore against `schedule_entry`
// (migration 002).
type scheduleStore struct{ pool *pgxpool.Pool }

var _ ScheduleStore = scheduleStore{}

const scheduleEntryColumns = `id, channel_id, idea_id, verdict_id, proposed_publish_at, state, approved_by_person_id, approved_at, created_by_person_id, created_at, updated_at, COALESCE(idempotency_key, '')`

// scheduleEntryColumnsAliased is scheduleEntryColumns qualified with the
// se. alias ListDetailByChannel's multi-table join needs (mirroring
// match.go's videoScheduleMatchColumns/vsm. pattern).
const scheduleEntryColumnsAliased = `se.id, se.channel_id, se.idea_id, se.verdict_id, se.proposed_publish_at, se.state, se.approved_by_person_id, se.approved_at, se.created_by_person_id, se.created_at, se.updated_at, COALESCE(se.idempotency_key, '')`

func scanScheduleEntry(row pgx.Row) (ScheduleEntry, error) {
	var e ScheduleEntry
	err := row.Scan(&e.ID, &e.ChannelID, &e.IdeaID, &e.VerdictID, &e.ProposedPublishAt, &e.State, &e.ApprovedByPersonID, &e.ApprovedAt, &e.CreatedByPersonID, &e.CreatedAt, &e.UpdatedAt, &e.IdempotencyKey)
	return e, err
}

// SaveDraft honours IdempotencyKey (a replayed (channel, author, key)
// triple returns the original row unchanged); when no key is supplied, it
// instead falls back to the (channel_id, idea_id, proposed_publish_at)
// natural key (NFR2) -- a keyless retry with the identical triple
// converges on the already-persisted row rather than inserting a
// duplicate, the same "lock the natural key, look up, insert if absent"
// idiom IdeaStore.FindOrCreate uses. Either way, -- inside the same
// transaction -- SaveDraft then looks up in.VerdictID's verdict and
// rejects anything but VerdictViable with ErrVerdictNotViable before
// inserting (FR16, LB3).
func (s scheduleStore) SaveDraft(ctx context.Context, in SaveDraftInput) (ScheduleEntry, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return ScheduleEntry{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if in.IdempotencyKey != "" {
		existing, err := scanScheduleEntry(tx.QueryRow(ctx, `
			SELECT `+scheduleEntryColumns+`
			FROM schedule_entry
			WHERE channel_id = $1 AND created_by_person_id = $2 AND idempotency_key = $3
		`, in.ChannelID, in.CreatedByPersonID, in.IdempotencyKey))
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return ScheduleEntry{}, fmt.Errorf("commit: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ScheduleEntry{}, fmt.Errorf("lookup schedule_entry by idempotency key: %w", err)
		}
	} else {
		// No idempotency_key -- fall back to the (channel_id, idea_id,
		// proposed_publish_at) natural key. The advisory lock serializes a
		// racing duplicate call for the identical triple the same way
		// IdeaStore.FindOrCreate's lock does for (channel_id, lower(title)).
		lockKey := in.ChannelID.String() + ":" + in.IdeaID.String() + ":" + in.ProposedPublishAt.UTC().Format(time.RFC3339Nano)
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, lockKey); err != nil {
			return ScheduleEntry{}, fmt.Errorf("lock schedule_entry natural key: %w", err)
		}

		existing, err := scanScheduleEntry(tx.QueryRow(ctx, `
			SELECT `+scheduleEntryColumns+`
			FROM schedule_entry
			WHERE channel_id = $1 AND idea_id = $2 AND proposed_publish_at = $3
			ORDER BY created_at
			LIMIT 1
		`, in.ChannelID, in.IdeaID, in.ProposedPublishAt))
		if err == nil {
			if err := tx.Commit(ctx); err != nil {
				return ScheduleEntry{}, fmt.Errorf("commit: %w", err)
			}
			return existing, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return ScheduleEntry{}, fmt.Errorf("lookup schedule_entry by natural key: %w", err)
		}
	}

	var verdict VerdictValue
	err = tx.QueryRow(ctx, `SELECT verdict FROM viability_verdict WHERE id = $1`, in.VerdictID).Scan(&verdict)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ScheduleEntry{}, fmt.Errorf("verdict %s: %w", in.VerdictID, pgx.ErrNoRows)
		}
		return ScheduleEntry{}, fmt.Errorf("lookup verdict for schedule draft: %w", err)
	}
	if verdict != VerdictViable {
		return ScheduleEntry{}, ErrVerdictNotViable
	}

	entry, err := scanScheduleEntry(tx.QueryRow(ctx, `
		INSERT INTO schedule_entry (channel_id, idea_id, verdict_id, proposed_publish_at, state, created_by_person_id, idempotency_key)
		VALUES ($1, $2, $3, $4, 'draft', $5, NULLIF($6, ''))
		RETURNING `+scheduleEntryColumns,
		in.ChannelID, in.IdeaID, in.VerdictID, in.ProposedPublishAt, in.CreatedByPersonID, in.IdempotencyKey))
	if err != nil {
		return ScheduleEntry{}, fmt.Errorf("insert schedule_entry: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return ScheduleEntry{}, fmt.Errorf("commit: %w", err)
	}
	return entry, nil
}

func (s scheduleStore) Approve(ctx context.Context, entryID, byPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	published, err := isPublished(ctx, tx, entryID)
	if err != nil {
		return err
	}
	if published {
		return ErrScheduleEntryPublished
	}

	tag, err := tx.Exec(ctx, `
		UPDATE schedule_entry
		SET state = 'committed', approved_by_person_id = $1, approved_at = NOW(), updated_at = NOW()
		WHERE id = $2 AND state = 'draft'
	`, byPersonID, entryID)
	if err != nil {
		return fmt.Errorf("approve schedule_entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("schedule_entry %s not a draft: %w", entryID, pgx.ErrNoRows)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s scheduleStore) Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	published, err := isPublished(ctx, tx, entryID)
	if err != nil {
		return err
	}
	if published {
		return ErrScheduleEntryPublished
	}

	tag, err := tx.Exec(ctx, `
		UPDATE schedule_entry
		SET state = 'draft', approved_by_person_id = NULL, approved_at = NULL, updated_at = NOW()
		WHERE id = $1 AND state = 'committed'
	`, entryID)
	if err != nil {
		return fmt.Errorf("unapprove schedule_entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("schedule_entry %s not committed: %w", entryID, pgx.ErrNoRows)
	}
	_ = byPersonID // who reversed the approval is the caller's audit trail (authz.go's CanApprove); not persisted on schedule_entry itself.
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s scheduleStore) Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	published, err := isPublished(ctx, tx, entryID)
	if err != nil {
		return err
	}
	if published {
		return ErrScheduleEntryPublished
	}

	tag, err := tx.Exec(ctx, `
		UPDATE schedule_entry
		SET proposed_publish_at = $1, updated_at = NOW()
		WHERE id = $2 AND state = 'draft'
	`, proposedPublishAt, entryID)
	if err != nil {
		return fmt.Errorf("update schedule_entry: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("schedule_entry %s not a draft: %w", entryID, pgx.ErrNoRows)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

func (s scheduleStore) GetByID(ctx context.Context, id uuid.UUID) (ScheduleEntry, error) {
	e, err := scanScheduleEntry(s.pool.QueryRow(ctx, `SELECT `+scheduleEntryColumns+` FROM schedule_entry WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ScheduleEntry{}, pgx.ErrNoRows
		}
		return ScheduleEntry{}, fmt.Errorf("get schedule_entry by id: %w", err)
	}
	return e, nil
}

func (s scheduleStore) ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntry, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+scheduleEntryColumns+` FROM schedule_entry WHERE channel_id = $1 ORDER BY proposed_publish_at`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list schedule entries by channel: %w", err)
	}
	defer rows.Close()

	var entries []ScheduleEntry
	for rows.Next() {
		e, err := scanScheduleEntry(rows)
		if err != nil {
			return nil, fmt.Errorf("scan schedule_entry: %w", err)
		}
		entries = append(entries, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schedule entries by channel: %w", err)
	}
	return entries, nil
}

// dbQueryRower is the subset of *pgxpool.Pool and pgx.Tx isPublished
// needs -- lets Approve/Unapprove/Update run the same freeze check
// against their own transaction (atomically with the mutation) that
// ScheduleStore.IsPublished runs directly against the pool.
type dbQueryRower interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// isPublished is IsPublished's shared body, factored out so Approve/
// Unapprove/Update can run it inside their own transaction (q == the tx)
// while ScheduleStore.IsPublished runs it directly against the pool
// (q == s.pool) -- exactly one query defines "recorded as published"
// (FR20).
func isPublished(ctx context.Context, q dbQueryRower, entryID uuid.UUID) (bool, error) {
	var published bool
	err := q.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM video_schedule_match vsm
			JOIN synced_video sv ON sv.id = vsm.synced_video_id
			WHERE vsm.schedule_entry_id = $1
			  AND vsm.state IN ('auto', 'confirmed')
			  AND sv.published_at IS NOT NULL
		)
	`, entryID).Scan(&published)
	if err != nil {
		return false, fmt.Errorf("check schedule_entry %s published: %w", entryID, err)
	}
	return published, nil
}

func (s scheduleStore) IsPublished(ctx context.Context, entryID uuid.UUID) (bool, error) {
	return isPublished(ctx, s.pool, entryID)
}

// ListDetailByChannel joins schedule_entry with its bound idea (title),
// its bound viability_verdict version/value/reasoning, the approver's
// person row (display name), and -- via a LATERAL subquery mirroring
// migration 002's v_prediction_vs_outcome -- the single best live/
// published video_schedule_match, if any (preferring a confirmed match
// over an auto one, then the most recent). The LATERAL join (rather than
// a plain LEFT JOIN on video_schedule_match/synced_video) guards against
// row multiplication: nothing in the schema prevents more than one live
// match row per schedule_entry_id, and a plain join would silently
// duplicate the schedule_entry row for each one.
func (s scheduleStore) ListDetailByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntryDetail, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT
			`+scheduleEntryColumnsAliased+`,
			i.title,
			vv.version,
			vv.verdict,
			vv.reasoning,
			COALESCE(NULLIF(approver.display_name, ''), COALESCE(approver.email, '')),
			pub.published,
			COALESCE(pub.youtube_video_id, ''),
			COALESCE(pub.video_title, '')
		FROM schedule_entry se
		JOIN idea i ON i.id = se.idea_id
		JOIN viability_verdict vv ON vv.id = se.verdict_id
		LEFT JOIN person approver ON approver.id = se.approved_by_person_id
		LEFT JOIN LATERAL (
			SELECT TRUE AS published, sv.youtube_video_id, sv.title AS video_title
			FROM video_schedule_match vsm
			JOIN synced_video sv ON sv.id = vsm.synced_video_id
			WHERE vsm.schedule_entry_id = se.id
			  AND vsm.state IN ('auto', 'confirmed')
			  AND sv.published_at IS NOT NULL
			ORDER BY (vsm.state = 'confirmed') DESC, vsm.created_at DESC
			LIMIT 1
		) pub ON TRUE
		WHERE se.channel_id = $1
		ORDER BY se.proposed_publish_at
	`, channelID)
	if err != nil {
		return nil, fmt.Errorf("list schedule entry details by channel: %w", err)
	}
	defer rows.Close()

	var details []ScheduleEntryDetail
	for rows.Next() {
		var d ScheduleEntryDetail
		var published *bool
		err := rows.Scan(
			&d.Entry.ID, &d.Entry.ChannelID, &d.Entry.IdeaID, &d.Entry.VerdictID, &d.Entry.ProposedPublishAt, &d.Entry.State,
			&d.Entry.ApprovedByPersonID, &d.Entry.ApprovedAt, &d.Entry.CreatedByPersonID, &d.Entry.CreatedAt, &d.Entry.UpdatedAt, &d.Entry.IdempotencyKey,
			&d.IdeaTitle, &d.VerdictVersion, &d.Verdict, &d.VerdictReasoning, &d.ApproverName,
			&published, &d.PublishedVideoID, &d.PublishedVideoTitle,
		)
		if err != nil {
			return nil, fmt.Errorf("scan schedule_entry detail: %w", err)
		}
		d.Published = published != nil && *published
		details = append(details, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schedule entry details by channel: %w", err)
	}
	return details, nil
}
