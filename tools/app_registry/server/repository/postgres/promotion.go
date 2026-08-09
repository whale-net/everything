package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/whale-net/everything/tools/app_registry/server/repository"
)

type promotionRepo struct{ ex dbtx }

// promotionSelectBase joins promotion -> environment (for environment_key)
// -> artifact (for kind/app_id/chart_id/repository/version/digest), so a
// state dump is readable on its own without a second query per row — see
// ARCHITECTURE.md "SCD2 on promotion". Column order matches
// v_current_promotion exactly, so scanPromotion serves both this
// base-table query (used for historical/window reads, which the view
// cannot serve since it is current-only) and the view (used for pure
// "current" reads).
const promotionSelectBase = `
	SELECT p.promotion_id, p.environment_id, e.key, a.kind, a.app_id, a.chart_id, p.artifact_id,
	       a.repository, a.version, a.digest, p.state, p.is_override, p.valid_from, p.valid_to, p.target_key
	FROM promotion p
	JOIN environment e ON e.environment_id = p.environment_id
	JOIN artifact a ON a.artifact_id = p.artifact_id`

const promotionCurrentSelect = `
	SELECT promotion_id, environment_id, environment_key, kind, app_id, chart_id, artifact_id,
	       repository, version, digest, state, is_override, valid_from, valid_to, target_key
	FROM v_current_promotion`

func scanPromotion(row pgx.Row) (repository.Promotion, error) {
	var p repository.Promotion
	var kind string
	var appID, chartID *string
	var state string
	if err := row.Scan(
		&p.PromotionID, &p.EnvironmentID, &p.EnvironmentKey, &kind, &appID, &chartID, &p.ArtifactID,
		&p.Repository, &p.Version, &p.Digest, &state, &p.IsOverride, &p.ValidFrom, &p.ValidTo, &p.TargetKey,
	); err != nil {
		return repository.Promotion{}, err
	}
	p.Kind = repository.ArtifactKind(kind)
	p.State = repository.PromotionState(state)
	if appID != nil {
		p.AppID = *appID
	}
	if chartID != nil {
		p.ChartID = *chartID
	}
	return p, nil
}

// Promote performs the SCD2 close-and-open write. Callers MUST invoke this
// inside Registry.WithTx -- it issues three statements (select current,
// close it, insert the new row) that must commit or roll back together, the
// exact hazard AGENTS.md's SCD2 section and ARCHITECTURE.md's "Promotion
// and the intent to write it back must commit atomically" call out.
func (r *promotionRepo) Promote(ctx context.Context, p repository.Promotion) (*repository.Promotion, *repository.Promotion, error) {
	// 1. Snapshot whatever is current before closing it, so we can return it
	// as `superseded`. Read from the base tables (not the view) so this is
	// consistent with the UPDATE below inside the same transaction.
	row := r.ex.QueryRow(ctx, promotionSelectBase+`
		WHERE p.environment_id = $1 AND p.target_key = $2 AND p.valid_to IS NULL`,
		p.EnvironmentID, p.TargetKey)
	var superseded *repository.Promotion
	if existing, err := scanPromotion(row); err == nil {
		superseded = &existing
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, fmt.Errorf("promote %s/%s: read current: %w", p.EnvironmentID, p.TargetKey, err)
	}

	// 2. Close it. If nothing was current, this affects zero rows -- fine,
	// superseded stays nil.
	if superseded != nil {
		now := time.Now().UTC()
		if _, err := r.ex.Exec(ctx, `
			UPDATE promotion SET valid_to = $3
			WHERE environment_id = $1 AND target_key = $2 AND valid_to IS NULL`,
			p.EnvironmentID, p.TargetKey, now); err != nil {
			return nil, nil, fmt.Errorf("promote %s/%s: close current: %w", p.EnvironmentID, p.TargetKey, err)
		}
		superseded.ValidTo = &now
	}

	// 3. Open the new row. If a concurrent transaction closed-and-opened
	// between steps 1 and here, promotion_current_idx (the partial unique
	// index on (environment_id, target_key) WHERE valid_to IS NULL) rejects
	// this insert -- that is the "double-promotion structurally impossible"
	// property, not a bug to work around.
	id := uuid.NewString()
	state := p.State
	if state == "" {
		state = repository.PromotionStateActive
	}
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO promotion (promotion_id, environment_id, target_key, artifact_id, state, is_override)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		id, p.EnvironmentID, p.TargetKey, p.ArtifactID, string(state), p.IsOverride); err != nil {
		msg := fmt.Sprintf("target %s already has a current promotion in environment %s", p.TargetKey, p.EnvironmentID)
		if de, ok := translatePgError(err, msg); ok {
			return nil, nil, de
		}
		return nil, nil, fmt.Errorf("promote %s/%s: insert: %w", p.EnvironmentID, p.TargetKey, err)
	}

	current, err := r.GetCurrent(ctx, p.EnvironmentID, p.TargetKey)
	if err != nil {
		return nil, nil, fmt.Errorf("promote %s/%s: read back: %w", p.EnvironmentID, p.TargetKey, err)
	}
	return current, superseded, nil
}

