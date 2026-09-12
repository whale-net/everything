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

	// ListFeatureSetsCurrentByIDs, ListFeaturesCurrentByIDs,
	// ListRequirementsCurrentByIDs, and ListDecisionsCurrentByIDs back
	// krill/slice's GetEntitySetSlice (FR5, M2, issue #2544): the one
	// granularity whose membership is an explicit, heterogeneous set of
	// ids rather than a subtree, so it resolves that set by kind with one
	// `= ANY($1)` query per kind instead of one query per id -- same
	// discipline as every other method on this interface. An id in ids
	// that has no current row of that kind is simply absent from the
	// result; that is never an error at this layer (GetEntitySetSlice
	// treats "no current row of any kind" as skip-not-error).

	// ListFeatureSetsCurrentByIDs returns the current row for every id in
	// ids that still has a current `feature_set` row.
	ListFeatureSetsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]FeatureSet, error)

	// ListFeaturesCurrentByIDs returns the current row for every id in ids
	// that still has a current `feature` row.
	ListFeaturesCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]Feature, error)

	// ListRequirementsCurrentByIDs returns the current row for every id in
	// ids that still has a current `requirement` row.
	ListRequirementsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]Requirement, error)

	// ListDecisionsCurrentByIDs returns the current row for every id in
	// ids that still has a current `load_bearing_decision` row.
	ListDecisionsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]LoadBearingDecision, error)
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

// uniqueUUIDs deduplicates ids, mirroring
// audience_score_system/store/research.go's GetByIDs precedent -- a
// caller (GetEntitySetSlice) may pass a raw union of several revision
// events' entity_deltas without pre-deduplicating itself.
func uniqueUUIDs(ids []uuid.UUID) []uuid.UUID {
	seen := make(map[uuid.UUID]struct{}, len(ids))
	unique := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		unique = append(unique, id)
	}
	return unique
}

func (s sliceStore) ListFeatureSetsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]FeatureSet, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+featureSetColumns+`
		FROM feature_set
		WHERE id = ANY($1) AND valid_to IS NULL
	`, uniqueUUIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("list feature_sets by ids: %w", err)
	}
	defer rows.Close()

	var featureSets []FeatureSet
	for rows.Next() {
		fs, err := scanFeatureSet(rows)
		if err != nil {
			return nil, fmt.Errorf("scan feature_set: %w", err)
		}
		featureSets = append(featureSets, fs)
	}
	return featureSets, rows.Err()
}

func (s sliceStore) ListFeaturesCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]Feature, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+featureColumns+`
		FROM feature
		WHERE id = ANY($1) AND valid_to IS NULL
	`, uniqueUUIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("list features by ids: %w", err)
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

func (s sliceStore) ListRequirementsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]Requirement, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+requirementColumns+`
		FROM requirement
		WHERE id = ANY($1) AND valid_to IS NULL
	`, uniqueUUIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("list requirements by ids: %w", err)
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

func (s sliceStore) ListDecisionsCurrentByIDs(ctx context.Context, ids []uuid.UUID) ([]LoadBearingDecision, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+loadBearingDecisionColumns+`
		FROM load_bearing_decision
		WHERE id = ANY($1) AND valid_to IS NULL
	`, uniqueUUIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("list load_bearing_decisions by ids: %w", err)
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
