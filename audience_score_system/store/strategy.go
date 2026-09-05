package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrStrategyVerdictNotViable is returned by StrategyStore.Save when a
// linked verdict_id does not exist or its verdict column is not
// VerdictViable -- a Strategy may only be built from viable
// viability_verdict rows (issue #1637), the same viable-verdict gate
// VideoScriptStore.Propose enforces one layer downstream (FR36).
var ErrStrategyVerdictNotViable = errors.New("verdict does not exist or is not viable")

// ErrStrategyNotFound is returned by StrategyStore.Save when
// SaveStrategyInput.StrategyID is set but does not resolve to a row on
// SaveStrategyInput.ChannelID.
var ErrStrategyNotFound = errors.New("strategy not found on this channel")

// SaveStrategyInput is the input to StrategyStore.Save.
type SaveStrategyInput struct {
	// StrategyID selects an existing strategy to update when non-nil.
	// When nil, Save always inserts a new strategy row -- a Strategy has
	// no natural key to converge on (a Channel may hold many Strategies),
	// so this mirrors IdeaStore.Create's plain-insert shape rather than a
	// find-or-create upsert.
	StrategyID       *uuid.UUID
	ChannelID        uuid.UUID
	Title            string
	PreferredWeekday string // "" for no day preference.
	Active           bool
	// VerdictIDs are the viability_verdict rows this Strategy is built
	// from directly -- not idea_ids: each verdict's idea must belong to
	// ChannelID, and the verdict itself must currently be viable
	// (ErrStrategyVerdictNotViable otherwise, nothing written). The same
	// verdict may be passed to more than one Strategy. Replaces the full
	// linked set on an update -- not a merge.
	VerdictIDs        []uuid.UUID
	CreatedByPersonID uuid.UUID // the calling Person, checked on every call (including updates) for the idempotency-key lookup below.
	IdempotencyKey    string
}

// StrategyStore covers `strategy` and `strategy_verdict` (migration 008,
// issue #1637).
type StrategyStore interface {
	// Save creates a new Strategy (in.StrategyID nil) or updates an
	// existing one (in.StrategyID set, replacing its linked verdicts
	// wholesale), always re-validating every verdict_id belongs to
	// in.ChannelID (via its idea) and is currently viable before writing
	// anything (ErrStrategyVerdictNotViable -- no partial write). Honours
	// IdempotencyKey: a replayed (channel, author, key) triple returns
	// the original row unchanged rather than writing again.
	Save(ctx context.Context, in SaveStrategyInput) (StrategyDetail, error)

	// GetByID returns the StrategyDetail for id, or pgx.ErrNoRows if none
	// exists.
	GetByID(ctx context.Context, id uuid.UUID) (StrategyDetail, error)

	// ListByChannel returns Strategies for channelID, ordered by
	// created_at. activeOnly restricts to active = TRUE. limit
	// (<=0 = unbounded) caps the response for list_strategies
	// (mcp/tools/strategy.go, issue #1813's follow-up); truncated reports
	// whether more matching rows exist beyond it.
	ListByChannel(ctx context.Context, channelID uuid.UUID, activeOnly bool, limit int) (details []StrategyDetail, truncated bool, err error)
}

// strategyStore implements StrategyStore against `strategy` and
// `strategy_verdict` (migration 008).
type strategyStore struct{ pool *pgxpool.Pool }

var _ StrategyStore = strategyStore{}

const strategyColumns = `id, channel_id, title, COALESCE(preferred_weekday, ''), active, created_by_person_id, created_at, updated_at, COALESCE(idempotency_key, '')`

func scanStrategy(row pgx.Row) (Strategy, error) {
	var s Strategy
	err := row.Scan(&s.ID, &s.ChannelID, &s.Title, &s.PreferredWeekday, &s.Active, &s.CreatedByPersonID, &s.CreatedAt, &s.UpdatedAt, &s.IdempotencyKey)
	return s, err
}

