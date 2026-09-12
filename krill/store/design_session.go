// This file (issue #2542, krill M2, FR1/FR8) is DesignSessionStore --
// `design_session`'s (migration 008) accessor.
package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DesignSessionStore covers `design_session` (migration 008) -- the
// longer-lived container FR2's revision_event rounds accumulate under.
// Single parent: Product.ID. Not SCD2 (LB3) -- see migration
// 008_design_session.up.sql's boundary comment; a DesignSession's mutable
// state is entirely derived from its RevisionEventStore log, never stored
// back onto this row.
type DesignSessionStore interface {
	// Open inserts a new design_session row against scopeID and
	// productID, recording openingSubmission (FR8) and the krill_session
	// that gated this call (FR1, provenance only -- see DesignSession's
	// doc comment).
	//
	// Returns ErrNotFound (via errParentNotFound) if productID has no
	// current `product` row in scopeID (LB2 parentage, same
	// currentRowExists enforcement, inside the same transaction as the
	// INSERT, as every other child entity's Create in this package).
	Open(ctx context.Context, scopeID, productID uuid.UUID, openingSubmission string, openedByKrillSessionID SessionID) (DesignSession, error)

	// GetByID returns the design_session row for id, or ErrNotFound if no
	// such row exists.
	GetByID(ctx context.Context, id uuid.UUID) (DesignSession, error)

	// ListByProduct returns every design_session row for productID.
	ListByProduct(ctx context.Context, productID uuid.UUID) ([]DesignSession, error)
}

// designSessionStore is the pgx-backed DesignSessionStore implementation.
type designSessionStore struct{ pool *pgxpool.Pool }

var _ DesignSessionStore = designSessionStore{}

// designSessionColumns is the scan/select column list shared by every
// method below, mirroring product.go's productColumns precedent.
const designSessionColumns = `id, scope_id, product_id, opening_submission, opened_by_krill_session_id, created_at`

func scanDesignSession(row pgx.Row) (DesignSession, error) {
	var ds DesignSession
	var openedBy uuid.UUID
	if err := row.Scan(&ds.ID, &ds.ScopeID, &ds.ProductID, &ds.OpeningSubmission, &openedBy, &ds.CreatedAt); err != nil {
		return DesignSession{}, err
	}
	ds.OpenedByKrillSessionID = SessionID(openedBy)
	return ds, nil
}

func (s designSessionStore) Open(ctx context.Context, scopeID, productID uuid.UUID, openingSubmission string, openedByKrillSessionID SessionID) (DesignSession, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return DesignSession{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return DesignSession{}, err
	}
	if !exists {
		return DesignSession{}, errParentNotFound("product", productID)
	}

	ds, err := scanDesignSession(tx.QueryRow(ctx, `
		INSERT INTO design_session (scope_id, product_id, opening_submission, opened_by_krill_session_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+designSessionColumns,
		scopeID, productID, openingSubmission, uuid.UUID(openedByKrillSessionID)))
	if err != nil {
		return DesignSession{}, fmt.Errorf("insert design_session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return DesignSession{}, fmt.Errorf("commit: %w", err)
	}
	return ds, nil
}

func (s designSessionStore) GetByID(ctx context.Context, id uuid.UUID) (DesignSession, error) {
	ds, err := scanDesignSession(s.pool.QueryRow(ctx, `
		SELECT `+designSessionColumns+`
		FROM design_session
		WHERE id = $1
	`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return DesignSession{}, fmt.Errorf("%w: design_session id %s", ErrNotFound, id)
	}
	if err != nil {
		return DesignSession{}, fmt.Errorf("select design_session: %w", err)
	}
	return ds, nil
}

func (s designSessionStore) ListByProduct(ctx context.Context, productID uuid.UUID) ([]DesignSession, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+designSessionColumns+`
		FROM design_session
		WHERE product_id = $1
		ORDER BY created_at
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list design_session by product: %w", err)
	}
	defer rows.Close()

	var sessions []DesignSession
	for rows.Next() {
		ds, err := scanDesignSession(rows)
		if err != nil {
			return nil, fmt.Errorf("scan design_session: %w", err)
		}
		sessions = append(sessions, ds)
	}
	return sessions, rows.Err()
}
