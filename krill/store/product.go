package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ProductStore covers `product` (migration 002) -- the top of the spec
// chain. Products have no parent, so unlike every other store in this
// package, Create performs no parent-existence check.
type ProductStore interface {
	// Create inserts a new Product row, minting a fresh surrogate id
	// (LB2). Fails if scopeID has no current row in `product` with the
	// same name (scope-qualified uniqueness, LB1) -- see migration 002's
	// product_scope_name_current_idx.
	Create(ctx context.Context, scopeID uuid.UUID, name, vision string) (Product, error)

	// GetCurrentByID returns the current (valid_to IS NULL) row for id, or
	// ErrNotFound if none exists.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (Product, error)

	// ListCurrentByScope returns every current Product in scopeID, ordered
	// by Position then Name -- Position is a freely-rewritable sort
	// column, never identity (LB2).
	ListCurrentByScope(ctx context.Context, scopeID uuid.UUID) ([]Product, error)
}

type productStore struct{ pool *pgxpool.Pool }

var _ ProductStore = productStore{}

const productColumns = `revision_id, id, scope_id, name, vision, position, valid_from, valid_to`

func scanProduct(row pgx.Row) (Product, error) {
	var p Product
	err := row.Scan(&p.RevisionID, &p.ID, &p.ScopeID, &p.Name, &p.Vision, &p.Position, &p.ValidFrom, &p.ValidTo)
	return p, err
}

func (s productStore) Create(ctx context.Context, scopeID uuid.UUID, name, vision string) (Product, error) {
	product, err := scanProduct(s.pool.QueryRow(ctx, `
		INSERT INTO product (scope_id, name, vision)
		VALUES ($1, $2, $3)
		RETURNING `+productColumns,
		scopeID, name, vision))
	if err != nil {
		return Product{}, fmt.Errorf("insert product: %w", err)
	}
	return product, nil
}

func (s productStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (Product, error) {
	product, err := scanProduct(s.pool.QueryRow(ctx, `
		SELECT `+productColumns+`
		FROM product
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Product{}, fmt.Errorf("%w: product id %s", ErrNotFound, id)
	}
	if err != nil {
		return Product{}, fmt.Errorf("get current product: %w", err)
	}
	return product, nil
}

func (s productStore) ListCurrentByScope(ctx context.Context, scopeID uuid.UUID) ([]Product, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+productColumns+`
		FROM product
		WHERE scope_id = $1 AND valid_to IS NULL
		ORDER BY position, name
	`, scopeID)
	if err != nil {
		return nil, fmt.Errorf("list current products: %w", err)
	}
	defer rows.Close()

	var products []Product
	for rows.Next() {
		p, err := scanProduct(rows)
		if err != nil {
			return nil, fmt.Errorf("scan product: %w", err)
		}
		products = append(products, p)
	}
	return products, rows.Err()
}
