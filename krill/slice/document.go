// Package slice implements krill's C3 scoped-slice query (FR5-FR9, issue
// #2491): four granularities -- FeatureSet, Feature, Requirement, and
// whole Product -- each assembled into the same typed, self-describing
// Document. PRODUCT.md's LB7 is the reason there is exactly one document
// type here rather than four: "the scoped slice C3 returns is the same
// typed document M4's claim payload later carries," so a per-granularity
// response type would be the drift LB7 exists to prevent. See
// krill/ARCHITECTURE.md's "The scoped-slice query (LB7, issue #2491)"
// section for how this fits the rest of the domain.
package slice

import "github.com/google/uuid"

// SchemaVersion is Document's current explicit schema version (LB7). Bump
// it, and note the change in ARCHITECTURE.md, whenever an existing field
// is removed, renamed, or repurposed. Purely additive fields -- LB7's
// "stays cheap" list explicitly includes "adding fields to the document"
// -- do not require a bump.
const SchemaVersion = "1"

// EntityRef is the surrogate id + as-of revision every entity embeds
// (FR9): ID is the entity's immutable identity (LB2), stable across every
// supersession; RevisionID is the exact SCD2 row this entity's fields
// were read from -- the current row for query.go's four Get*Slice
// methods, or the row that was current at a past instant for their
// Get*SliceAsOf twins (issue #2493, backed by krill/store's HistoryStore
// -- see krill/ARCHITECTURE.md's "As-of slice assembly" section).
type EntityRef struct {
	ID         uuid.UUID `json:"id"`
	RevisionID uuid.UUID `json:"revision_id"`
}

// ProductEntity is one Product's slice payload.
type ProductEntity struct {
	EntityRef
	Name string `json:"name"`
	// Position is sibling order among a scope's Products, never a display
	// number -- LB2. A render-time pass (not this package) turns this
	// into a citation like "C4" or "FR7" from sibling position or
	// creation order; nothing here stores or emits one.
	Position int    `json:"position"`
	Vision   string `json:"vision"`
}

// FeatureSetEntity is one FeatureSet's slice payload.
type FeatureSetEntity struct {
	EntityRef
	ProductID   uuid.UUID `json:"product_id"`
	Name        string    `json:"name"`
	Description *string   `json:"description,omitempty"`
	Position    int       `json:"position"`
}

// FeatureEntity is one Feature's slice payload. A capability-map entry
// (`Cn`) resolves onto exactly this entity -- see
// krill/store/models.go's doc comment on store.Feature.
type FeatureEntity struct {
	EntityRef
	FeatureSetID uuid.UUID `json:"feature_set_id"`
	Name         string    `json:"name"`
	Description  *string   `json:"description,omitempty"`
	Position     int       `json:"position"`
}

// RequirementEntity is one FR or NFR's slice payload, discriminated by
// Kind (mirrors store.RequirementKind's two values, "FR" and "NFR").
type RequirementEntity struct {
	EntityRef
	FeatureID uuid.UUID `json:"feature_id"`
	Kind      string    `json:"kind"`
	Name      string    `json:"name"`
	Body      *string   `json:"body,omitempty"`
	Position  int       `json:"position"`
}

// DecisionEntity is one LoadBearingDecision's slice payload.
type DecisionEntity struct {
	EntityRef
	FeatureSetID uuid.UUID `json:"feature_set_id"`
	Name         string    `json:"name"`
	Body         *string   `json:"body,omitempty"`
	Position     int       `json:"position"`
}

// PointerArtifactEntity is one PointerArtifact's slice payload (issue
// #2496, FR20) -- the thin GitHub issue krill created for the Product this
// slice belongs to. Unlike every *Entity type above, it embeds only ID
// (no EntityRef/RevisionID): store.PointerArtifact is not SCD2 (LB3), so
// there is no revision concept to carry -- see store/models.go's doc
// comment on store.PointerArtifact.
type PointerArtifactEntity struct {
	ID          uuid.UUID `json:"id"`
	ProductID   uuid.UUID `json:"product_id"`
	Kind        string    `json:"kind"`
	IssueNumber int       `json:"issue_number"`
	IssueURL    string    `json:"issue_url"`
}

// Document is the one typed, self-describing shape every granularity in
// this package returns (FR5-FR9, LB7). Its shape does not vary by
// granularity -- a single-Requirement slice (GetRequirementSlice, FR7)
// and a whole-Product slice (GetProductSlice, FR8) are the same Document
// type with different populated extents: FR7 leaves every field but
// Requirements empty, FR8 populates all six (including PointerArtifacts,
// issue #2496, FR20 -- the only granularity that does, since a pointer
// artifact's single parent is the Product itself, never a FeatureSet or
// Feature). Do not add a per-granularity response type "for convenience"
// -- see this package's doc comment.
//
// A field being empty (nil slice, nil *ProductEntity) means "not part of
// this granularity's extent," never "queried and found none" -- e.g.
// GetFeatureSlice never populates Decisions, because FR6 promises "the
// Feature and its FRs/NFRs, and nothing else in the product," not "the
// Feature's FeatureSet's Decisions, which happen to be empty here."
type Document struct {
	SchemaVersion string `json:"schema_version"`

	Product          *ProductEntity          `json:"product,omitempty"`
	FeatureSets      []FeatureSetEntity      `json:"feature_sets,omitempty"`
	Features         []FeatureEntity         `json:"features,omitempty"`
	Requirements     []RequirementEntity     `json:"requirements,omitempty"`
	Decisions        []DecisionEntity        `json:"decisions,omitempty"`
	PointerArtifacts []PointerArtifactEntity `json:"pointer_artifacts,omitempty"`
}
