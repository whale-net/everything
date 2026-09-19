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

// MilestoneKind discriminates `milestone_ref.kind` (migration 010, issue
// #2683; widened by migration 011, issue #2684) -- CHECK-constrained to
// the two values below. Every existing consumer of MilestoneRef
// (ListRefsByProduct, krill/render, krill/importer) must filter on this
// field rather than assuming every row is a milestone.
type MilestoneKind string

const (
	MilestoneKindMilestone MilestoneKind = "milestone"
	// MilestoneKindMilepebble (migration 011, issue #2684, FR3) is a
	// sub-milestone container cut from exactly one milestone --
	// ParentMilestoneID is always set on a row of this kind, never on a
	// MilestoneKindMilestone row (the DB CHECK enforces this pairing, not
	// just this comment).
	MilestoneKindMilepebble MilestoneKind = "milepebble"
)

// MilestoneRef is one row of `milestone_ref` (migration 004, issue #2492,
// FR17, LB6; authoring fields added by migration 010, issue #2683, FR1/
// FR2; ParentMilestoneID added by migration 011, issue #2684, FR3).
// Originally a bare reference to an `M<n>` identifier a source document
// names -- Kind/Outcome/FRBudget/Position/CreatedByActing/
// CreatedByOnBehalfOf/ParentMilestoneID are additions since. Single
// parent: Product.ID (always) plus, for a milepebble, a second parent
// naming the milestone it was cut from (ParentMilestoneID) -- see this
// field's own doc comment for why that is a second FK, not a
// `milestone_id` column on a different table. Not SCD2 (LB3) -- see
// 004_milestone_assoc.up.sql's, 010_milestone_authoring.up.sql's, and
// 011_milepebble.up.sql's comments for why.
type MilestoneRef struct {
	ID        uuid.UUID
	ScopeID   uuid.UUID
	ProductID uuid.UUID
	Name      string // the bare "M<n>" identifier, e.g. "M1"
	CreatedAt time.Time

	// Kind discriminates this row's own shape (migration 010) -- see
	// MilestoneKind's doc comment. Every row from before migration 010
	// defaults to MilestoneKindMilestone.
	Kind MilestoneKind

	// ParentMilestoneID names the one milestone a milepebble was cut from
	// (migration 011, FR3) -- nil for a MilestoneKindMilestone row, always
	// set for a MilestoneKindMilepebble row (DB CHECK-enforced pairing).
	// This is a real MilestoneRef.ID, never the milepebble's own
	// Product.ID parentage, which is unchanged (a milepebble belongs to
	// the same Product its parent milestone does).
	ParentMilestoneID *uuid.UUID

	// Outcome is the milestone's outcome sentence (FR1), NULL until
	// CreateMilestone/SetOutcome sets it.
	Outcome *string

	// FRBudget is the milestone's FR budget (FR2), NULL until
	// CreateMilestone/SetFRBudget sets it.
	FRBudget *int

	// Position carries sibling order among a Product's milestones (FR7),
	// explicitly not identity -- see this package's doc comment.
	Position int

	// CreatedByActing/CreatedByOnBehalfOf are the LB4 subject pair
	// recorded when this row was created through the authoring path
	// (CreateMilestone). Both are nil for a row krill/importer's
	// GetOrCreateRef created -- an import has no session-attributable
	// actor to record; see migration 010's LB4 note for why this column
	// pair is nullable, unlike pointer_artifact's.
	CreatedByActing     *Subject
	CreatedByOnBehalfOf *Subject
}

// MilestoneRelation discriminates an `entity_milestone` row's delivery-axis
// meaning (migration 010, issue #2683, LB6): a "Delivers: C13, C28" line
// and a "Must not foreclose: LB2, LB6, LB7" line are both entity_milestone
// rows, distinguished only by this column -- never a second association
// table, and never a column on the spec entity itself.
type MilestoneRelation string

const (
	MilestoneRelationDelivers         MilestoneRelation = "delivers"
	MilestoneRelationMustNotForeclose MilestoneRelation = "must_not_foreclose"
)

