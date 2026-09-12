// This file (issue #2542, krill M2, FR1/FR8) is DesignSessionStore --
// `design_session`'s (migration 006) accessor. Method bodies are scaffold
// stubs; the Implementation phase of issue #2542 fills them in against
// this same interface.
package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// DesignSessionStore covers `design_session` (migration 006) -- the
// longer-lived container FR2's revision_event rounds accumulate under.
// Single parent: Product.ID. Not SCD2 (LB3) -- see migration
// 006_design_session.up.sql's boundary comment; a DesignSession's mutable
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

func (s designSessionStore) Open(ctx context.Context, scopeID, productID uuid.UUID, openingSubmission string, openedByKrillSessionID SessionID) (DesignSession, error) {
	return DesignSession{}, fmt.Errorf("store: DesignSessionStore.Open not implemented -- see issue #2542's Implementation phase")
}

func (s designSessionStore) GetByID(ctx context.Context, id uuid.UUID) (DesignSession, error) {
	return DesignSession{}, fmt.Errorf("store: DesignSessionStore.GetByID not implemented -- see issue #2542's Implementation phase")
}

func (s designSessionStore) ListByProduct(ctx context.Context, productID uuid.UUID) ([]DesignSession, error) {
	return nil, fmt.Errorf("store: DesignSessionStore.ListByProduct not implemented -- see issue #2542's Implementation phase")
}