// loadStrategyVerdicts returns strategyID's linked verdicts
// (StrategyVerdictDetail, joined with viability_verdict.version and
// idea.id/title), ordered by idea.title so rendering is stable across
// calls. q is pgxQueryer (verdict.go) so this can run either against the
// pool (GetByID, ListByChannel) or inside Save's own transaction.
func loadStrategyVerdicts(ctx context.Context, q pgxQueryer, strategyID uuid.UUID) ([]StrategyVerdictDetail, error) {
	rows, err := q.Query(ctx, `
		SELECT sv.verdict_id, vv.version, vv.idea_id, i.title
		FROM strategy_verdict sv
		JOIN viability_verdict vv ON vv.id = sv.verdict_id
		JOIN idea i ON i.id = vv.idea_id
		WHERE sv.strategy_id = $1
		ORDER BY i.title
	`, strategyID)
	if err != nil {
		return nil, fmt.Errorf("list strategy_verdict: %w", err)
	}
	defer rows.Close()

	var out []StrategyVerdictDetail
	for rows.Next() {
		var d StrategyVerdictDetail
		if err := rows.Scan(&d.VerdictID, &d.VerdictVersion, &d.IdeaID, &d.IdeaTitle); err != nil {
			return nil, fmt.Errorf("scan strategy_verdict: %w", err)
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list strategy_verdict: %w", err)
	}
	return out, nil
}

// Save honours IdempotencyKey (a replayed (channel, author, key) triple
// returns the original row, linked verdicts included, rather than writing
// again). Otherwise it validates every in.VerdictIDs entry -- exists, its
// idea belongs to in.ChannelID, and it is currently viable -- before ever
// inserting/updating `strategy` or touching `strategy_verdict`, so a
// rejected verdict_id leaves nothing written (mirrors
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
			verdicts, err := loadStrategyVerdicts(ctx, tx, existing.ID)
			if err != nil {
				return StrategyDetail{}, err
			}
			if err := tx.Commit(ctx); err != nil {
				return StrategyDetail{}, fmt.Errorf("commit: %w", err)
			}
			return StrategyDetail{Strategy: existing, Verdicts: verdicts}, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return StrategyDetail{}, fmt.Errorf("lookup strategy by idempotency key: %w", err)
		}
	}

	links := make([]StrategyVerdictDetail, 0, len(in.VerdictIDs))
	for _, verdictID := range in.VerdictIDs {
		var ideaID, ideaChannelID uuid.UUID
		var ideaTitle string
		var verdictValue VerdictValue
		var verdictVersion int
		err := tx.QueryRow(ctx, `
			SELECT vv.idea_id, i.channel_id, i.title, vv.verdict, vv.version
			FROM viability_verdict vv
			JOIN idea i ON i.id = vv.idea_id
			WHERE vv.id = $1
		`, verdictID).Scan(&ideaID, &ideaChannelID, &ideaTitle, &verdictValue, &verdictVersion)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return StrategyDetail{}, fmt.Errorf("verdict %s: %w", verdictID, ErrStrategyVerdictNotViable)
			}
			return StrategyDetail{}, fmt.Errorf("load verdict %s: %w", verdictID, err)
		}
		if ideaChannelID != in.ChannelID {
			return StrategyDetail{}, fmt.Errorf("verdict %s does not belong to channel %s", verdictID, in.ChannelID)
		}
		if verdictValue != VerdictViable {
			return StrategyDetail{}, fmt.Errorf("verdict %s: %w", verdictID, ErrStrategyVerdictNotViable)
		}

		links = append(links, StrategyVerdictDetail{VerdictID: verdictID, VerdictVersion: verdictVersion, IdeaID: ideaID, IdeaTitle: ideaTitle})
	}

	var strategy Strategy
	if in.StrategyID == nil {
		strategy, err = scanStrategy(tx.QueryRow(ctx, `
			INSERT INTO strategy (channel_id, title, preferred_weekday, active, created_by_person_id, idempotency_key)
			VALUES ($1, $2, NULLIF($3, ''), $4, $5, NULLIF($6, ''))
			RETURNING `+strategyColumns,
			in.ChannelID, in.Title, in.PreferredWeekday, in.Active, in.CreatedByPersonID, in.IdempotencyKey))
		if err != nil {
			return StrategyDetail{}, fmt.Errorf("insert strategy: %w", err)
		}
	} else {
		// Lock the target row (and confirm it belongs to in.ChannelID)
		// before updating, so a racing Save for the same strategy
		// serializes its verdict-link replacement below.
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
			SET title = $1, preferred_weekday = NULLIF($2, ''), active = $3,
				updated_at = NOW(), idempotency_key = NULLIF($4, '')
			WHERE id = $5
			RETURNING `+strategyColumns,
			in.Title, in.PreferredWeekday, in.Active, in.IdempotencyKey, *in.StrategyID))
		if err != nil {
			return StrategyDetail{}, fmt.Errorf("update strategy: %w", err)
		}

		if _, err := tx.Exec(ctx, `DELETE FROM strategy_verdict WHERE strategy_id = $1`, strategy.ID); err != nil {
			return StrategyDetail{}, fmt.Errorf("clear strategy_verdict: %w", err)
		}
	}

	for _, link := range links {
		if _, err := tx.Exec(ctx, `
			INSERT INTO strategy_verdict (strategy_id, verdict_id) VALUES ($1, $2)
		`, strategy.ID, link.VerdictID); err != nil {
			return StrategyDetail{}, fmt.Errorf("insert strategy_verdict: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return StrategyDetail{}, fmt.Errorf("commit: %w", err)
	}
	return StrategyDetail{Strategy: strategy, Verdicts: links}, nil
}

func (s strategyStore) GetByID(ctx context.Context, id uuid.UUID) (StrategyDetail, error) {
	strategy, err := scanStrategy(s.pool.QueryRow(ctx, `SELECT `+strategyColumns+` FROM strategy WHERE id = $1`, id))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return StrategyDetail{}, pgx.ErrNoRows
		}
		return StrategyDetail{}, fmt.Errorf("get strategy by id: %w", err)
	}

	verdicts, err := loadStrategyVerdicts(ctx, s.pool, strategy.ID)
	if err != nil {
		return StrategyDetail{}, err
	}
	return StrategyDetail{Strategy: strategy, Verdicts: verdicts}, nil
}

func (s strategyStore) ListByChannel(ctx context.Context, channelID uuid.UUID, activeOnly bool, limit int) ([]StrategyDetail, bool, error) {
	query := `SELECT ` + strategyColumns + ` FROM strategy WHERE channel_id = $1`
	args := []any{channelID}
	if activeOnly {
		query += ` AND active`
	}
	args = append(args, fetchLimit(limit))
	query += fmt.Sprintf(` ORDER BY created_at LIMIT $%d`, len(args))

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, false, fmt.Errorf("list strategies by channel: %w", err)
	}
	defer rows.Close()

	var strategies []Strategy
	for rows.Next() {
		st, err := scanStrategy(rows)
		if err != nil {
			return nil, false, fmt.Errorf("scan strategy: %w", err)
		}
		strategies = append(strategies, st)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list strategies by channel: %w", err)
	}
	strategies, truncated := paginate(strategies, limit)

	details := make([]StrategyDetail, 0, len(strategies))
	for _, st := range strategies {
		verdicts, err := loadStrategyVerdicts(ctx, s.pool, st.ID)
		if err != nil {
			return nil, false, err
		}
		details = append(details, StrategyDetail{Strategy: st, Verdicts: verdicts})
	}
	return details, truncated, nil
}