// MilestoneDeferral is one row of `milestone_deferral` (migration 010,
// issue #2683, FR1) -- one deliberately-deferred item cited under a
// milestone's authoring content. Single parent: MilestoneRef.ID (a real
// DB-enforced REFERENCES -- milestone_ref is not SCD2, so its id is
// table-wide unique). Not SCD2 (LB3): a deferral is a fact, not a value
// that changes over time -- see migration 010's comment. Destination is
// never empty: FR1 requires every deferred entry to cite where it went.
type MilestoneDeferral struct {
	ID          uuid.UUID
	ScopeID     uuid.UUID
	MilestoneID uuid.UUID
	Body        string // what was deferred
	Destination string // the milestone or "Later" capability bucket it moved to
	Position    int
	CreatedAt   time.Time

	// CreatedByActing/CreatedByOnBehalfOf are always populated (NFR4) --
	// every write path onto this table is the new AddDeferral method,
	// which always has a real caller session.
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
}

// EntityMilestone is one row of `entity_milestone` (migration 004; the
// Relation discriminator added by migration 010, issue #2683, LB6) -- the
// delivery-axis association LB6 specifies, keyed `(entity_id,
// milestone_id, relation)`. EntityID is a spec entity's immutable `id` (to
// date, always a Feature.ID for a `Cn` citation or a LoadBearingDecision.ID
// for an `LBn` citation) -- never a `milestone_id` column added to that
// entity's own table. Not SCD2 (LB3).
type EntityMilestone struct {
	ID          uuid.UUID
	ScopeID     uuid.UUID
	EntityID    uuid.UUID
	MilestoneID uuid.UUID
	Relation    MilestoneRelation
	CreatedAt   time.Time
}

// MilestoneStatus discriminates `milestone_status_event.status` (migration
// 012, issue #2685, FR8, FR9) -- CHECK-constrained to exactly these seven
// values, shared verbatim by a MilestoneKindMilestone and a
// MilestoneKindMilepebble row (FR9). Not a column on `milestone_ref`: the
// current value is always derived from MilestoneStatusEventStore's
// append-only history (see MilestoneStatusEvent's doc comment) -- there is
// no `current_status` column anywhere, and MilestoneStatusNotStarted is
// never itself written as a row.
type MilestoneStatus string

const (
	// MilestoneStatusNotStarted is the derived answer when a milestone or
	// milepebble has zero MilestoneStatusEvent rows -- the absence of a
	// transition, never a seeded one (FR8).
	MilestoneStatusNotStarted        MilestoneStatus = "not started"
	MilestoneStatusInDesign          MilestoneStatus = "in design"
	MilestoneStatusPlanned           MilestoneStatus = "planned"
	MilestoneStatusInProgress        MilestoneStatus = "in progress"
	MilestoneStatusShipped           MilestoneStatus = "shipped"
	MilestoneStatusPartiallyComplete MilestoneStatus = "partially complete"
	MilestoneStatusAbandoned         MilestoneStatus = "abandoned"
)

// MilestoneStatusEvent is one row of `milestone_status_event` (migration
// 012, issue #2685, FR8, FR9, FR12). Single parent: MilestoneRef.ID (a
// real DB-enforced REFERENCES -- milestone_ref is not SCD2, so its id is
// table-wide unique), covering both a milestone and a milepebble row
// (FR9) since both share the one `milestone_ref` table. Append-only, NOT
// SCD2 (LB3, NFR2) -- see migration 012's boundary comment for why this
// table draws that line even though milestone_ref itself is plain
// mutable: a transition is an addition to history, never an overwrite,
// so this struct carries no ValidFrom/ValidTo pair and there is no store
// method that updates or deletes a row of this shape. CreatedByActing/
// CreatedByOnBehalfOf are always populated (NFR4) -- the only write path
// onto this table, RecordTransition, always has a real caller session.
type MilestoneStatusEvent struct {
	ID          uuid.UUID
	ScopeID     uuid.UUID
	MilestoneID uuid.UUID
	Status      MilestoneStatus
	Note        *string
	CreatedAt   time.Time

	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
}

