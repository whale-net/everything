// This file is the SCD2 close-and-open write path AGENTS.md's "SCD2"
// section describes, applied to every spec-axis entity kind: Product,
// FeatureSet, Feature, Requirement, Persona, NonGoal, LoadBearingDecision,
// Milestone, and a milestone's Deferral. Migration 002 already shipped
// every column the seven spec-axis tables need; migration 020 gave
// `milestone_ref` the same shape so a milestone's authoring fields are
// amendable too (FR 39373553), and migration 024 gave `milestone_deferral`
// the same shape so a deferral's body and destination are correctable in
// place.
package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AmendStore is the write side of SCD2 supersession: closing an entity's
// current row and opening a new one under the SAME surrogate id (LB2), for
// every spec-axis kind. No method here ever mints a new id, ever
// reparents, ever re-kinds, ever rewrites `position` or `display_number`,
// and never touches a sibling's row -- an amendment supersedes exactly one
// logical entity's own content.
//
// A Milestone's amend is the one that has to say what it does not touch:
// kind, product, parent milestone, position, and FR budget are carried
// forward unchanged, and the delivery axis (status transitions, Delivers,
// must-not-foreclose) lives in separate append-only tables keyed on the
// same immutable id, so a supersession never reaches them (FR 39373553).
// Deferrals used to be in that untouchable set; migration 024 moved them
// out of it, but only as far as AmendDeferral, which supersedes a single
// deferral's own text and still leaves every sibling deferral, the
// milestone's other axis, and the deferral's existence alone.
type AmendStore interface {
	// AmendProduct closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, and position, with name and
	// vision as given. Returns ErrNotFound if id has no current row.
	AmendProduct(ctx context.Context, id uuid.UUID, name, vision string) (Product, error)

	// AmendFeatureSet closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, and position,
	// with name and description as given.
	AmendFeatureSet(ctx context.Context, id uuid.UUID, name string, description *string) (FeatureSet, error)

	// AmendFeature closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, feature_set_id, position,
	// and display_number (the `Cn` a caller already cites, migration 017),
	// with name and description as given.
	AmendFeature(ctx context.Context, id uuid.UUID, name string, description *string) (Feature, error)

	// AmendRequirement closes the current row for id (`valid_to = NOW()`)
	// and inserts a new current row carrying the closed row's id, scope_id,
	// feature_id, kind, and position unchanged, with name and body as
	// given. Returns ErrNotFound if id has no current row.
	AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error)

	// AmendPersona closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, and position,
	// with name and description as given.
	AmendPersona(ctx context.Context, id uuid.UUID, name string, description *string) (Persona, error)

	// AmendNonGoal closes the current row for id and inserts a successor
	// carrying the closed row's id, scope_id, product_id, kind, and
	// position, with name and body as given. Kind is carried forward, never
	// re-chosen: a `deferred` Non-Goal becomes `permanent` (or is retired)
	// through the resolution verb, not through an amend.
	AmendNonGoal(ctx context.Context, id uuid.UUID, name string, body *string) (NonGoal, error)

	// AmendLoadBearingDecision closes the current row for id and inserts a
	// new current row carrying the closed row's id, scope_id,
	// feature_set_id, position, and display_number, with name and body as
	// given. Returns ErrNotFound if id has no current row.
	AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error)

	// AmendMilestone closes the milestone's current row and inserts a
	// successor carrying the closed row's id, scope_id, product_id, kind,
	// parent_milestone_id, position, FR budget, created_at, and LB4 subject
	// pair unchanged, with name and outcome as given. Everything on the
	// delivery axis is left exactly as it was.
	AmendMilestone(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error)

	// AmendMilepebble is AmendMilestone restricted to kind='milepebble': it
	// replaces name and outcome as a new revision under the same id, leaving
	// the FR budget, status history and delivery axis untouched. A milestone
	// id is ErrNotFound here.
	AmendMilepebble(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error)

	// CurrentPlacement reads the placement columns of the entity's current
	// row -- the parent id that kind has, and its kind -- so an amend
	// surface can tell a caller echoing its own placement from one trying
	// to move or re-kind the entity (FR b62ed47a). A field the kind has no
	// column for reads as absent. Returns ErrNotFound if id has no
	// current row. It is the only read in this file, and no Amend* method
	// consults it: the guard stays a refusal the surface decides before any
	// supersede runs.
	CurrentPlacement(ctx context.Context, entityKind string, id uuid.UUID) (AmendPlacementChange, error)

	// AmendDeferral closes the deferral's current row and inserts a
	// successor carrying the closed row's id, scope_id, milestone_id,
	// position, created_at, and LB4 subject pair unchanged, with body and
	// destination as given. An empty destination is refused, exactly as
	// AddDeferral refuses one (FR1). Returns ErrNotFound if id has no
	// current row.
	AmendDeferral(ctx context.Context, id uuid.UUID, body, destination string, capabilityID *uuid.UUID) (MilestoneDeferral, error)
}

