package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStrategyIdeaNotViable is returned by StrategyStore.Save when a
// linked Idea has no current verdict, or its current verdict is not
// VerdictViable -- a Strategy may only be built from viable-verdict
// Ideas (issue #1637), the same FR16 gate ScheduleStore.SaveDraft
// enforces one layer downstream.
var ErrStrategyIdeaNotViable = errors.New("idea does not have a current viable verdict")

// ErrStrategyNotFound is returned by StrategyStore.Save when
// SaveStrategyInput.StrategyID is set but does not resolve to a row on
// SaveStrategyInput.ChannelID.
var ErrStrategyNotFound = errors.New("strategy not found on this channel")

// SaveStrategyInput is the input to StrategyStore.Save.
type SaveStrategyInput struct {
	// StrategyID selects an existing strategy to update when non-nil.
	// When nil, Save always inserts a new strategy row -- unlike
	// PacingPolicy, a Strategy has no natural key to converge on (a
	// Channel may hold many Strategies), so this mirrors IdeaStore.Create
	// rather than PacingStore.Upsert.
	StrategyID       *uuid.UUID
	ChannelID        uuid.UUID
	Title            string
	Cadence          Cadence
	PreferredWeekday string // "" for no day preference.
	Active           bool
	// IdeaIDs are the Ideas this Strategy is built from; each must belong
	// to ChannelID and currently have a viable verdict
	// (ErrStrategyIdeaNotViable otherwise, nothing written). Replaces the
	// full linked set on an update -- not a merge.
	IdeaIDs           []uuid.UUID
	CreatedByPersonID uuid.UUID // the calling Person, checked on every call (including updates) for the idempotency-key lookup below.
	IdempotencyKey    string
}

// StrategyStore covers `strategy` and `strategy_idea` (migration 006,
// issue #1637).
type StrategyStore interface {
	// Save creates a new Strategy (in.StrategyID nil) or updates an
	// existing one (in.StrategyID set, replacing its linked Ideas
	// wholesale), always re-validating every idea_id belongs to
	// in.ChannelID and currently has a viable verdict before writing
	// anything (ErrStrategyIdeaNotViable -- no partial write). Honours
	// IdempotencyKey: a replayed (channel, author, key) triple returns
	// the original row unchanged rather than writing again.
	Save(ctx context.Context, in SaveStrategyInput) (StrategyDetail, error)

	// GetByID returns the StrategyDetail for id, or pgx.ErrNoRows if none
	// exists.
	GetByID(ctx context.Context, id uuid.UUID) (StrategyDetail, error)

	// ListByChannel returns every Strategy for channelID, ordered by
	// created_at. activeOnly restricts to active = TRUE -- the set
	// generate_schedule_plan (mcp/tools/strategy.go) reads from.
	ListByChannel(ctx context.Context, channelID uuid.UUID, activeOnly bool) ([]StrategyDetail, error)
}

// strategyStore implements StrategyStore against `strategy` and
// `strategy_idea` (migration 006).
type strategyStore struct{ pool *pgxpool.Pool }

var _ StrategyStore = strategyStore{}

const strategyColumns = `id, channel_id, title, cadence, COALESCE(preferred_weekday, ''), active, created_by_person_id, created_at, updated_at, COALESCE(idempotency_key, '')`

func scanStrategy(row pgx.Row) (Strategy, error) {
	var s Strategy
	err := row.Scan(&s.ID, &s.ChannelID, &s.Title, &s.Cadence, &s.PreferredWeekday, &s.Active, &s.CreatedByPersonID, &s.CreatedAt, &s.UpdatedAt, &s.IdempotencyKey)
	return s, err
}

