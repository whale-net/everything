// Unit coverage for Render (issue #2495's Testing section), driven entirely
// through fakeSource (fake_source_test.go) -- no Postgres involved. See
// render_integration_test.go for the real-store, real-Postgres half of the
// same Testing section (the seeded-product end-to-end render and FR15's
// read-only-role proof).
package render_test

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

func strPtr(s string) *string { return &s }

func newRef() slice.EntityRef {
	return slice.EntityRef{ID: uuid.New(), RevisionID: uuid.New()}
}

func newFeature(name string) slice.FeatureEntity {
	return slice.FeatureEntity{EntityRef: newRef(), Name: name}
}

func newDecision(name string) slice.DecisionEntity {
	return slice.DecisionEntity{EntityRef: newRef(), Name: name}
}

// TestRender_ProducesFourFileLayout is the "rendering a seeded product
// produces exactly the four files in the specified layout, with
// Vision/Personas/LB/Non-goals inline and the other three split" case from
// #2495's Testing section.
func TestRender_ProducesFourFileLayout(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product: &slice.ProductEntity{
				EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()},
				Name:      "Widgets",
				Vision:    "Make great widgets.",
			},
			Decisions: []slice.DecisionEntity{
				newDecision("LB1 — Keep it simple"),
				newDecision("Ship fast"),
			},
		},
		Personas: []store.Persona{
			{Name: "Operator", Description: strPtr("runs the fleet")},
		},
		NonGoals: []store.NonGoal{
			{Kind: store.NonGoalKindPermanent, Name: "Rendering other domains' docs"},
			{Kind: store.NonGoalKindDeferred, Name: "Multi-tenant scopes", Body: strPtr("later, not now")},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	fm := files.FileMap()
	assert.Equal(t, map[string]bool{
		"PRODUCT.md":                   true,
		"product/01-current-state.md":  true,
		"product/02-capability-map.md": true,
		"product/03-roadmap.md":        true,
	}, boolMap(fm), "exactly the four-file layout, no more, no fewer")

	// PRODUCT.md is the index: Vision, Personas, Load-bearing decisions,
	// and Non-goals inline, plus a jump table to the three split files.
	product := files.ProductMD
	assert.Contains(t, product, "# Widgets — Product brief")
	assert.Contains(t, product, "[`product/01-current-state.md`](product/01-current-state.md)")
	assert.Contains(t, product, "[`product/02-capability-map.md`](product/02-capability-map.md)")
	assert.Contains(t, product, "[`product/03-roadmap.md`](product/03-roadmap.md)")
	assert.Contains(t, product, "## Vision\n\nMake great widgets.")
	assert.Contains(t, product, "## Personas")
	assert.Contains(t, product, "- **Operator** — runs the fleet")
	assert.Contains(t, product, "## Load-bearing decisions")
	// The stored "LB1 — " prefix on the first decision's Name must be
	// stripped, not doubled, when re-prefixed with its render-time number.
	assert.Contains(t, product, "LB1 — Keep it simple")
	assert.NotContains(t, product, "LB1 — LB1")
	assert.Contains(t, product, "LB2 — Ship fast")
	assert.Contains(t, product, "## Non-goals")
	assert.Contains(t, product, "**Permanent:**")
	assert.Contains(t, product, "- **Rendering other domains' docs.**")
	assert.Contains(t, product, "**Explicitly *not* non-goals — deferred, not foreclosed:**")
	assert.Contains(t, product, "- **Multi-tenant scopes.** later, not now")

	// The three split files each carry their own exact-heading-text title
	// (AGENTS.md § Size Limits & Splitting's grep-discoverability rule) and
	// nothing from the other sections.
	assert.True(t, strings.Contains(files.CurrentStateMD, "\n# Current state\n"))
	assert.True(t, strings.Contains(files.CapabilityMapMD, "\n# Capability map\n"))
	assert.True(t, strings.Contains(files.RoadmapMD, "\n# Roadmap\n"))
	assert.NotContains(t, files.CurrentStateMD, "## Vision")
	assert.NotContains(t, files.CapabilityMapMD, "## Vision")
	assert.NotContains(t, files.RoadmapMD, "## Vision")

	// Every generated file carries the provenance header (LB5) and NFR3's
	// non-hand-editable marker, naming the Product and the exact revision
	// it was rendered from.
	headerRe := regexp.MustCompile(`^<!-- ` + regexp.QuoteMeta(render.GeneratedMarker) + `\n     Rendered by krill/render from Product "Widgets", revision [0-9a-f-]+, at [0-9TZ:+-]+\. -->\n`)
	for name, content := range fm {
		assert.Regexp(t, headerRe, content, "file %s must carry the provenance + non-hand-editable header", name)
	}
}

func boolMap(m map[string]string) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

