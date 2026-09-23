package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadBearingDecisionStore covers `load_bearing_decision` (migration 002)
// -- FR4's persistence for PRODUCT.md's `LB<n>` entries. Single parent:
// FeatureSet.ID.
type LoadBearingDecisionStore interface {
	// Create inserts a new LoadBearingDecision row, minting a fresh
	// surrogate id (LB2) and the next product-wide DisplayNumber (`LBn`,
	// migration 017, issue #2969), and attaches it to featureSetID in the
	// same write (C2: attached to the area it constrains, never a global
	// list -- see migration 002's table comment). Returns ErrNotFound if
	// featureSetID has no current `feature_set` row (LB2 parentage).
	// Fails if scopeID/featureSetID already has a current
	// LoadBearingDecision with the same name (LB1).
	//
	// There is no separate "attach" step in this task: reparenting an
	// existing LoadBearingDecision to a different FeatureSet is a
	// supersession (closing the current row and opening one with a new
	// feature_set_id but the same id), which is explicitly out of this
	// task's scope -- see migration 002's LB3 note and issue #2488's
	// "amend task" reference. Create's featureSetID parameter is what the
	// issue's "AttachLoadBearingDecision" verb reduces to for a
	// brand-new decision.
	Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, body *string) (LoadBearingDecision, error)

	// CreateWithDisplayNumber is Create, except displayNumber is taken
	// verbatim rather than auto-incremented -- krill/importer/write.go's
	// only caller, so an imported document's own `LBn` token survives the
	// import unchanged instead of being renumbered by creation order.
	// Every other caller (every MCP-driven create_load_bearing_decision)
	// uses Create.
	CreateWithDisplayNumber(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, body *string, displayNumber int) (LoadBearingDecision, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (LoadBearingDecision, error)

	// ListCurrentByFeatureSet returns every current LoadBearingDecision
	// attached to featureSetID, ordered by Position then Name -- the read
	// that proves a decision "is readable back from that FeatureSet."
	ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]LoadBearingDecision, error)
}

type decisionStore struct{ pool *pgxpool.Pool }

var _ LoadBearingDecisionStore = decisionStore{}

const loadBearingDecisionColumns = `revision_id, id, scope_id, feature_set_id, name, body, position, display_number, valid_from, valid_to`

func scanLoadBearingDecision(row pgx.Row) (LoadBearingDecision, error) {
	var d LoadBearingDecision
	err := row.Scan(&d.RevisionID, &d.ID, &d.ScopeID, &d.FeatureSetID, &d.Name, &d.Body, &d.Position, &d.DisplayNumber, &d.ValidFrom, &d.ValidTo)
	return d, err
}

// createDecisionTx is Create's/CreateWithDisplayNumber's shared body.
// displayNumberOverride is nil for every ordinary create (the next
// product-wide number is computed here, nextDisplayNumber) and non-nil
// only for krill/importer's CreateWithDisplayNumber path, which supplies
// the source document's own `LBn` token verbatim (issue #2969).
func createDecisionTx(ctx context.Context, q txQuerier, scopeID, featureSetID uuid.UUID, name string, body *string, displayNumberOverride *int) (LoadBearingDecision, error) {
	var productID uuid.UUID
	err := q.QueryRow(ctx, `
		SELECT product_id FROM feature_set WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL
	`, featureSetID, scopeID).Scan(&productID)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoadBearingDecision{}, errParentNotFound("feature_set", featureSetID)
	}
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("get feature_set for create: %w", err)
	}

	position, err := nextSiblingPosition(ctx, q, "load_bearing_decision", "feature_set_id", featureSetID, scopeID)
	if err != nil {
		return LoadBearingDecision{}, err
	}

	displayNumber := 0
	if displayNumberOverride != nil {
		displayNumber = *displayNumberOverride
	} else {
		displayNumber, err = nextDisplayNumber(ctx, q, "load_bearing_decision", productID, scopeID)
		if err != nil {
			return LoadBearingDecision{}, err
		}
	}

	decision, err := scanLoadBearingDecision(q.QueryRow(ctx, `
		INSERT INTO load_bearing_decision (scope_id, feature_set_id, name, body, position, display_number)
		VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+loadBearingDecisionColumns,
		scopeID, featureSetID, name, body, position, displayNumber))
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("insert load_bearing_decision: %w", err)
	}
	return decision, nil
}

func (s decisionStore) Create(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, body *string) (LoadBearingDecision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	decision, err := createDecisionTx(ctx, tx, scopeID, featureSetID, name, body, nil)
	if err != nil {
		return LoadBearingDecision{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LoadBearingDecision{}, fmt.Errorf("commit: %w", err)
	}
	return decision, nil
}

func (s decisionStore) CreateWithDisplayNumber(ctx context.Context, scopeID, featureSetID uuid.UUID, name string, body *string, displayNumber int) (LoadBearingDecision, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	decision, err := createDecisionTx(ctx, tx, scopeID, featureSetID, name, body, &displayNumber)
	if err != nil {
		return LoadBearingDecision{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return LoadBearingDecision{}, fmt.Errorf("commit: %w", err)
	}
	return decision, nil
}

func (s decisionStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (LoadBearingDecision, error) {
	decision, err := scanLoadBearingDecision(s.pool.QueryRow(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return LoadBearingDecision{}, fmt.Errorf("%w: load_bearing_decision id %s", ErrNotFound, id)
	}
	if err != nil {
		return LoadBearingDecision{}, fmt.Errorf("get current load_bearing_decision: %w", err)
	}
	return decision, nil
}

func (s decisionStore) ListCurrentByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]LoadBearingDecision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE feature_set_id = $1 AND valid_to IS NULL
		ORDER BY position, name
	`, featureSetID)
	if err != nil {
		return nil, fmt.Errorf("list current load_bearing_decisions: %w", err)
	}
	defer rows.Close()

	var decisions []LoadBearingDecision
	for rows.Next() {
		d, err := scanLoadBearingDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan load_bearing_decision: %w", err)
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}
