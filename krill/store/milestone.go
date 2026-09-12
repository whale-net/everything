package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MilestoneStore covers `milestone_ref` and `entity_milestone` (migration
// 004, issue #2492, FR17, LB6) -- the bare delivery-axis reference and its
// association, and the only store surface the importer (FR16) writes on
// the delivery axis. Neither table is SCD2 (see migration
// 004_milestone_assoc.up.sql's LB3 note), so unlike every store in this
// package built over migration 002, there is no "current row" distinction
// here -- a row simply exists or does not.
type MilestoneStore interface {
	// GetOrCreateRef resolves the `milestone_ref` row for (scopeID,
	// productID, name) -- name is the bare "M<n>" identifier the source
	// document spells (e.g. "M1") -- creating it if this is the first time
	// the importer has seen that identifier for productID. Returns
	// ErrNotFound if productID has no current `product` row (LB2
	// parentage, same enforcement as every Create in this package).
	//
	// This is the primitive that makes a second import of the same
	// document idempotent on milestone_ref: two calls with the same
	// (scopeID, productID, name) always resolve to the same row, never a
	// duplicate.
	GetOrCreateRef(ctx context.Context, scopeID, productID uuid.UUID, name string) (MilestoneRef, error)

	// AddAssociation inserts an `entity_milestone` row for (entityID,
	// milestoneID) if one does not already exist -- idempotent for the
	// same reason GetOrCreateRef is: re-importing the same document must
	// not duplicate the association LB6 specifies. entityID is a spec
	// entity's immutable id (a Feature.ID for a `Cn` citation, a
	// LoadBearingDecision.ID for an `LBn` citation); this method does not
	// validate that entityID names a real row of either table -- the
	// importer resolves entityID from its own just-written entities
	// before calling this, so there is nothing to look up here that the
	// caller does not already know.
	AddAssociation(ctx context.Context, scopeID, entityID, milestoneID uuid.UUID) error

	// ListAssociationsByMilestone returns every EntityMilestone row for
	// milestoneID, in creation order -- used by the importer's report
	// (FR16) and by tests asserting a `Must not foreclose: LB1, LB4` line
	// produced exactly two rows.
	ListAssociationsByMilestone(ctx context.Context, milestoneID uuid.UUID) ([]EntityMilestone, error)

	// ListRefsByProduct returns every MilestoneRef under (scopeID,
	// productID), ordered by Name -- a pure read, added for the renderer
	// (issue #2495, FR13): krill/render enumerates a product's milestones
	// this way to rebuild product/03-roadmap.md, then resolves each
	// milestone's own Delivers/Must-not-foreclose lists via
	// ListAssociationsByMilestone above. Name order is lexicographic
	// ("M1" < "M10" < "M2"); callers that need numeric milestone order
	// (krill/render does) re-sort by the parsed integer suffix themselves.
	ListRefsByProduct(ctx context.Context, scopeID, productID uuid.UUID) ([]MilestoneRef, error)
}

type milestoneStore struct{ pool *pgxpool.Pool }

var _ MilestoneStore = milestoneStore{}

const milestoneRefColumns = `id, scope_id, product_id, name, created_at`

func scanMilestoneRef(row pgx.Row) (MilestoneRef, error) {
	var m MilestoneRef
	err := row.Scan(&m.ID, &m.ScopeID, &m.ProductID, &m.Name, &m.CreatedAt)
	return m, err
}

func (s milestoneStore) GetOrCreateRef(ctx context.Context, scopeID, productID uuid.UUID, name string) (MilestoneRef, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	exists, err := currentRowExists(ctx, tx, "product", productID, scopeID)
	if err != nil {
		return MilestoneRef{}, err
	}
	if !exists {
		return MilestoneRef{}, errParentNotFound("product", productID)
	}

	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (scope_id, product_id, name) DO NOTHING
		RETURNING `+milestoneRefColumns,
		scopeID, productID, name))
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING returned no row: the ref already exists
		// from a prior import (or an earlier entity in this same import),
		// so fetch it instead.
		ref, err = scanMilestoneRef(tx.QueryRow(ctx, `
			SELECT `+milestoneRefColumns+`
			FROM milestone_ref
			WHERE scope_id = $1 AND product_id = $2 AND name = $3
		`, scopeID, productID, name))
	}
	if err != nil {
		return MilestoneRef{}, fmt.Errorf("get or create milestone_ref: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return MilestoneRef{}, fmt.Errorf("commit: %w", err)
	}
	return ref, nil
}

func (s milestoneStore) AddAssociation(ctx context.Context, scopeID, entityID, milestoneID uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (entity_id, milestone_id) DO NOTHING
	`, scopeID, entityID, milestoneID)
	if err != nil {
		return fmt.Errorf("insert entity_milestone: %w", err)
	}
	return nil
}

func (s milestoneStore) ListAssociationsByMilestone(ctx context.Context, milestoneID uuid.UUID) ([]EntityMilestone, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope_id, entity_id, milestone_id, created_at
		FROM entity_milestone
		WHERE milestone_id = $1
		ORDER BY created_at
	`, milestoneID)
	if err != nil {
		return nil, fmt.Errorf("list entity_milestone by milestone: %w", err)
	}
	defer rows.Close()

	var associations []EntityMilestone
	for rows.Next() {
		var m EntityMilestone
		if err := rows.Scan(&m.ID, &m.ScopeID, &m.EntityID, &m.MilestoneID, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entity_milestone: %w", err)
		}
		associations = append(associations, m)
	}
	return associations, rows.Err()
}

func (s milestoneStore) ListRefsByProduct(ctx context.Context, scopeID, productID uuid.UUID) ([]MilestoneRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+milestoneRefColumns+`
		FROM milestone_ref
		WHERE scope_id = $1 AND product_id = $2
		ORDER BY name
	`, scopeID, productID)
	if err != nil {
		return nil, fmt.Errorf("list milestone_ref by product: %w", err)
	}
	defer rows.Close()

	var refs []MilestoneRef
	for rows.Next() {
		ref, err := scanMilestoneRef(rows)
		if err != nil {
			return nil, fmt.Errorf("scan milestone_ref: %w", err)
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}
