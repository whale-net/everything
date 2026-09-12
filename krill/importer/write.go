package importer

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// loadBearingFeatureSetName is the synthetic FeatureSet the importer
// creates to hold every LoadBearingDecision it writes. PRODUCT.md's
// Load-bearing decisions are product-level, not scoped to any one
// capability bucket -- but migration 002 requires every
// LoadBearingDecision to have a FeatureSet parent (C2: attached to the
// area it constrains). Rather than guess which bucket an LB entry belongs
// to (it is frequently about more than one), the importer gives every
// imported product exactly one FeatureSet for this purpose, named
// identically every time so a second import resolves the same row rather
// than creating a sibling.
const loadBearingFeatureSetName = "Load-bearing decisions"

// write persists parsed into st under scopeID, producing the entity-id
// report FR16 requires. It is the only place this package calls a Create
// or Get-or-create method on krill/store -- Parse itself never does.
func write(ctx context.Context, st *store.Store, scopeID uuid.UUID, parsed *ParsedProduct) (*Report, error) {
	product, err := st.Products().Create(ctx, scopeID, parsed.Name, parsed.Vision)
	if err != nil {
		return nil, fmt.Errorf("create product %q: %w", parsed.Name, err)
	}

	report := &Report{ProductID: product.ID, ProductName: product.Name}

	for _, p := range parsed.Personas {
		desc := p.Description
		persona, err := st.Personas().Create(ctx, scopeID, product.ID, p.Name, &desc)
		if err != nil {
			return nil, fmt.Errorf("create persona %q: %w", p.Name, err)
		}
		report.add("persona", p.Name, p.Name, persona.ID)
	}

	for _, ng := range parsed.NonGoals {
		body := ng.Body
		kind := store.NonGoalKindPermanent
		if ng.Kind == NonGoalDeferred {
			kind = store.NonGoalKindDeferred
		}
		nonGoal, err := st.NonGoals().Create(ctx, scopeID, product.ID, kind, ng.Name, &body)
		if err != nil {
			return nil, fmt.Errorf("create non-goal %q: %w", ng.Name, err)
		}
		report.add("non_goal", ng.Name, ng.Name, nonGoal.ID)
	}

	decisionIDs := map[string]uuid.UUID{}
	if len(parsed.Decisions) > 0 {
		lbFeatureSet, err := st.FeatureSets().Create(ctx, scopeID, product.ID, loadBearingFeatureSetName, nil)
		if err != nil {
			return nil, fmt.Errorf("create feature_set %q: %w", loadBearingFeatureSetName, err)
		}
		for _, d := range parsed.Decisions {
			body := d.Body
			decision, err := st.Decisions().Create(ctx, scopeID, lbFeatureSet.ID, d.Name, &body)
			if err != nil {
				return nil, fmt.Errorf("create load_bearing_decision %s: %w", d.ID, err)
			}
			decisionIDs[d.ID] = decision.ID
			report.add("decision", d.ID, d.Name, decision.ID)
		}
	}

	featureIDs := map[string]uuid.UUID{}
	for _, bucket := range parsed.Buckets {
		if len(bucket.Capabilities) == 0 {
			continue
		}
		featureSet, err := st.FeatureSets().Create(ctx, scopeID, product.ID, bucket.Name, nil)
		if err != nil {
			return nil, fmt.Errorf("create feature_set %q: %w", bucket.Name, err)
		}
		for _, c := range bucket.Capabilities {
			desc := c.Description
			feature, err := st.Features().Create(ctx, scopeID, featureSet.ID, desc, &desc)
			if err != nil {
				return nil, fmt.Errorf("create feature %s: %w", c.ID, err)
			}
			featureIDs[c.ID] = feature.ID
			report.add("capability", c.ID, c.Description, feature.ID)
		}
	}

	for _, m := range parsed.Milestones {
		ref, err := st.Milestones().GetOrCreateRef(ctx, scopeID, product.ID, m.ID)
		if err != nil {
			return nil, fmt.Errorf("get or create milestone_ref %s: %w", m.ID, err)
		}
		report.add("milestone", m.ID, m.Title, ref.ID)

		for _, capID := range m.Delivers {
			featureID, ok := featureIDs[capID]
			if !ok {
				return nil, fmt.Errorf("milestone %s Delivers: cites %s, which the capability map never defines", m.ID, capID)
			}
			if err := st.Milestones().AddAssociation(ctx, scopeID, featureID, ref.ID); err != nil {
				return nil, fmt.Errorf("associate %s with milestone %s: %w", capID, m.ID, err)
			}
		}
		for _, lbID := range m.MustNotForeclose {
			decisionID, ok := decisionIDs[lbID]
			if !ok {
				return nil, fmt.Errorf("milestone %s Must not foreclose: cites %s, which the Load-bearing decisions section never defines", m.ID, lbID)
			}
			if err := st.Milestones().AddAssociation(ctx, scopeID, decisionID, ref.ID); err != nil {
				return nil, fmt.Errorf("associate %s with milestone %s: %w", lbID, m.ID, err)
			}
		}
	}

	return report, nil
}