// loadStrategyIdeas returns strategyID's linked Ideas (StrategyIdeaDetail,
// joined with idea.title and viability_verdict.version), ordered by
// idea.title so rendering is stable across calls. q is pgxQueryer
// (verdict.go) so this can run either against the pool (GetByID,
// ListByChannel) or inside Save's own transaction.
func loadStrategyIdeas(ctx context.Context, q pgxQueryer, strategyID uuid.UUID) ([]StrategyIdeaDetail, error) {
	rows, err := q.Query(ctx, `
		SELECT si.idea_id, i.title, si.verdict_id, vv.version
		FROM strategy_idea si
		JOIN idea i ON i.id = si.idea_id
		JOIN viability_verdict vv ON vv.id = si.verdict_id
		WHERE si.strategy_id = $1
		ORDER BY i.title
	`, strategyID)
	if err != nil {
		return nil, fmt.Errorf("list strategy_idea: %w", err)
	}
	defer rows.Close()

	var out []StrategyIdeaDetail
	for rows.Next() {
		var d StrategyIdeaDetail
		if err := rows.Scan(&d.IdeaID, &d.IdeaTitle, &d.VerdictID, &d.VerdictVersion); err != nil {
			return nil, fmt.Errorf("scan strategy_idea: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list strategy_idea: %w", err)
	}
	return out, nil
}

// Save honours IdempotencyKey (a replayed (channel, author, key) triple
// returns the original row, linked Ideas included, rather than writing
// again). Otherwise it validates every in.IdeaIDs entry -- exists, belongs
// to in.ChannelID, currently has a viable verdict -- capturing that
// verdict's ID, before ever inserting/updating `strategy` or touching
// `strategy_idea`, so a rejected idea_id leaves nothing written (mirrors
// save_viability_verdict's citation validation, verdict.go).
func (s strategyStore) Save(ctx context.Context, in SaveStrategyInput) (StrategyDetail, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return StrategyDetail{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if in.IdempotencyKey != "" {
		existing, err := scanStrategy(tx.QueryRow(ctx, `
			SELECT `+strategyColumns+`
			FROM strategy
			WHERE channel_id = $1 AND created_by_person_id = $2 AND idempotency_key = $3
		`, in.ChannelID, in.CreatedByPersonID, in.IdempotencyKey))
		if err == nil {
			ideas, err := loadStrategyIdeas(ctx, tx, existing.ID)
			if err != nil {
				return StrategyDetail{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return StrategyDetail{}, fmt.Errorf("commit: %w", err)
			}
			return StrategyDetail{Strategy: existing, Ideas: ideas}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return StrategyDetail{}, fmt.Errorf("lookup strategy by idempotency key: %w", err)
		}
	}

	links := make([]StrategyIdeaDetail, 0, len(in.IdeaIDs))
	for _, ideaID := range in.IdeaIDs {
		var channelID uuid.UUID
		var ideaTitle string
		err := tx.QueryRow(ctx, `SELECT channel_id, title FROM idea WHERE id = $1`, ideaID).Scan(&channelID, &ideaTitle)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StrategyDetail{}, fmt.Errorf("idea %s: %w", ideaID, pgx.ErrNoRows)
			}
			return StrategyDetail{}, fmt.Errorf("load idea %s: %w", ideaID, err)
		}
		if channelID != in.ChannelID {
			return StrategyDetail{}, fmt.Errorf("idea %s does not belong to channel %s", ideaID, in.ChannelID)
		}

		var verdictID uuid.UUID
		var verdictValue VerdictValue
		var verdictVersion int
		err = tx.QueryRow(ctx, `
			SELECT id, verdict, version FROM v_current_verdict WHERE idea_id = $1
		`, ideaID).Scan(&verdictID, &verdictValue, &verdictVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StrategyDetail{}, fmt.Errorf("idea %s: %w", ideaID, ErrStrategyIdeaNotViable)
			}
			return StrategyDetail{}, fmt.Errorf("load current verdict for idea %s: %w", ideaID, err)
		}
		if verdictValue != VerdictViable {
			return StrategyDetail{}, fmt.Errorf("idea %s: %w", ideaID, ErrStrategyIdeaNotViable)
		}

		links = append(links, StrategyIdeaDetail{IdeaID: ideaID, IdeaTitle: ideaTitle, VerdictID: verdictID, VerdictVersion: verdictVersion})
	}

	var strategy Strategy
	if in.StrategyID == nil {
		strategy, err = scanStrategy(tx.QueryRow(ctx, `
			INSERT INTO strategy (channel_id, title, cadence, preferred_weekday, active, created_by_person_id, idempotency_key)
			VALUES ($1, $2, $3, NULLIF($4, ''), $5, $6, NULLIF($7, ''))
			RETURNING `+strategyColumns,
			in.ChannelID, in.Title, in.Cadence, in.PreferredWeekday, in.Active, in.CreatedByPersonID, in.IdempotencyKey))
		if err != nil {
			return StrategyDetail{}, fmt.Errorf("insert strategy: %w", err)
		}
	} else {
		// Lock the target row (and confirm it belongs to in.ChannelID)
		// before updating, so a racing Save for the same strategy
		// serializes its idea-link replacement below.
		var exists bool
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM strategy WHERE id = $1 AND channel_id = $2 FOR UPDATE)
		`, *in.StrategyID, in.ChannelID).Scan(&exists); err != nil {
			return StrategyDetail{}, fmt.Errorf("lock strategy: %w", err)
		}
		if !exists {
			return StrategyDetail{}, ErrStrategyNotFound
		}

		strategy, err = scanStrategy(tx.QueryRow(ctx, `
			UPDATE strategy
			SET title = $1, cadence = $2, preferred_weekday = NULLIF($3, ''), active = $4,
				updated_at = NOW(), idempotency_key = NULLIF($5, '')
			WHERE id = $6
			RETURNING `+strategyColumns,
			in.Title, in.Cadence, in.PreferredWeekday, in.Active, in.IdempotencyKey, *in.StrategyID))
		if err != nil {
			return StrategyDetail{}, fmt.Errorf("update strategy: %w", err)
		}

		if _, err := tx.Exec(ctx, `DELETE FROM strategy_idea WHERE strategy_id = $1`, strategy.ID); err != nil {
			return StrategyDetail{}, fmt.Errorf("clear strategy_idea: %w", err)
		}
	}

	for _, link := range links {
		if _, err := tx.Exec(ctx, `
			INSERT INTO strategy_idea (strategy_id, idea_id, verdict_id) VALUES ($1, $2, $3)
		`, strategy.ID, link.IdeaID, link.VerdictID); err != nil {
			return StrategyDetail{}, fmt.Errorf("insert strategy_idea: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return StrategyDetail{}, fmt.Errorf("commit: %w", err)
	}
	return StrategyDetail{Strategy: strategy, Ideas: links}, nil
}

func (s strategyStore) GetByID(ctx context.Context, id uuid.UUID) (StrategyDetail, error) {
	strategy, err := scanStrategy(s.pool.QueryRow(ctx, `SELECT `+strategyColumns+` FROM strategy WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StrategyDetail{}, pgx.ErrNoRows
		}
		return StrategyDetail{}, fmt.Errorf("get strategy by id: %w", err)
	}

	ideas, err := loadStrategyIdeas(ctx, s.pool, strategy.ID)
	if err != nil {
		return StrategyDetail{}, err
	}
	return StrategyDetail{Strategy: strategy, Ideas: ideas}, nil
}

func (s strategyStore) ListByChannel(ctx context.Context, channelID uuid.UUID, activeOnly bool) ([]StrategyDetail, error) {
	query := `SELECT ` + strategyColumns + ` FROM strategy WHERE channel_id = $1`
	if activeOnly {
		query += ` AND active`
	}
	query += ` ORDER BY created_at`

	rows, err := s.pool.Query(ctx, query, channelID)
	if err != nil {
		return nil, fmt.Errorf("list strategies by channel: %w", err)
	}
	defer rows.Close()

	var strategies []Strategy
	for rows.Next() {
		st, err := scanStrategy(rows)
		if err != nil {
			return nil, fmt.Errorf("scan strategy: %w", err)
		}
		strategies = append(strategies, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list strategies by channel: %w", err)
	}

	details := make([]StrategyDetail, 0, len(strategies))
	for _, st := range strategies {
		ideas, err := loadStrategyIdeas(ctx, s.pool, st.ID)
		if err != nil {
			return nil, err
		}
		details = append(details, StrategyDetail{Strategy: st, Ideas: ideas})
	}
	return details, nil
}
