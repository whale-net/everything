package slice

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// Querier assembles Document values from krill/store -- the one place
// FR5-FR8's four granularities are implemented, so the later MCP tool
// (LB7, "M1's MCP tool is a thin wrapper over it, not the thing itself")
// has one call to wrap per granularity instead of a bespoke join each.
//
// Not gated by `init`/session (FR3 is write-only; read paths never
// require it -- see PRODUCT.md and root plan issue #2485).
type Querier struct {
	store *store.Store
}

// NewQuerier returns a Querier backed by s.
func NewQuerier(s *store.Store) *Querier {
	return &Querier{store: s}
}

// GetFeatureSetSlice is FR5: the FeatureSet, its Features, their FRs/NFRs,
// and only the LoadBearingDecisions attached to that FeatureSet -- never
// the product-wide decision list, which is the one FR5 clause a naive
// join across all of a Product's decisions would get wrong (this method
// scopes its ListDecisionsByFeatureSet-equivalent call to featureSetID
// alone, via store.LoadBearingDecisionStore.ListCurrentByFeatureSet).
func (q *Querier) GetFeatureSetSlice(ctx context.Context, featureSetID uuid.UUID) (Document, error) {
	featureSet, err := q.store.FeatureSets().GetCurrentByID(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("get feature_set: %w", err)
	}

	features, err := q.store.Features().ListCurrentByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list features by feature_set: %w", err)
	}

	requirements, err := q.store.Slices().ListRequirementsByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by feature_set: %w", err)
	}

	decisions, err := q.store.Decisions().ListCurrentByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list decisions by feature_set: %w", err)
	}

	return Document{
		SchemaVersion: SchemaVersion,
		FeatureSets:   []FeatureSetEntity{toFeatureSetEntity(featureSet)},
		Features:      toFeatureEntities(features),
		Requirements:  toRequirementEntities(requirements),
		Decisions:     toDecisionEntities(decisions),
	}, nil
}

// GetFeatureSlice is FR6: the Feature and its FRs/NFRs, and nothing else
// in the product -- no FeatureSet, no Decisions.
func (q *Querier) GetFeatureSlice(ctx context.Context, featureID uuid.UUID) (Document, error) {
	feature, err := q.store.Features().GetCurrentByID(ctx, featureID)
	if err != nil {
		return Document{}, fmt.Errorf("get feature: %w", err)
	}

	requirements, err := q.store.Requirements().ListCurrentByFeature(ctx, featureID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by feature: %w", err)
	}

	return Document{
		SchemaVersion: SchemaVersion,
		Features:      []FeatureEntity{toFeatureEntity(feature)},
		Requirements:  toRequirementEntities(requirements),
	}, nil
}

// GetRequirementSlice is FR7: a single FR or NFR by surrogate id alone.
func (q *Querier) GetRequirementSlice(ctx context.Context, requirementID uuid.UUID) (Document, error) {
	requirement, err := q.store.Requirements().GetCurrentByID(ctx, requirementID)
	if err != nil {
		return Document{}, fmt.Errorf("get requirement: %w", err)
	}

	return Document{
		SchemaVersion: SchemaVersion,
		Requirements:  []RequirementEntity{toRequirementEntity(requirement)},
	}, nil
}

// GetProductSlice is FR8: every FeatureSet, Feature, FR, NFR, and
// LoadBearingDecision beneath the Product, in one call.
func (q *Querier) GetProductSlice(ctx context.Context, productID uuid.UUID) (Document, error) {
	product, err := q.store.Products().GetCurrentByID(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("get product: %w", err)
	}

	featureSets, err := q.store.FeatureSets().ListCurrentByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list feature_sets by product: %w", err)
	}

	features, err := q.store.Slices().ListFeaturesByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list features by product: %w", err)
	}

	requirements, err := q.store.Slices().ListRequirementsByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by product: %w", err)
	}

	decisions, err := q.store.Slices().ListDecisionsByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list decisions by product: %w", err)
	}

	productEntity := toProductEntity(product)
	return Document{
		SchemaVersion: SchemaVersion,
		Product:       &productEntity,
		FeatureSets:   toFeatureSetEntities(featureSets),
		Features:      toFeatureEntities(features),
		Requirements:  toRequirementEntities(requirements),
		Decisions:     toDecisionEntities(decisions),
	}, nil
}