func (r *promotionRepo) GetCurrent(ctx context.Context, environmentID, targetKey string) (*repository.Promotion, error) {
	row := r.ex.QueryRow(ctx, promotionCurrentSelect+` WHERE environment_id = $1 AND target_key = $2`, environmentID, targetKey)
	p, err := scanPromotion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *promotionRepo) GetPrevious(ctx context.Context, environmentID, targetKey string) (*repository.Promotion, error) {
	row := r.ex.QueryRow(ctx, promotionSelectBase+`
		WHERE p.environment_id = $1 AND p.target_key = $2 AND p.valid_to IS NOT NULL
		ORDER BY p.valid_from DESC LIMIT 1`, environmentID, targetKey)
	p, err := scanPromotion(row)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, repository.ErrNotFound
		}
		return nil, err
	}
	return &p, nil
}

func (r *promotionRepo) StateAt(ctx context.Context, environmentID string, at *time.Time) ([]repository.Promotion, error) {
	var rows pgx.Rows
	var err error
	if at == nil {
		rows, err = r.ex.Query(ctx, promotionCurrentSelect+` WHERE environment_id = $1`, environmentID)
	} else {
		// The SCD2 window query from ARCHITECTURE.md "SCD2 on promotion":
		// the incident query, "what was live at time T".
		rows, err = r.ex.Query(ctx, promotionSelectBase+`
			WHERE p.environment_id = $1 AND p.valid_from <= $2 AND (p.valid_to IS NULL OR p.valid_to > $2)`,
			environmentID, *at)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPromotions(rows)
}

func (r *promotionRepo) ListPromotions(ctx context.Context, filter repository.PromotionListFilter) ([]repository.Promotion, error) {
	base := promotionSelectBase + `
		LEFT JOIN app ON a.app_id = app.app_id
		LEFT JOIN chart ON a.chart_id = chart.chart_id
		WHERE 1=1`
	var args []any
	if filter.EnvironmentKey != "" {
		args = append(args, filter.EnvironmentKey)
		base += fmt.Sprintf(" AND e.key = $%d", len(args))
	}
	if filter.OwnerFullName != "" {
		args = append(args, filter.OwnerFullName)
		base += fmt.Sprintf(" AND (app.domain || '-' || app.name = $%d OR chart.domain || '-' || chart.name = $%d)", len(args), len(args))
	}
	if !filter.IncludeHistory {
		base += " AND p.valid_to IS NULL"
	}
	base += " ORDER BY p.valid_from DESC"

	rows, err := r.ex.Query(ctx, base, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanPromotions(rows)
}

func scanPromotions(rows pgx.Rows) ([]repository.Promotion, error) {
	var out []repository.Promotion
	for rows.Next() {
		p, err := scanPromotion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (r *promotionRepo) RecordEvent(ctx context.Context, e repository.PromotionEvent) (*repository.PromotionEvent, error) {
	e.EventID = uuid.NewString()
	if e.OccurredAt.IsZero() {
		e.OccurredAt = time.Now().UTC()
	}
	if _, err := r.ex.Exec(ctx, `
		INSERT INTO promotion_event (event_id, promotion_id, action, actor, reason, temporal_workflow_id, temporal_run_id, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
		e.EventID, e.PromotionID, string(e.Action), e.Actor, e.Reason, e.TemporalWorkflowID, e.TemporalRunID, e.OccurredAt); err != nil {
		return nil, fmt.Errorf("record promotion event for %s: %w", e.PromotionID, err)
	}
	return &e, nil
}

const promotionEventColumns = `pe.event_id, pe.promotion_id, pe.action, pe.actor, pe.reason, pe.temporal_workflow_id, pe.temporal_run_id, pe.occurred_at`

func scanPromotionEvent(row pgx.Row) (repository.PromotionEvent, error) {
	var e repository.PromotionEvent
	var action string
	if err := row.Scan(&e.EventID, &e.PromotionID, &action, &e.Actor, &e.Reason, &e.TemporalWorkflowID, &e.TemporalRunID, &e.OccurredAt); err != nil {
		return repository.PromotionEvent{}, err
	}
	e.Action = repository.PromotionAction(action)
	return e, nil
}

func (r *promotionRepo) ListEvents(ctx context.Context, filter repository.PromotionEventListFilter) ([]repository.PromotionEvent, error) {
	query := `SELECT ` + promotionEventColumns + `
		FROM promotion_event pe
		JOIN promotion p ON p.promotion_id = pe.promotion_id
		JOIN environment e ON e.environment_id = p.environment_id
		JOIN artifact a ON a.artifact_id = p.artifact_id
		LEFT JOIN app ON a.app_id = app.app_id
		LEFT JOIN chart ON a.chart_id = chart.chart_id
		WHERE 1=1`
	var args []any
	if filter.PromotionID != "" {
		args = append(args, filter.PromotionID)
		query += fmt.Sprintf(" AND pe.promotion_id = $%d", len(args))
	}
	if filter.EnvironmentKey != "" {
		args = append(args, filter.EnvironmentKey)
		query += fmt.Sprintf(" AND e.key = $%d", len(args))
	}
	if filter.OwnerFullName != "" {
		args = append(args, filter.OwnerFullName)
		query += fmt.Sprintf(" AND (app.domain || '-' || app.name = $%d OR chart.domain || '-' || chart.name = $%d)", len(args), len(args))
	}
	if filter.Actor != "" {
		args = append(args, filter.Actor)
		query += fmt.Sprintf(" AND pe.actor = $%d", len(args))
	}
	if !filter.Since.IsZero() {
		args = append(args, filter.Since)
		query += fmt.Sprintf(" AND pe.occurred_at >= $%d", len(args))
	}
	query += " ORDER BY pe.occurred_at DESC"

	rows, err := r.ex.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []repository.PromotionEvent
	for rows.Next() {
		e, err := scanPromotionEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
