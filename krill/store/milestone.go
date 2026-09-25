package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// MilestoneStore covers `milestone_ref` and `entity_milestone` (migration
// 004, issue #2492, FR17, LB6) -- the bare delivery-axis reference and its
// association, and the only store surface the importer (FR16) writes on
// the delivery axis. `milestone_ref` became SCD2 in migration 020 (so a
// milestone's authoring fields are amendable, FR 39373553) while
// `entity_milestone` remains an append-only association, exactly as
// migration 004's LB3 note describes.
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
	// productID) whose Kind is MilestoneKindMilestone, ordered by Name --
	// a pure read, added for the renderer (issue #2495, FR13): krill/render
	// enumerates a product's milestones this way to rebuild
	// product/03-roadmap.md, then resolves each milestone's own Delivers/
	// Must-not-foreclose lists via ListAssociationsByMilestone above. Name
	// order is lexicographic ("M1" < "M10" < "M2"); callers that need
	// numeric milestone order (krill/render does) re-sort by the parsed
	// integer suffix themselves.
	//
	// Kind-filtered as of migration 010 (issue #2683): a later kind
	// (milepebble, backlog bucket) must never silently appear where a
	// caller asked for milestones -- see MilestoneKind's doc comment
	// (models.go).
	ListRefsByProduct(ctx context.Context, scopeID, productID uuid.UUID) ([]MilestoneRef, error)
}

type milestoneStore struct{ pool *pgxpool.Pool }

var _ MilestoneStore = milestoneStore{}

// milestoneRefColumns covers the bare migration-004 columns, the
// authoring columns migration 010 added (issue #2683),
// parent_milestone_id migration 011 added (issue #2684), and the SCD2
// triple migration 020 added: Kind/Outcome/FRBudget/Position/
// ParentMilestoneID plus the nullable LB4 subject pair, RevisionID and
// ValidFrom/ValidTo -- see scanMilestoneRef and MilestoneRef's doc
// comment (models.go) for why the subject-pair and parent columns may be
// NULL.
const milestoneRefColumns = `revision_id, id, scope_id, product_id, name, kind, outcome, fr_budget, position, parent_milestone_id, ` +
	`created_by_acting_iss, created_by_acting_sub, created_by_acting_kind, ` +
	`created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind, created_at, ` +
	`valid_from, valid_to`

func scanMilestoneRef(row pgx.Row) (MilestoneRef, error) {
	var m MilestoneRef
	var kind string
	var parentMilestoneID uuid.NullUUID
	var actingIss, actingSub, actingKind sql.NullString
	var onBehalfOfIss, onBehalfOfSub, onBehalfOfKind sql.NullString
	err := row.Scan(
		&m.RevisionID, &m.ID, &m.ScopeID, &m.ProductID, &m.Name, &kind, &m.Outcome, &m.FRBudget, &m.Position, &parentMilestoneID,
		&actingIss, &actingSub, &actingKind,
		&onBehalfOfIss, &onBehalfOfSub, &onBehalfOfKind,
		&m.CreatedAt, &m.ValidFrom, &m.ValidTo,
	)
	if err != nil {
		return MilestoneRef{}, err
	}
	m.Kind = MilestoneKind(kind)
	if parentMilestoneID.Valid {
		m.ParentMilestoneID = &parentMilestoneID.UUID
	}
	// The importer's GetOrCreateRef path writes no subject pair (no
	// session to attribute to) -- both sides are NULL together, never
	// independently, since every writer either sets both (CreateMilestone)
	// or neither (GetOrCreateRef).
	if actingIss.Valid {
		m.CreatedByActing = &Subject{Iss: actingIss.String, Sub: actingSub.String, Kind: SubjectKind(actingKind.String)}
	}
	if onBehalfOfIss.Valid {
		m.CreatedByOnBehalfOf = &Subject{Iss: onBehalfOfIss.String, Sub: onBehalfOfSub.String, Kind: SubjectKind(onBehalfOfKind.String)}
	}
	return m, nil
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

	// GetOrCreateRef only ever creates/resolves a MilestoneKindMilestone
	// row (kind defaults to 'milestone', parent_milestone_id stays NULL --
	// the importer has no notion of milepebbles). Both the ON CONFLICT
	// inference and the fallback SELECT below are scoped to
	// `parent_milestone_id IS NULL AND valid_to IS NULL` -- migration 011
	// (issue #2684) widened milestone_ref_scope_product_name_idx into a
	// partial index over the first predicate (a milepebble may share a
	// name with a milestone under the same product, since its own
	// uniqueness is scoped per-parent instead), and migration 020 added
	// the second, so a plain `ON CONFLICT (scope_id, product_id, name)`
	// matches no index at all and the WHERE clauses below are what keep
	// this method from ever resolving onto a same-named milepebble row or
	// a superseded revision.
	ref, err := scanMilestoneRef(tx.QueryRow(ctx, `
		INSERT INTO milestone_ref (scope_id, product_id, name)
		VALUES ($1, $2, $3)
		ON CONFLICT (scope_id, product_id, name) WHERE parent_milestone_id IS NULL AND valid_to IS NULL DO NOTHING
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
			  AND parent_milestone_id IS NULL AND valid_to IS NULL
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
	// relation is written explicitly as MilestoneRelationDelivers -- the
	// importer (FR16) has no concept of "must not foreclose", so every row
	// it writes is a Delivers row, same meaning this column defaults to
	// (migration 010's comment). The ON CONFLICT target must name every
	// column of entity_milestone_entity_milestone_idx (migration 010
	// widened it to (entity_id, milestone_id, relation)) -- naming only
	// the first two, as this method did before that migration, is no
	// longer a valid arbiter and fails at the database with "no unique or
	// exclusion constraint matching the ON CONFLICT specification".
	_, err := s.pool.Exec(ctx, `
		INSERT INTO entity_milestone (scope_id, entity_id, milestone_id, relation)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (entity_id, milestone_id, relation) DO NOTHING
	`, scopeID, entityID, milestoneID, string(MilestoneRelationDelivers))
	if err != nil {
		return fmt.Errorf("insert entity_milestone: %w", err)
	}
	return nil
}

func (s milestoneStore) ListAssociationsByMilestone(ctx context.Context, milestoneID uuid.UUID) ([]EntityMilestone, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, scope_id, entity_id, milestone_id, relation, created_at
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
		var relation string
		if err := rows.Scan(&m.ID, &m.ScopeID, &m.EntityID, &m.MilestoneID, &relation, &m.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan entity_milestone: %w", err)
		}
		m.Relation = MilestoneRelation(relation)
		associations = append(associations, m)
	}
	return associations, rows.Err()
}

func (s milestoneStore) ListRefsByProduct(ctx context.Context, scopeID, productID uuid.UUID) ([]MilestoneRef, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+milestoneRefColumns+`
		FROM milestone_ref
		WHERE scope_id = $1 AND product_id = $2 AND kind = $3 AND valid_to IS NULL
		ORDER BY name
	`, scopeID, productID, string(MilestoneKindMilestone))
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