// -- as-of assembly (FR11 x FR5-FR8, issue #2493) ------------------------
//
// Each granularity above has an *AsOf twin below: the same shape of
// Document, assembled from the revisions that were current at asOf
// instead of today. This is what closes #2493's own Testing bullet ("a
// slice query at a past as-of assembles from the historical revisions,
// and its as-of revisions metadata reflects them") -- "as-of revisions
// metadata" is EntityRef.RevisionID (see document.go), which already
// carries whichever row a Document's entities were read from; these
// methods just make that row a historical one instead of always today's.
//
// Requirement and LoadBearingDecision are the only two kinds
// krill/store/amend.go can amend (issue #2493) -- Product, FeatureSet, and
// Feature have no write path that supersedes a row yet (see
// krill/ARCHITECTURE.md's "Open items"), so their current row is their
// only revision ever. For those three, "as of asOf" therefore reduces to
// "had it been created by asOf" (entityExistedAsOf), never a historical
// re-read. For Requirement/LoadBearingDecision, it is a real historical
// re-read through krill/store's HistoryStore (issue #2493) -- the exact
// revision that was current at asOf, and never-existed-yet entities are
// dropped rather than reported as their current revision.

// entityExistedAsOf reports whether an entity whose current row's
// ValidFrom is validFrom had already been created by asOf.
func entityExistedAsOf(validFrom, asOf time.Time) bool {
	return !validFrom.After(asOf)
}

// GetFeatureSetSliceAsOf is GetFeatureSetSlice assembled as of asOf.
func (q *Querier) GetFeatureSetSliceAsOf(ctx context.Context, featureSetID uuid.UUID, asOf time.Time) (Document, error) {
	featureSet, err := q.store.FeatureSets().GetCurrentByID(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("get feature_set: %w", err)
	}
	if !entityExistedAsOf(featureSet.ValidFrom, asOf) {
		return Document{}, fmt.Errorf("%w: feature_set id %s as of %s", store.ErrNotFound, featureSetID, asOf)
	}

	features, err := q.store.Features().ListCurrentByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list features by feature_set: %w", err)
	}
	features = filterFeaturesExistedAsOf(features, asOf)

	requirements, err := q.store.Slices().ListRequirementsByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by feature_set: %w", err)
	}
	requirements, err = q.requirementsAsOf(ctx, requirements, asOf)
	if err != nil {
		return Document{}, err
	}

	decisions, err := q.store.Decisions().ListCurrentByFeatureSet(ctx, featureSetID)
	if err != nil {
		return Document{}, fmt.Errorf("list decisions by feature_set: %w", err)
	}
	decisions, err = q.decisionsAsOf(ctx, decisions, asOf)
	if err != nil {
		return Document{}, err
	}

	return Document{
		SchemaVersion: SchemaVersion,
		FeatureSets:   []FeatureSetEntity{toFeatureSetEntity(featureSet)},
		Features:      toFeatureEntities(features),
		Requirements:  toRequirementEntities(requirements),
		Decisions:     toDecisionEntities(decisions),
	}, nil
}

// GetFeatureSliceAsOf is GetFeatureSlice assembled as of asOf.
func (q *Querier) GetFeatureSliceAsOf(ctx context.Context, featureID uuid.UUID, asOf time.Time) (Document, error) {
	feature, err := q.store.Features().GetCurrentByID(ctx, featureID)
	if err != nil {
		return Document{}, fmt.Errorf("get feature: %w", err)
	}
	if !entityExistedAsOf(feature.ValidFrom, asOf) {
		return Document{}, fmt.Errorf("%w: feature id %s as of %s", store.ErrNotFound, featureID, asOf)
	}

	requirements, err := q.store.Requirements().ListCurrentByFeature(ctx, featureID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by feature: %w", err)
	}
	requirements, err = q.requirementsAsOf(ctx, requirements, asOf)
	if err != nil {
		return Document{}, err
	}

	return Document{
		SchemaVersion: SchemaVersion,
		Features:      []FeatureEntity{toFeatureEntity(feature)},
		Requirements:  toRequirementEntities(requirements),
	}, nil
}

