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
	Approve(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Unapprove reverses Approve, transitioning entryID back to draft and
	// clearing approved_by_person_id/approved_at (FR20).
	Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error

	// Update changes a draft entry's proposed_publish_at (FR18's pacing
	// adjustments).
	Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error

	// ListByChannel returns every ScheduleEntry for channelID.
	ListByChannel(ctx context.Context, channelID uuid.UUID) ([]ScheduleEntry, error)

	// GetByID returns the ScheduleEntry for id, or pgx.ErrNoRows if none
	// exists. Backs save_schedule_draft's WriteRender step
	// (mcp/tools/schedule_draft.go, issue #1579), which always re-reads
	// from Postgres rather than caching what SaveDraft returned (LB4).
	GetByID(ctx context.Context, id uuid.UUID) (ScheduleEntry, error)
}

// scheduleStore implements ScheduleStore against `schedule_entry`
// (migration 002).
type scheduleStore struct{ pool *pgxpool.Pool }

var _ ScheduleStore = scheduleStore{}

const scheduleEntryColumns = `id, channel_id, idea_id, verdict_id, proposed_publish_at, state, approved_by_person_id, approved_at, created_by_person_id, created_at, updated_at, COALESCE(idempotency_key, '')`

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
	tag, err := s.pool.Exec(ctx, `
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
	return nil
}

func (s scheduleStore) Unapprove(ctx context.Context, entryID, byPersonID uuid.UUID) error {
	tag, err := s.pool.Exec(ctx, `
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
	return nil
}

func (s scheduleStore) Update(ctx context.Context, entryID uuid.UUID, proposedPublishAt time.Time) error {
	tag, err := s.pool.Exec(ctx, `
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
