// Package store is the pgx-based repository for krill's spec axis (issue
// #2488, M1): Product -> FeatureSet -> Feature -> {FR, NFR}, and
// FeatureSet -> LoadBearingDecision, plus the product-level Persona and
// NonGoal entities settled by the same task (see krill/ARCHITECTURE.md's
// "Capability map entries, personas, and non-goals" section). Store layer
// only -- no HTTP surface lands here (that is a separate task).
//
// Every type below shares one shape, per PRODUCT.md's LB2/LB3:
//
//   - ID is the immutable surrogate id, stable across every supersession of
//     the same logical entity. It is never rendered directly as a display
//     number ("FR7", "C4", "LB3") -- that rendering is a later task's job,
//     derived from Position (or from creation order) at render time.
//   - RevisionID is the SCD2 row key: unique per physical row, distinct from
//     ID because multiple revisions of one entity share the same ID.
//   - ScopeID is non-null on every row from this migration (LB1).
//   - Position carries sibling order and is explicitly not identity -- it
//     may be freely rewritten (e.g. by an insert-between-siblings) without
//     touching any other row's ID.
//   - ValidFrom/ValidTo are the SCD2 interval; ValidTo == nil means current.
//
// See krill/migrate/schema/migrations/002_spec_entities.up.sql for the
// full schema-level rationale, including why parent FK columns (e.g.
// FeatureSet.ProductID) are plain UUID columns rather than DB-enforced
// REFERENCES.
package store

import (
	"time"

	"github.com/google/uuid"
)

// Product is one row of `product` (migration 002) -- the top of the spec
// chain. One Product per krill-managed repo/domain in M1 (LB1 seeds one
// `scope` row); C22's second repo/tenant is a later scope row, not a
// second Product under this one.
type Product struct {
	RevisionID uuid.UUID
	ID         uuid.UUID
	ScopeID    uuid.UUID
	Name       string
	Vision     string
	Position   int
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// FeatureSet is one row of `feature_set` (migration 002) -- a grouping of
// related Features under a Product. Single parent: Product.ID.
type FeatureSet struct {
	RevisionID  uuid.UUID
	ID          uuid.UUID
	ScopeID     uuid.UUID
	ProductID   uuid.UUID
	Name        string
	Description *string
	Position    int
	ValidFrom   time.Time
	ValidTo     *time.Time
}

// Feature is one row of `feature` (migration 002). Single parent:
// FeatureSet.ID.
//
// Feature is also the entity a capability-map entry (`Cn`) resolves onto --
// see krill/ARCHITECTURE.md's "Capability map entries, personas, and
// non-goals" section. There is no separate Capability type: krill's model
// retires that vocabulary slot in favor of Feature (PRODUCT.md's
// product/02-capability-map.md, "a vocabulary collision, on purpose").
type Feature struct {
	RevisionID   uuid.UUID
	ID           uuid.UUID
	ScopeID      uuid.UUID
	FeatureSetID uuid.UUID
	Name         string
	Description  *string
	Position     int
	ValidFrom    time.Time
	ValidTo      *time.Time
}

// RequirementKind discriminates a functional from a non-functional
// requirement -- `requirement.kind`'s CHECK constraint (migration 002).
// Not a display number: "FR7" is rendered from Kind + a render-time
// position among same-Kind siblings, never stored as e.g. an `fr_number`
// column (LB2).
type RequirementKind string

const (
	RequirementKindFR  RequirementKind = "FR"
	RequirementKindNFR RequirementKind = "NFR"
)

// Requirement is one row of `requirement` (migration 002) -- an FR or an
// NFR, discriminated by Kind. Single parent: Feature.ID.
type Requirement struct {
	RevisionID uuid.UUID
	ID         uuid.UUID
	ScopeID    uuid.UUID
	FeatureID  uuid.UUID
	Kind       RequirementKind
	Name       string
	Body       *string
	Position   int
	ValidFrom  time.Time
	ValidTo    *time.Time
}

// LoadBearingDecision is one row of `load_bearing_decision` (migration
// 002) -- FR4's persistence for PRODUCT.md's `LB<n>` entries. Single
// parent: FeatureSet.ID (C2 -- attached to the area it constrains, not a
// global list a caller has to load in full). A later many-to-many for this
// specifically (C23, cross-product decisions) is deliberately deferred --
// see PRODUCT.md's LB2 "Stays cheap" clause.
type LoadBearingDecision struct {
	RevisionID   uuid.UUID
	ID           uuid.UUID
	ScopeID      uuid.UUID
	FeatureSetID uuid.UUID
	Name         string
	Body         *string
	Position     int
	ValidFrom    time.Time
	ValidTo      *time.Time
}

// Persona is one row of `persona` (migration 002) -- a product-level spec
// entity settled by this task (issue #2488) so the importer (FR16) and
// renderer (FR13) agree on its shape. Single parent: Product.ID.
type Persona struct {
	RevisionID  uuid.UUID
	ID          uuid.UUID
	ScopeID     uuid.UUID
	ProductID   uuid.UUID
	Name        string
	Description *string
	Position    int
	ValidFrom   time.Time
	ValidTo     *time.Time
}

// NonGoalKind discriminates PRODUCT.md's two Non-goals buckets --
// "Permanent" vs "Explicitly *not* non-goals -- deferred, not foreclosed."
// `non_goal.kind`'s CHECK constraint (migration 002).
type NonGoalKind string

const (
	NonGoalKindPermanent NonGoalKind = "permanent"
	NonGoalKindDeferred  NonGoalKind = "deferred"
)

// NonGoal is one row of `non_goal` (migration 002) -- the other
// brief-document entity kind this task settles. Single parent: Product.ID.
type NonGoal struct {
	RevisionID uuid.UUID
	ID         uuid.UUID
	ScopeID    uuid.UUID
	ProductID  uuid.UUID
	Kind       NonGoalKind
	Name       string
	Body       *string
	Position   int
	ValidFrom  time.Time
	ValidTo    *time.Time
}