// GetRequirementSliceAsOf is GetRequirementSlice assembled as of asOf --
// FR7 read through krill/store's HistoryStore (issue #2493) instead of
// the current row.
func (q *Querier) GetRequirementSliceAsOf(ctx context.Context, requirementID uuid.UUID, asOf time.Time) (Document, error) {
	requirement, err := q.store.History().GetRequirementAsOf(ctx, requirementID, asOf)
	if err != nil {
		return Document{}, fmt.Errorf("get requirement as of %s: %w", asOf, err)
	}

	return Document{
		SchemaVersion: SchemaVersion,
		Requirements:  []RequirementEntity{toRequirementEntity(requirement)},
	}, nil
}

// GetProductSliceAsOf is GetProductSlice assembled as of asOf.
func (q *Querier) GetProductSliceAsOf(ctx context.Context, productID uuid.UUID, asOf time.Time) (Document, error) {
	product, err := q.store.Products().GetCurrentByID(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("get product: %w", err)
	}
	if !entityExistedAsOf(product.ValidFrom, asOf) {
		return Document{}, fmt.Errorf("%w: product id %s as of %s", store.ErrNotFound, productID, asOf)
	}

	featureSets, err := q.store.FeatureSets().ListCurrentByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list feature_sets by product: %w", err)
	}
	featureSets = filterFeatureSetsExistedAsOf(featureSets, asOf)

	features, err := q.store.Slices().ListFeaturesByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list features by product: %w", err)
	}
	features = filterFeaturesExistedAsOf(features, asOf)

	requirements, err := q.store.Slices().ListRequirementsByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list requirements by product: %w", err)
	}
	requirements, err = q.requirementsAsOf(ctx, requirements, asOf)
	if err != nil {
		return Document{}, err
	}

	decisions, err := q.store.Slices().ListDecisionsByProduct(ctx, productID)
	if err != nil {
		return Document{}, fmt.Errorf("list decisions by product: %w", err)
	}
	decisions, err = q.decisionsAsOf(ctx, decisions, asOf)
	if err != nil {
		return Document{}, err
	}

	productEntity := toProductEntity(product)
	return Document{
		SchemaVersion: SchemaVersion,
		Product:       &productEntity,
		FeatureSets:   toFeatureSetEntities(featureSets),
		Features:      toFeatureEntities(features),
		Requirements:  toRequirementEntities(requirements),
		Decisions:     toDecisionEntities(decisions),
	}, nil
}

// requirementsAsOf re-reads every id in current through
// krill/store's HistoryStore at asOf, dropping any id that had not been
// created yet (HistoryStore's ErrNotFound) rather than keeping its
// current-row contents -- a requirement listed by ListRequirementsBy* is
// always today's set, which is a superset of what existed at a past asOf.
func (q *Querier) requirementsAsOf(ctx context.Context, current []store.Requirement, asOf time.Time) ([]store.Requirement, error) {
	result := make([]store.Requirement, 0, len(current))
	for _, r := range current {
		historical, err := q.store.History().GetRequirementAsOf(ctx, r.ID, asOf)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get requirement %s as of %s: %w", r.ID, asOf, err)
		}
		result = append(result, historical)
	}
	return result, nil
}