type amendStore struct{ pool *pgxpool.Pool }

var _ AmendStore = amendStore{}

// supersede is the shared body of every Amend* method: it row-locks the
// current row for id, closes it (valid_to = NOW()), and runs open -- which
// inserts the successor revision under the same immutable id -- all inside
// one transaction.
//
// The FOR UPDATE is what makes a concurrent amend of the same id
// serialize rather than race: both would otherwise be eligible to win the
// `(id) WHERE valid_to IS NULL` partial unique index, turning a legitimate
// race into a 500 instead of a pair of amendments applied in order.
//
// table and columns are always this package's own constant names, never
// caller input, so building the statements with fmt.Sprintf carries no
// injection risk (matches currentRowExists' same note in errors.go).
func supersede[T any](
	ctx context.Context,
	pool *pgxpool.Pool,
	table, columns string,
	scan func(pgx.Row) (T, error),
	id uuid.UUID,
	open func(ctx context.Context, q txQuerier, current T) (T, error),
) (T, error) {
	var zero T

	tx, err := pool.Begin(ctx)
	if err != nil {
		return zero, fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck

	current, err := scan(tx.QueryRow(ctx, fmt.Sprintf(`
		SELECT %s
		FROM %s
		WHERE id = $1 AND valid_to IS NULL
		FOR UPDATE
	`, columns, table), id))
	if errors.Is(err, pgx.ErrNoRows) {
		return zero, fmt.Errorf("%w: %s id %s", ErrNotFound, table, id)
	}
	if err != nil {
		return zero, fmt.Errorf("get current %s for amend: %w", table, err)
	}

	if _, err := tx.Exec(ctx, fmt.Sprintf(
		`UPDATE %s SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL`, table), id); err != nil {
		return zero, fmt.Errorf("close current %s: %w", table, err)
	}

	amended, err := open(ctx, tx, current)
	if err != nil {
		return zero, err
	}

	if err := tx.Commit(ctx); err != nil {
		return zero, fmt.Errorf("commit: %w", err)
	}
	return amended, nil
}

func (s amendStore) AmendProduct(ctx context.Context, id uuid.UUID, name, vision string) (Product, error) {
	return supersede(ctx, s.pool, "product", productColumns, scanProduct, id,
		func(ctx context.Context, q txQuerier, current Product) (Product, error) {
			amended, err := scanProduct(q.QueryRow(ctx, `
				INSERT INTO product (id, scope_id, name, vision, position)
				VALUES ($1, $2, $3, $4, $5)
				RETURNING `+productColumns,
				current.ID, current.ScopeID, name, vision, current.Position))
			return amended, errNameConflict("product", "insert amended product", err)
		})
}

func (s amendStore) AmendFeatureSet(ctx context.Context, id uuid.UUID, name string, description *string) (FeatureSet, error) {
	return supersede(ctx, s.pool, "feature_set", featureSetColumns, scanFeatureSet, id,
		func(ctx context.Context, q txQuerier, current FeatureSet) (FeatureSet, error) {
			amended, err := scanFeatureSet(q.QueryRow(ctx, `
				INSERT INTO feature_set (id, scope_id, product_id, name, description, position)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING `+featureSetColumns,
				current.ID, current.ScopeID, current.ProductID, name, description, current.Position))
			return amended, errNameConflict("feature_set", "insert amended feature_set", err)
		})
}

func (s amendStore) AmendFeature(ctx context.Context, id uuid.UUID, name string, description *string) (Feature, error) {
	return supersede(ctx, s.pool, "feature", featureColumns, scanFeature, id,
		func(ctx context.Context, q txQuerier, current Feature) (Feature, error) {
			amended, err := scanFeature(q.QueryRow(ctx, `
				INSERT INTO feature (id, scope_id, feature_set_id, name, description, position, display_number)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+featureColumns,
				current.ID, current.ScopeID, current.FeatureSetID, name, description, current.Position, current.DisplayNumber))
			return amended, errNameConflict("feature", "insert amended feature", err)
		})
}

func (s amendStore) AmendRequirement(ctx context.Context, id uuid.UUID, name string, body *string) (Requirement, error) {
	return supersede(ctx, s.pool, "requirement", requirementColumns, scanRequirement, id,
		func(ctx context.Context, q txQuerier, current Requirement) (Requirement, error) {
			amended, err := scanRequirement(q.QueryRow(ctx, `
				INSERT INTO requirement (id, scope_id, feature_id, kind, name, body, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+requirementColumns,
				current.ID, current.ScopeID, current.FeatureID, string(current.Kind), name, body, current.Position))
			return amended, errNameConflict("requirement", "insert amended requirement", err)
		})
}

func (s amendStore) AmendPersona(ctx context.Context, id uuid.UUID, name string, description *string) (Persona, error) {
	return supersede(ctx, s.pool, "persona", personaColumns, scanPersona, id,
		func(ctx context.Context, q txQuerier, current Persona) (Persona, error) {
			amended, err := scanPersona(q.QueryRow(ctx, `
				INSERT INTO persona (id, scope_id, product_id, name, description, position)
				VALUES ($1, $2, $3, $4, $5, $6)
				RETURNING `+personaColumns,
				current.ID, current.ScopeID, current.ProductID, name, description, current.Position))
			return amended, errNameConflict("persona", "insert amended persona", err)
		})
}

func (s amendStore) AmendNonGoal(ctx context.Context, id uuid.UUID, name string, body *string) (NonGoal, error) {
	return supersede(ctx, s.pool, "non_goal", nonGoalColumns, scanNonGoal, id,
		func(ctx context.Context, q txQuerier, current NonGoal) (NonGoal, error) {
			amended, err := scanNonGoal(q.QueryRow(ctx, `
				INSERT INTO non_goal (id, scope_id, product_id, kind, name, body, position)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+nonGoalColumns,
				current.ID, current.ScopeID, current.ProductID, string(current.Kind), name, body, current.Position))
			return amended, errNameConflict("non_goal", "insert amended non_goal", err)
		})
}

func (s amendStore) AmendLoadBearingDecision(ctx context.Context, id uuid.UUID, name string, body *string) (LoadBearingDecision, error) {
	return supersede(ctx, s.pool, "load_bearing_decision", loadBearingDecisionColumns, scanLoadBearingDecision, id,
		func(ctx context.Context, q txQuerier, current LoadBearingDecision) (LoadBearingDecision, error) {
			amended, err := scanLoadBearingDecision(q.QueryRow(ctx, `
				INSERT INTO load_bearing_decision (id, scope_id, feature_set_id, name, body, position, display_number)
				VALUES ($1, $2, $3, $4, $5, $6, $7)
				RETURNING `+loadBearingDecisionColumns,
				current.ID, current.ScopeID, current.FeatureSetID, name, body, current.Position, current.DisplayNumber))
			return amended, errNameConflict("load_bearing_decision", "insert amended load_bearing_decision", err)
		})
}

func (s amendStore) AmendMilestone(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error) {
	return supersede(ctx, s.pool, "milestone_ref", milestoneRefColumns, scanMilestoneRef, id,
		func(ctx context.Context, q txQuerier, current MilestoneRef) (MilestoneRef, error) {
			args := []any{
				current.ID, current.ScopeID, current.ProductID, name, string(current.Kind), outcome,
				current.FRBudget, current.Position, current.ParentMilestoneID,
			}
			args = append(args, subjectArgs(current.CreatedByActing)...)
			args = append(args, subjectArgs(current.CreatedByOnBehalfOf)...)
			args = append(args, current.CreatedAt)

			amended, err := scanMilestoneRef(q.QueryRow(ctx, `
				INSERT INTO milestone_ref (
					id, scope_id, product_id, name, kind, outcome, fr_budget, position, parent_milestone_id,
					created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
					created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind,
					created_at
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
				RETURNING `+milestoneRefColumns, args...))
			return amended, errNameConflict("milestone_ref", "insert amended milestone_ref", err)
		})
}

func (s amendStore) AmendMilepebble(ctx context.Context, id uuid.UUID, name string, outcome *string) (MilestoneRef, error) {
	placement, err := s.CurrentPlacement(ctx, "milepebble", id)
	if err != nil {
		return MilestoneRef{}, err
	}
	if placement.Kind == nil || *placement.Kind != string(MilestoneKindMilepebble) {
		return MilestoneRef{}, fmt.Errorf("%w: milepebble id %s", ErrNotFound, id)
	}
	return s.AmendMilestone(ctx, id, name, outcome)
}

// AmendDeferral supersedes one deferral's text under its unchanged id
// (migration 024, which made `milestone_deferral` SCD2 so this path could
// exist at all). FR1's destination rule is enforced up front, the same
// refusal AddDeferral gives, so an amend can never write the one row shape
// AddDeferral would have rejected.
//
// The successor carries the closed row's `created_by_acting` /
// `created_by_on_behalf_of` forward rather than recording the amender's
// own subject pair, and that is the same choice AmendMilestone makes: the
// columns name who deferred the item, which is a fact about when it was
// deferred and does not change when its wording is corrected. Overwriting
// them would make "when, and by whom, was this deferred" unreadable, which
// is the exact provenance delete-and-recreate already destroyed. So a
// corrected deferral keeps its original authorship and reads as a revision
// of it, while the superseded row beside it retains the pre-correction text
// in full.
func (s amendStore) AmendDeferral(ctx context.Context, id uuid.UUID, body, destination string, capabilityID *uuid.UUID) (MilestoneDeferral, error) {
	if destination == "" {
		return MilestoneDeferral{}, fmt.Errorf("destination: required -- every deferred entry must cite where it went (FR1)")
	}
	// A nil capabilityID leaves the deferral's current citation unchanged.
	return supersede(ctx, s.pool, "milestone_deferral", milestoneDeferralColumns, scanMilestoneDeferral, id,
		func(ctx context.Context, q txQuerier, current MilestoneDeferral) (MilestoneDeferral, error) {
			if capabilityID == nil {
				capabilityID = current.CapabilityID
			} else if ok, err := currentRowExists(ctx, q, "feature", *capabilityID, current.ScopeID); err != nil {
				return MilestoneDeferral{}, err
			} else if !ok {
				return MilestoneDeferral{}, errParentNotFound("feature", *capabilityID)
			}
			amended, err := scanMilestoneDeferral(q.QueryRow(ctx, `
				INSERT INTO milestone_deferral (
					id, scope_id, milestone_id, body, destination, position,
					created_by_acting_iss, created_by_acting_sub, created_by_acting_kind,
					created_by_on_behalf_of_iss, created_by_on_behalf_of_sub, created_by_on_behalf_of_kind,
					created_at, capability_id
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14)
				RETURNING `+milestoneDeferralColumns,
				current.ID, current.ScopeID, current.MilestoneID, body, destination, current.Position,
				current.CreatedByActing.Iss, current.CreatedByActing.Sub, string(current.CreatedByActing.Kind),
				current.CreatedByOnBehalfOf.Iss, current.CreatedByOnBehalfOf.Sub, string(current.CreatedByOnBehalfOf.Kind),
				current.CreatedAt, capabilityID))
			if err != nil {
				return MilestoneDeferral{}, fmt.Errorf("insert amended milestone_deferral: %w", err)
			}
			return amended, nil
		})
}

// subjectArgs renders a milestone_ref row's nullable LB4 subject pair into
// the three query arguments its columns take. A row written by the
// importer records no subject at all (migration 010's LB4 note), so a nil
// Subject becomes three NULLs rather than a zero-value triple.
func subjectArgs(s *Subject) []any {
	if s == nil {
		return []any{nil, nil, nil}
	}
	return []any{s.Iss, s.Sub, string(s.Kind)}
}

// AmendPlacementChange is the reparent/re-kind guard an amend request body
// may carry (FR f0f6bc18). Amend replaces an entity's content under its
// unchanged id, parent, and kind; none of these fields is ever applied.
// They exist so that a caller attempting a move or a re-kind gets the named
// refusal Refuse reports, naming the operation to use instead, rather than
// a generic unknown-field decode error -- and so that a caller echoing back
// the placement it read is not refused for it (FR b62ed47a). The same
// struct is what CurrentPlacement returns, which is how Refuse compares a
// submitted value against the entity's own.
type AmendPlacementChange struct {
	ProductID         *string `json:"product_id,omitempty"`
	FeatureSetID      *string `json:"feature_set_id,omitempty"`
	FeatureID         *string `json:"feature_id,omitempty"`
	ParentMilestoneID *string `json:"parent_milestone_id,omitempty"`
	Kind              *string `json:"kind,omitempty"`
}

// Sent reports whether the caller submitted any placement field at all. A
// body that submits none has nothing to compare, so a surface can skip the
// current-placement read entirely rather than pay for it on every amend.
func (p AmendPlacementChange) Sent() bool {
	return p.ProductID != nil || p.FeatureSetID != nil || p.FeatureID != nil ||
		p.ParentMilestoneID != nil || p.Kind != nil
}

// ErrPlacementChange is the named refusal an amend returns when a caller
// asks to reparent or re-kind. A move is its own verb where one exists and
// a create-then-void pair where one does not; amend never is one.
var ErrPlacementChange = errors.New("krill/store: amend cannot reparent or re-kind an entity")

// parentColumn names the column that holds each kind's parent, or "" for a
// kind that is not parented inside a Product. A field that is not this
// kind's parent column is not a placement of the kind at all, which is a
// different -- and more useful -- refusal than "you cannot move this".
var parentColumn = map[string]string{
	"feature set":           "product_id",
	"feature":               "feature_set_id",
	"requirement":           "feature_id",
	"load-bearing decision": "feature_set_id",
	"persona":               "product_id",
	"non-goal":              "product_id",
	"milestone":             "product_id",
	"milepebble":            "product_id",
}

// placementAdvice names the operation to use instead of the refused
// change, per (entityKind, field). The verb is named only where one
// exists: a Feature has reparent_feature and a Non-Goal has
// resolve_non_goal, and telling a Milestone to use a Feature's verb would
// be false in both halves.
func placementAdvice(entityKind, field string) string {
	switch {
	case entityKind == "feature" && field == "feature_set_id":
		return "reparent it with reparent_feature, which moves a feature between feature sets of the same product under the same id"
	case entityKind == "non-goal" && field == "kind":
		return "re-kind a non_goal with resolve_non_goal, which settles a deferred non-goal by promoting it to permanent or retiring it"
	case entityKind == "requirement" && field == "kind":
		return "a requirement's FR/NFR kind is fixed at creation -- create the requirement with the kind you want, then void this one"
	case parentColumn[entityKind] == field:
		if entityKind == "milestone" || entityKind == "milepebble" {
			return "a milestone's parent is fixed at create, and a milepebble's parent is set by the cut that made it -- create the milestone under the parent you want, then abandon_milestone this one"
		}
		return fmt.Sprintf("a %s has no reparent verb -- create the %s under the parent you want, then void this one", entityKind, entityKind)
	case (entityKind == "milestone" || entityKind == "milepebble") && field == "parent_milestone_id":
		return "only a milepebble is parented to a milestone, and that parent is set by the cut that created it -- create the milepebble under the milestone you want, then abandon_milestone this one"
	default:
		return fmt.Sprintf("%s is not a placement of a %s, so there is nothing here to reparent", field, entityKind)
	}
}

// Refuse reports ErrPlacementChange naming entityKind and the first field
// whose SUBMITTED value differs from current, the entity's own placement as
// CurrentPlacement read it -- or nil when every submitted value is the one
// the entity already has. An omitted field was never sent and is never a
// difference; a field the kind has no column for has an absent current
// value, so any submitted value differs, an empty string included (FR
// b62ed47a).
func (p AmendPlacementChange) Refuse(entityKind string, current AmendPlacementChange) error {
	for _, field := range []struct {
		name      string
		submitted *string
		current   *string
	}{
		{"product_id", p.ProductID, current.ProductID},
		{"feature_set_id", p.FeatureSetID, current.FeatureSetID},
		{"feature_id", p.FeatureID, current.FeatureID},
		{"parent_milestone_id", p.ParentMilestoneID, current.ParentMilestoneID},
		{"kind", p.Kind, current.Kind},
	} {
		if placementDiffers(field.submitted, field.current) {
			return fmt.Errorf("%w: %s cannot change %s on amend -- %s",
				ErrPlacementChange, entityKind, field.name, placementAdvice(entityKind, field.name))
		}
	}
	return nil
}

// placementDiffers is the whole narrowing: a field the caller did not send
// is never a difference, and a sent field is one only when the current value
// is absent or holds something else.
func placementDiffers(submitted, current *string) bool {
	if submitted == nil {
		return false
	}
	return current == nil || *submitted != *current
}

// placementSources maps each amendable kind, under the same spelling the
// surfaces pass Refuse, to the table its current row lives in and to which
// of the five placement fields that table has a column for. A field absent
// from the map entry has no column, so it reads back as an absent current
// value.
var placementSources = map[string]struct {
	table                                                       string
	productID, featureSetID, featureID, parentMilestoneID, kind string
}{
	"product":               {table: "product"},
	"feature set":           {table: "feature_set", productID: "product_id"},
	"feature":               {table: "feature", featureSetID: "feature_set_id"},
	"requirement":           {table: "requirement", featureID: "feature_id", kind: "kind"},
	"persona":               {table: "persona", productID: "product_id"},
	"non-goal":              {table: "non_goal", productID: "product_id", kind: "kind"},
	"load-bearing decision": {table: "load_bearing_decision", featureSetID: "feature_set_id"},
	"milepebble":            {table: "milestone_ref", productID: "product_id", parentMilestoneID: "parent_milestone_id", kind: "kind"},
	"milestone":             {table: "milestone_ref", productID: "product_id", parentMilestoneID: "parent_milestone_id", kind: "kind"},
}

func (s amendStore) CurrentPlacement(ctx context.Context, entityKind string, id uuid.UUID) (AmendPlacementChange, error) {
	src, ok := placementSources[entityKind]
	if !ok {
		return AmendPlacementChange{}, fmt.Errorf("krill/store: %q is not an amendable entity kind", entityKind)
	}

	// A constant NULL stands in for every field the kind has no column for,
	// so the scan below reads AmendPlacementChange's field order either way.
	selected := make([]string, 0, 5)
	for _, column := range []string{src.productID, src.featureSetID, src.featureID, src.parentMilestoneID, src.kind} {
		if column == "" {
			selected = append(selected, "NULL")
			continue
		}
		selected = append(selected, column)
	}

	var productID, featureSetID, featureID, parentMilestoneID *uuid.UUID
	var kind *string
	row := s.pool.QueryRow(ctx, fmt.Sprintf(
		`SELECT %s FROM %s WHERE id = $1 AND valid_to IS NULL`,
		strings.Join(selected, ", "), src.table), id)
	if err := row.Scan(&productID, &featureSetID, &featureID, &parentMilestoneID, &kind); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AmendPlacementChange{}, fmt.Errorf("%w: %s id %s", ErrNotFound, src.table, id)
		}
		return AmendPlacementChange{}, fmt.Errorf("get current %s placement: %w", src.table, err)
	}

	return AmendPlacementChange{
		ProductID:         uuidString(productID),
		FeatureSetID:      uuidString(featureSetID),
		FeatureID:         uuidString(featureID),
		ParentMilestoneID: uuidString(parentMilestoneID),
		Kind:              kind,
	}, nil
}

// uuidString renders a nullable placement column as the *string the
// AmendPlacementChange the caller compares against carries; a NULL column --
// a kind with no parent, a top-level milestone's own parent_milestone_id --
// stays nil so any submitted value reads as a difference.
func uuidString(id *uuid.UUID) *string {
	if id == nil {
		return nil
	}
	s := id.String()
	return &s
}
