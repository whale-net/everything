package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// PersonaStore covers `persona` (migration 002) -- a product-level spec
// entity settled by issue #2488 (see krill/ARCHITECTURE.md's "Capability
// map entries, personas, and non-goals" section). Single parent:
// Product.ID.
type PersonaStore interface {
	// Create inserts a new Persona row under productID, minting a fresh
	// surrogate id (LB2). Returns ErrNotFound if productID has no current
	// `product` row (LB2 parentage). Fails if scopeID/productID already
	// has a current Persona with the same name (LB1).
	Create(ctx context.Context, scopeID, productID uuid.UUID, name string, description *string) (Persona, error)

	// GetCurrentByID returns the current row for id, or ErrNotFound.
	GetCurrentByID(ctx context.Context, id uuid.UUID) (Persona, error)

	// ListCurrentByProduct returns every current Persona under productID,
	// ordered by Position then Name.
	ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]Persona, error)
}

type personaStore struct{ pool *pgxpool.Pool }

var _ PersonaStore = personaStore{}

const personaColumns = `revision_id, id, scope_id, product_id, name, description, position, valid_from, valid_to`

func scanPersona(row pgx.Row) (Persona, error) {
	var p Persona
	err := row.Scan(&p.RevisionID, &p.ID, &p.ScopeID, &p.ProductID, &p.Name, &p.Description, &p.Position, &p.ValidFrom, &p.ValidTo)
	return p, err
}

func (s personaStore) Create(ctx context.Context, scopeID, productID uuid.UUID, name string, description *string) (Persona, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Persona{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return Persona{}, err
	}
	if !exists {
		return Persona{}, errParentNotFound("product", productID)
	}

	// position is one past the current max among productID's own current
	// Personas (append order) -- see product.go's Create doc comment for
	// why this must not be left at column DEFAULT 0.
	persona, err := scanPersona(tx.QueryRow(ctx, `
		INSERT INTO persona (scope_id, product_id, name, description, position)
		VALUES ($1, $2, $3, $4, (SELECT COALESCE(MAX(position), -1) + 1 FROM persona WHERE product_id = $2 AND valid_to IS NULL))
		RETURNING `+personaColumns,
		scopeID, productID, name, description))
	if err != nil {
		return Persona{}, fmt.Errorf("insert persona: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return Persona{}, fmt.Errorf("commit: %w", err)
	}
	return persona, nil
}

func (s personaStore) GetCurrentByID(ctx context.Context, id uuid.UUID) (Persona, error) {
	persona, err := scanPersona(s.pool.QueryRow(ctx, `
		SELECT `+personaColumns+`
		FROM persona
		WHERE id = $1 AND valid_to IS NULL
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return Persona{}, fmt.Errorf("%w: persona id %s", ErrNotFound, id)
	}
	if err != nil {
		return Persona{}, fmt.Errorf("get current persona: %w", err)
	}
	return persona, nil
}

func (s personaStore) ListCurrentByProduct(ctx context.Context, productID uuid.UUID) ([]Persona, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+personaColumns+`
		FROM persona
		WHERE product_id = $1 AND valid_to IS NULL
		ORDER BY position, name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list current personas: %w", err)
	}
	defer rows.Close()

	var personas []Persona
	for rows.Next() {
		p, err := scanPersona(rows)
		if err != nil {
			return nil, fmt.Errorf("scan persona: %w", err)
		}
		personas = append(personas, p)
	}
	return personas, rows.Err()
}