// decisionsAsOf mirrors requirementsAsOf for LoadBearingDecision.
func (q *Querier) decisionsAsOf(ctx context.Context, current []store.LoadBearingDecision, asOf time.Time) ([]store.LoadBearingDecision, error) {
	result := make([]store.LoadBearingDecision, 0, len(current))
	for _, d := range current {
		historical, err := q.store.History().GetLoadBearingDecisionAsOf(ctx, d.ID, asOf)
		if errors.Is(err, store.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get load_bearing_decision %s as of %s: %w", d.ID, asOf, err)
		}
		result = append(result, historical)
	}
	return result, nil
}

// filterFeatureSetsExistedAsOf drops any FeatureSet not yet created by
// asOf -- see entityExistedAsOf.
func filterFeatureSetsExistedAsOf(featureSets []store.FeatureSet, asOf time.Time) []store.FeatureSet {
	out := make([]store.FeatureSet, 0, len(featureSets))
	for _, fs := range featureSets {
		if entityExistedAsOf(fs.ValidFrom, asOf) {
			out = append(out, fs)
		}
	}
	return out
}

// filterFeaturesExistedAsOf drops any Feature not yet created by asOf --
// see entityExistedAsOf.
func filterFeaturesExistedAsOf(features []store.Feature, asOf time.Time) []store.Feature {
	out := make([]store.Feature, 0, len(features))
	for _, f := range features {
		if entityExistedAsOf(f.ValidFrom, asOf) {
			out = append(out, f)
		}
	}
	return out
}

// -- store.* -> *Entity conversions --------------------------------------

func toProductEntity(p store.Product) ProductEntity {
	return ProductEntity{
		EntityRef: EntityRef{ID: p.ID, RevisionID: p.RevisionID},
		Name:      p.Name,
		Vision:    p.Vision,
		Position:  p.Position,
	}
}

func toFeatureSetEntity(fs store.FeatureSet) FeatureSetEntity {
	return FeatureSetEntity{
		EntityRef:   EntityRef{ID: fs.ID, RevisionID: fs.RevisionID},
		ProductID:   fs.ProductID,
		Name:        fs.Name,
		Description: fs.Description,
		Position:    fs.Position,
	}
}

func toFeatureSetEntities(featureSets []store.FeatureSet) []FeatureSetEntity {
	entities := make([]FeatureSetEntity, len(featureSets))
	for i, fs := range featureSets {
		entities[i] = toFeatureSetEntity(fs)
	}
	return entities
}

func toFeatureEntity(f store.Feature) FeatureEntity {
	return FeatureEntity{
		EntityRef:    EntityRef{ID: f.ID, RevisionID: f.RevisionID},
		FeatureSetID: f.FeatureSetID,
		Name:         f.Name,
		Description:  f.Description,
		Position:     f.Position,
	}
}

func toFeatureEntities(features []store.Feature) []FeatureEntity {
	entities := make([]FeatureEntity, len(features))
	for i, f := range features {
		entities[i] = toFeatureEntity(f)
	}
	return entities
}

func toRequirementEntity(r store.Requirement) RequirementEntity {
	return RequirementEntity{
		EntityRef: EntityRef{ID: r.ID, RevisionID: r.RevisionID},
		FeatureID: r.FeatureID,
		Kind:      string(r.Kind),
		Name:      r.Name,
		Body:      r.Body,
		Position:  r.Position,
	}
}

func toRequirementEntities(requirements []store.Requirement) []RequirementEntity {
	entities := make([]RequirementEntity, len(requirements))
	for i, r := range requirements {
		entities[i] = toRequirementEntity(r)
	}
	return entities
}

func toDecisionEntity(d store.LoadBearingDecision) DecisionEntity {
	return DecisionEntity{
		EntityRef:    EntityRef{ID: d.ID, RevisionID: d.RevisionID},
		FeatureSetID: d.FeatureSetID,
		Name:         d.Name,
		Body:         d.Body,
		Position:     d.Position,
	}
}

func toDecisionEntities(decisions []store.LoadBearingDecision) []DecisionEntity {
	entities := make([]DecisionEntity, len(decisions))
	for i, d := range decisions {
		entities[i] = toDecisionEntity(d)
	}
	return entities
}