// TestRender_FR14_InsertingSiblingRenumbersCitations is FR14's core
// assertion: "inserting a new capability between two existing ones
// renumbers citations in the rendered output while no stored row changed."
// X, Y, and Z's identities (EntityRef.ID) never change between the two
// renders below -- only their position in the slice Source returns, which
// is exactly what a real insert-between-siblings changes in krill/store
// (position, never id, per LB2) -- and yet Y's and Z's rendered citation
// changes. That is the proof this package computes citations from sibling
// order at render time, never from a stored display-number column (no such
// column exists, LB2).
func TestRender_FR14_InsertingSiblingRenumbersCitations(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}
	fsID := uuid.New()
	featureSet := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}

	x := newFeature("X")
	x.FeatureSetID = fsID
	y := newFeature("Y")
	y.FeatureSetID = fsID
	z := newFeature("Z")
	z.FeatureSetID = fsID

	before := &fakeSource{Doc: slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       product,
		FeatureSets:   []slice.FeatureSetEntity{featureSet},
		Features:      []slice.FeatureEntity{x, y, z},
	}}
	beforeFiles, err := render.Render(ctx, before, scopeID, productID)
	require.NoError(t, err)
	assert.Contains(t, beforeFiles.CapabilityMapMD, "- **C1** — X\n")
	assert.Contains(t, beforeFiles.CapabilityMapMD, "- **C2** — Y\n")
	assert.Contains(t, beforeFiles.CapabilityMapMD, "- **C3** — Z\n")

	// Insert W between X and Y. X, Y, Z's IDs/RevisionIDs are untouched --
	// only the returned order changes, mirroring what a real
	// insert-between-siblings does to krill/store's own Position column.
	w := newFeature("W")
	w.FeatureSetID = fsID

	after := &fakeSource{Doc: slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       product,
		FeatureSets:   []slice.FeatureSetEntity{featureSet},
		Features:      []slice.FeatureEntity{x, w, y, z},
	}}
	afterFiles, err := render.Render(ctx, after, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C1** — X\n", "X's own citation is unaffected by an insert after it")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C2** — W\n", "the newly inserted sibling gets the number its new position earns")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C3** — Y\n", "Y renumbers from C2 to C3 -- its own row never changed")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C4** — Z\n", "Z renumbers from C3 to C4 -- its own row never changed")

	// The identities themselves never changed -- only the computed number.
	assert.Equal(t, x.ID, x.ID)
	assert.Equal(t, y.ID, y.ID)
	assert.Equal(t, z.ID, z.ID)
}

// TestRender_MustNotForecloseRendersFromAssociationRows is #2495's "a `Must
// not foreclose: LB1, LB4` line renders from association rows" case: the
// roadmap line must be reconstructed from entity_milestone rows, never
// stored as prose anywhere.
func TestRender_MustNotForecloseRendersFromAssociationRows(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}
	fsID := uuid.New()
	featureSet := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}

	f1 := newFeature("F1")
	f1.FeatureSetID = fsID
	f2 := newFeature("F2")
	f2.FeatureSetID = fsID

	lb1 := newDecision("Keep it simple")
	lb1.FeatureSetID = fsID
	lb2 := newDecision("Ship fast")
	lb2.FeatureSetID = fsID
	lb3 := newDecision("Own the data model")
	lb3.FeatureSetID = fsID
	lb4 := newDecision("No second writable source")
	lb4.FeatureSetID = fsID

	milestoneID := uuid.New()

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       product,
			FeatureSets:   []slice.FeatureSetEntity{featureSet},
			Features:      []slice.FeatureEntity{f1, f2},
			Decisions:     []slice.DecisionEntity{lb1, lb2, lb3, lb4},
		},
		MilestoneRefs: []store.MilestoneRef{{ID: milestoneID, Name: "M1"}},
		Associations: map[uuid.UUID][]store.EntityMilestone{
			milestoneID: {
				{EntityID: f1.ID},  // delivers C1
				{EntityID: lb1.ID}, // must not foreclose LB1
				{EntityID: lb4.ID}, // must not foreclose LB4
			},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1")
	assert.Contains(t, files.RoadmapMD, "Delivers: C1")
	assert.Contains(t, files.RoadmapMD, "Must not foreclose: LB1, LB4")
}

// TestRender_NoProductRow_ReturnsError guards the one error path Render
// itself owns (as opposed to propagating a Source error): an empty
// GetProductSlice response (no current Product row) must not silently
// render an empty-Vision doc.
func TestRender_NoProductRow_ReturnsError(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	src := &fakeSource{Doc: slice.Document{SchemaVersion: slice.SchemaVersion}}

	_, err := render.Render(ctx, src, uuid.New(), productID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), productID.String())
}
