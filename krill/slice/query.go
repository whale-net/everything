package slice

import (
	"context"
	"fmt"

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