// Scope is one row of `scope` (migration 001, issue #2487, LB1) -- the
// forge-coordinate row every other table's scope_id hangs off. Plain
// mutable config, not SCD2 (see migrations/001_scope.up.sql's boundary
// comment) -- ScopeStore (scope.go) exposes only a read, never a CRUD
// surface (ARCHITECTURE.md's "The scope table" section: "no UI exposes
// it"); PointerArtifactStore.Create (issue #2496, FR20) is the one
// exception, writing PointerIssueNumber directly in its own transaction
// once a pointer issue is minted.
type Scope struct {
	ID                 uuid.UUID
	RepoFullName       string
	DefaultBranch      string
	PointerIssueNumber *int
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// EventType discriminates a revision_event's round kind (migration 008,
// issue #2542, FR2) -- `revision_event.event_type`'s CHECK constraint,
// closed at exactly these five values. See DesignSession's doc comment
// for why FR8's opening submission is deliberately not a sixth value
// here.
type EventType string

const (
	EventTypeDraft          EventType = "draft"
	EventTypeReconciliation EventType = "reconciliation"
	EventTypeAnswer         EventType = "answer"
	EventTypeSignoff        EventType = "signoff"
	EventTypeRuling         EventType = "ruling"
)

// EntityDeltaChange discriminates the kind of change one entry of a
// revision_event's entity_deltas names -- `created` or `updated` only, no
// `deleted` (migration 008's comment: M1's entity model has no
// delete/retire operation, and no M2 FR retracts a proposed entity).
type EntityDeltaChange string

const (
	EntityDeltaChangeCreated EntityDeltaChange = "created"
	EntityDeltaChangeUpdated EntityDeltaChange = "updated"
)

// SignoffStatus is FR4's closed signoff outcome -- `revision_event.
// signoff_status`'s CHECK constraint. There is no free-text alternative:
// FR4 is explicit that a signoff does not accept free text in place of
// this enum.
type SignoffStatus string

const (
	SignoffStatusApproved         SignoffStatus = "approved"
	SignoffStatusChangesRequested SignoffStatus = "changes_requested"
)

// EntityDelta is one entry of a revision_event's entity_deltas JSONB
// array (migration 008, FR2) -- one entity a round touched, the kind of
// change, and a one-line human summary.
type EntityDelta struct {
	EntityID    uuid.UUID         `json:"entity_id"`
	Change      EntityDeltaChange `json:"change"`
	SummaryLine string            `json:"summary_line"`
}

// OpenQuestionOpened is one entry of OpenQuestionsDelta.Opened (migration
// 008, FR2/FR6) -- a question id together with the two properties FR6
// says are established at open time and never mutated: its blocking flag
// and its text. A later `resolved` entry names only the question id
// (issue #2545's settled shape) because a resolution never needs to
// restate either -- restating them would require exactly the mutable
// question row FR6/NFR1 forbid.
type OpenQuestionOpened struct {
	QuestionID string `json:"question_id"`
	Blocking   bool   `json:"blocking"`
	Text       string `json:"text"`
}

// OpenQuestionsDelta is a revision_event's open_questions_delta JSONB
// object (migration 008, FR2, FR6) -- the questions a round opened (with
// their blocking flag and text) and the bare question ids it resolved.
type OpenQuestionsDelta struct {
	Opened   []OpenQuestionOpened `json:"opened"`
	Resolved []string             `json:"resolved"`
}

// DesignSession is one row of `design_session` (migration 008, issue
// #2542, FR1/FR8) -- the longer-lived container a session's
// revision_event rounds accumulate under. Single parent: Product.ID.
//
// DesignSession is explicitly NOT KrillSession/Session (session.go,
// migration 003): a Session is the write-gate row FR3's `init` mints per
// mutating call, with one fixed acting/on-behalf-of pair; a DesignSession
// spans many separate Session-gated calls, from potentially different
// actors, over its lifetime. See krill/ARCHITECTURE.md's "design_session
// vs krill_session" section.
type DesignSession struct {
	ID                     uuid.UUID
	ScopeID                uuid.UUID
	ProductID              uuid.UUID
	OpeningSubmission      string
	OpenedByKrillSessionID SessionID
	CreatedAt              time.Time
}

// NewRevisionEvent is the caller-supplied shape RevisionEventStore.Append
// (revision_event.go) inserts (migration 008, issue #2542, FR2-FR4). The
// store layer never infers ScopeID or either identity triple from
// SessionID -- every caller passes them explicitly, matching session.go's
// InitSession contract ("the store layer never infers this -- every
// caller passes both explicitly").
type NewRevisionEvent struct {
	ScopeID            uuid.UUID
	SessionID          uuid.UUID
	Acting             Subject
	OnBehalfOf         Subject
	EventType          EventType
	EntityDeltas       []EntityDelta
	OpenQuestionsDelta OpenQuestionsDelta
	// VerifiedAgainst must be non-nil for EventTypeDraft/
	// EventTypeReconciliation and nil otherwise (FR3) -- Append validates
	// this in Go as well as the DDL CHECK, so a caller gets a named error
	// rather than a raw Postgres constraint violation.
	VerifiedAgainst *string
	// SignoffStatus must be non-nil for EventTypeSignoff and nil
	// otherwise (FR4) -- same double-enforcement as VerifiedAgainst.
	SignoffStatus *SignoffStatus
}

// RevisionEvent is one row of `revision_event` (migration 008, issue
// #2542, FR2-FR4, NFR1) -- one round of a DesignSession, append-only.
// NFR1: there is no Update/Delete/Amend anywhere on RevisionEventStore --
// a RevisionEvent, once Appended, is never mutated.
type RevisionEvent struct {
	ID                 uuid.UUID
	ScopeID            uuid.UUID
	SessionID          uuid.UUID
	SeqNo              int
	Acting             Subject
	OnBehalfOf         Subject
	EventType          EventType
	EntityDeltas       []EntityDelta
	OpenQuestionsDelta OpenQuestionsDelta
	VerifiedAgainst    *string
	SignoffStatus      *SignoffStatus
	CreatedAt          time.Time
}

// OpenQuestion is one row of RevisionEventStore.ListOpenQuestions' derived
// result (issue #2545, FR6) -- never a table of its own (see
// open_questions.go's package doc comment for why). OpenedAtSeqNo is the
// seq_no of the revision_event whose `opened` entry is currently winning
// for QuestionID (the latest one, per last-event-wins), not the first time
// the question id ever appeared.
type OpenQuestion struct {
	QuestionID    string
	Text          string
	Blocking      bool
	OpenedAtSeqNo int
}

// PointerArtifact is one row of `pointer_artifact` (migration 005, issue
// #2496, FR20, C9) -- the thin GitHub issue krill creates to stand in for
// a Product now that the spec itself lives in krill instead of a file.
// Single parent: Product.ID. Not SCD2 (LB3) -- see migration
// 005_pointer_artifact.up.sql's boundary comment. Kind is a discriminator
// on the artifact's own shape (today, always "github_issue") -- it does
// not describe what has since referenced the created issue; see that same
// migration comment for why no PR/commit/conversation-shaped column
// exists here (C20).
type PointerArtifact struct {
	ID          uuid.UUID
	ScopeID     uuid.UUID
	ProductID   uuid.UUID
	Kind        string
	IssueNumber int
	IssueURL    string
	// CreatedByActing/CreatedByOnBehalfOf are the two LB4 subjects
	// (mirrors krill_session's acting/on_behalf_of triples) attributing
	// who minted this pointer issue and on whose behalf.
	CreatedByActing     Subject
	CreatedByOnBehalfOf Subject
	CreatedAt           time.Time
}
