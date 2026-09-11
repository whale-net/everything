package store

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SliceStore covers the cross-table reads FR5's GetFeatureSetSlice and
// FR8's GetProductSlice need (issue #2491, krill/slice/query.go) that no
// single entity's *Store above owns on its own: Requirements live under a
// FeatureSet only by way of its Features (a two-hop join), and a whole
// Product's Features/Requirements/LoadBearingDecisions live under it only
// by way of its FeatureSets (a two- or three-hop join). Without these,
// query.go would have to loop a ListCurrentByFeatureSet/ListCurrentByFeature
// call per sibling -- one query per FeatureSet, then one per Feature -- to
// assemble a single slice.Document; these collapse that to one query per
// entity kind, regardless of how many FeatureSets or Features exist
// beneath the requested id.
//
// Every join leg re-asserts `valid_to IS NULL` on every table it crosses,
// not just the leaf table being selected -- a superseded Feature must not
// pull in Requirements it once owned, and a Requirement reachable only
// through a superseded Feature or FeatureSet must not appear either. This
// is the same current-row discipline as every *Store.GetCurrentByID and
// ListCurrentBy* above, just applied across a join instead of a single
// table (see AGENTS.md "SCD2").
type SliceStore interface {
	// ListRequirementsByFeatureSet returns every current Requirement (FR
	// and NFR) under every current Feature of featureSetID, ordered by
	// the owning Feature's Position and Name, then Requirement Kind,
	// Position, and Name.
	ListRequirementsByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]Requirement, error)

	// ListFeaturesByProduct returns every current Feature under every
	// current FeatureSet of productID, ordered by the owning FeatureSet's
	// Position and Name, then Feature Position and Name.
	ListFeaturesByProduct(ctx context.Context, productID uuid.UUID) ([]Feature, error)

	// ListRequirementsByProduct returns every current Requirement beneath
	// productID's whole current chain (FeatureSet -> Feature ->
	// Requirement), ordered by FeatureSet Position/Name, then Feature
	// Position/Name, then Requirement Kind/Position/Name.
	ListRequirementsByProduct(ctx context.Context, productID uuid.UUID) ([]Requirement, error)

	// ListDecisionsByProduct returns every current LoadBearingDecision
	// attached to any current FeatureSet of productID, ordered by the
	// owning FeatureSet's Position and Name, then LoadBearingDecision
	// Position and Name.
	ListDecisionsByProduct(ctx context.Context, productID uuid.UUID) ([]LoadBearingDecision, error)
}

type sliceStore struct{ pool *pgxpool.Pool }

var _ SliceStore = sliceStore{}

// qualifiedRequirementColumns mirrors requirementColumns (requirement.go)
// column-for-column, table-qualified so it can be selected alongside a
// JOIN without ambiguity -- the order must stay in lockstep with
// scanRequirement's Scan call.
const qualifiedRequirementColumns = `requirement.revision_id, requirement.id, requirement.scope_id, requirement.feature_id, requirement.kind, requirement.name, requirement.body, requirement.position, requirement.valid_from, requirement.valid_to`

// qualifiedFeatureColumns mirrors featureColumns (feature.go) the same way.
const qualifiedFeatureColumns = `feature.revision_id, feature.id, feature.scope_id, feature.feature_set_id, feature.name, feature.description, feature.position, feature.valid_from, feature.valid_to`

// qualifiedLoadBearingDecisionColumns mirrors loadBearingDecisionColumns
// (decision.go) the same way.
const qualifiedLoadBearingDecisionColumns = `load_bearing_decision.revision_id, load_bearing_decision.id, load_bearing_decision.scope_id, load_bearing_decision.feature_set_id, load_bearing_decision.name, load_bearing_decision.body, load_bearing_decision.position, load_bearing_decision.valid_from, load_bearing_decision.valid_to`

func (s sliceStore) ListRequirementsByFeatureSet(ctx context.Context, featureSetID uuid.UUID) ([]Requirement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+qualifiedRequirementColumns+`
		FROM requirement
		JOIN feature ON requirement.feature_id = feature.id AND feature.valid_to IS NULL
		WHERE feature.feature_set_id = $1 AND requirement.valid_to IS NULL
		ORDER BY feature.position, feature.name, requirement.kind, requirement.position, requirement.name
	`, featureSetID)
	if err != nil {
		return nil, fmt.Errorf("list requirements by feature_set: %w", err)
	}
	defer rows.Close()

	var requirements []Requirement
	for rows.Next() {
		r, err := scanRequirement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		requirements = append(requirements, r)
	}
	return requirements, rows.Err()
}

func (s sliceStore) ListFeaturesByProduct(ctx context.Context, productID uuid.UUID) ([]Feature, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+qualifiedFeatureColumns+`
		FROM feature
		JOIN feature_set ON feature.feature_set_id = feature_set.id AND feature_set.valid_to IS NULL
		WHERE feature_set.product_id = $1 AND feature.valid_to IS NULL
		ORDER BY feature_set.position, feature_set.name, feature.position, feature.name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list features by product: %w", err)
	}
	defer rows.Close()

	var features []Feature
	for rows.Next() {
		f, err := scanFeature(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feature: %w", err)
		}
		features = append(features, f)
	}
	return features, rows.Err()
}

func (s sliceStore) ListRequirementsByProduct(ctx context.Context, productID uuid.UUID) ([]Requirement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+qualifiedRequirementColumns+`
		FROM requirement
		JOIN feature ON requirement.feature_id = feature.id AND feature.valid_to IS NULL
		JOIN feature_set ON feature.feature_set_id = feature_set.id AND feature_set.valid_to IS NULL
		WHERE feature_set.product_id = $1 AND requirement.valid_to IS NULL
		ORDER BY feature_set.position, feature_set.name, feature.position, feature.name, requirement.kind, requirement.position, requirement.name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list requirements by product: %w", err)
	}
	defer rows.Close()

	var requirements []Requirement
	for rows.Next() {
		r, err := scanRequirement(rows)
		if err != nil {
			return nil, fmt.Errorf("scan requirement: %w", err)
		}
		requirements = append(requirements, r)
	}
	return requirements, rows.Err()
}

func (s sliceStore) ListDecisionsByProduct(ctx context.Context, productID uuid.UUID) ([]LoadBearingDecision, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+qualifiedLoadBearingDecisionColumns+`
		FROM load_bearing_decision
		JOIN feature_set ON load_bearing_decision.feature_set_id = feature_set.id AND feature_set.valid_to IS NULL
		WHERE feature_set.product_id = $1 AND load_bearing_decision.valid_to IS NULL
		ORDER BY feature_set.position, feature_set.name, load_bearing_decision.position, load_bearing_decision.name
	`, productID)
	if err != nil {
		return nil, fmt.Errorf("list decisions by product: %w", err)
	}
	defer rows.Close()

	var decisions []LoadBearingDecision
	for rows.Next() {
		d, err := scanLoadBearingDecision(rows)
		if err != nil {
			return nil, fmt.Errorf("scan load_bearing_decision: %w", err)
		}
		decisions = append(decisions, d)
	}
	return decisions, rows.Err()
}
