package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// NonGoalStore covers `non_goal` (migration 002) -- the other
// brief-document entity kind issue #2488 settles (see
// krill/ARCHITECTURE.md's "Capability map entries, personas, and
// non-goals" section). Single parent: Product.ID.
type NonGoalStore interface {
	// Create inserts a new NonGoal row of the given kind (permanent or
	// deferred) under productID, minting a fresh surrogate id (LB2).
	// Returns ErrNotFound if productID has no current `product` row (LB2
	// parentage). Fails if scopeID/productID already has a current
	// NonGoal with the same name (LB1).
	Create(ctx context.Context, scopeID, productID uuid.UUID, kind NonGoalKind, name string, body *string) (NonGoal, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (NonGoal, error)

	// ListCurrentByProduct returns every current NonGoal (both kinds)
	// under productID, ordered by Kind, then Position, then Name.
	ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]NonGoal, error)
}

type nonGoalStore struct{ pool *pgxpool.Pool }

var _ NonGoalStore = nonGoalStore{}

const nonGoalColumns = `revision_id, id, scope_id, product_id, kind, name, body, position, valid_from, valid_to`

func scanNonGoal(row pgx.Row) (NonGoal, error) {
	var n NonGoal
	err := row.Scan(&n.RevisionID, &n.ID, &n.ScopeID, &n.ProductID, &n.Kind, &n.Name, &n.Body, &n.Position, &n.ValidFrom, &n.ValidTo)
	return n, err
}

func (s nonGoalStore) Create(ctx context.Context, scopeID, productID uuid.UUID, kind NonGoalKind, name string, body *string) (NonGoal, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return NonGoal{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return NonGoal{}, err
	}
	if !exists {
		return NonGoal{}, errParentNotFound("product", productID)
	}

	nonGoal, err := scanNonGoal(tx.QueryRow(ctx, `
		INSERT INTO non_goal (scope_id, product_id, kind, name, body)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+nonGoalColumns,
		scopeID, productID, string(kind), name, body))
	if err != nil {
		return NonGoal{}, fmt.Errorf("insert non_goal: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return NonGoal{}, fmt.Errorf("commit: %w", err)
	}
	return nonGoal, nil
}

func (s nonGoalStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (NonGoal, error) {
	nonGoal, err := scanNonGoal(s.pool.QueryRow(ctx, `
		SELECT `+nonGoalColumns+`
		FROM non_goal
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return NonGoal{}, fmt.Errorf("%w: non_goal id %s", ErrNotFound, id)
	}
	if err != nil {
		return NonGoal{}, fmt.Errorf("get current non_goal: %w", err)
	}
	return nonGoal, nil
}

func (s nonGoalStore) ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]NonGoal, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+nonGoalColumns+`
		FROM non_goal
		WHERE product_id = $1 AND valid_to IS NULL
		ORDER BY kind, position, name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list current non_goals: %w", err)
	}
	defer rows.Close()

	var nonGoals []NonGoal
	for rows.Next() {
		n, err := scanNonGoal(rows)
		if err != nil {
			return nil, fmt.Errorf("scan non_goal: %w", err)
		}
		nonGoals = append(nonGoals, n)
	}
	return nonGoals, rows.Err()
}
