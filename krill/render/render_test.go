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

func newFeature(name string, displayNumber int) slice.FeatureEntity {
	return slice.FeatureEntity{EntityRef: newRef(), Name: name, DisplayNumber: displayNumber}
}

func newDecision(name string, displayNumber int) slice.DecisionEntity {
	return slice.DecisionEntity{EntityRef: newRef(), Name: name, DisplayNumber: displayNumber}
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
				newDecision("LB1 — Keep it simple", 1),
				newDecision("Ship fast", 2),
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

// TestRender_DisplayNumbersStableAcrossReorder is issue #2969's core
// assertion, reversing the old (buggy) FR14 behavior this test used to
// name: inserting a new sibling out of order, or reordering existing
// siblings, must NOT change any existing entity's rendered citation --
// DisplayNumber is read verbatim off each entity (stored at creation time),
// never recomputed from the entity's position in the slice Source returns.
// X, Y, Z keep C1/C2/C3 even after the slice order is scrambled and a
// fourth sibling W is appended; W gets its own stored number (C4), not one
// derived from where it landed in the list.
func TestRender_DisplayNumbersStableAcrossReorder(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}
	fsID := uuid.New()
	featureSet := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}

	x := newFeature("X", 1)
	x.FeatureSetID = fsID
	y := newFeature("Y", 2)
	y.FeatureSetID = fsID
	z := newFeature("Z", 3)
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

	// Scramble X/Y's order (mirroring a real Position swap in krill/store)
	// and append a new sibling W carrying its own stored DisplayNumber --
	// krill's own capability map appends entries out of position on purpose
	// (C25-C28), exactly this shape.
	w := newFeature("W", 4)
	w.FeatureSetID = fsID

	after := &fakeSource{Doc: slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       product,
		FeatureSets:   []slice.FeatureSetEntity{featureSet},
		Features:      []slice.FeatureEntity{y, x, w, z},
	}}
	afterFiles, err := render.Render(ctx, after, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C1** — X\n", "X keeps its stored citation despite the reorder")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C2** — Y\n", "Y keeps its stored citation despite the reorder")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C3** — Z\n", "Z keeps its stored citation despite W's append")
	assert.Contains(t, afterFiles.CapabilityMapMD, "- **C4** — W\n", "W renders its own stored number, never one derived from its position in the list")
}

// TestRender_FeatureNamePrefixStripped mirrors the LB1-prefix case in
// TestRender_ProducesFourFileLayout: a Feature whose stored Name still
// carries its own baked-in "Cn — " prefix (e.g. imported, or hand-created
// before #2961's create_feature existed) must not have that stale prefix
// doubled up with the freshly computed DisplayNumber at render time.
func TestRender_FeatureNamePrefixStripped(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	fsID := uuid.New()
	f1 := newFeature("C1 — Do the thing", 1)
	f1.FeatureSetID = fsID
	f2 := newFeature("Do another thing", 2)
	f2.FeatureSetID = fsID

	src := &fakeSource{Doc: slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"},
		FeatureSets:   []slice.FeatureSetEntity{{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}},
		Features:      []slice.FeatureEntity{f1, f2},
	}}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "- **C1** — Do the thing\n")
	assert.NotContains(t, files.CapabilityMapMD, "C1 — C1")
	assert.Contains(t, files.CapabilityMapMD, "- **C2** — Do another thing\n")
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

	f1 := newFeature("F1", 1)
	f1.FeatureSetID = fsID
	f2 := newFeature("F2", 2)
	f2.FeatureSetID = fsID

	lb1 := newDecision("Keep it simple", 1)
	lb1.FeatureSetID = fsID
	lb2 := newDecision("Ship fast", 2)
	lb2.FeatureSetID = fsID
	lb3 := newDecision("Own the data model", 3)
	lb3.FeatureSetID = fsID
	lb4 := newDecision("No second writable source", 4)
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
		MilestoneRefs: []store.MilestoneRef{{ID: milestoneID, Name: "M1", Kind: store.MilestoneKindMilestone}},
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

// TestRender_OutcomeFRBudgetAndDeferralsRenderFromMilestoneRows is issue
// #2970's fix: a milestone's outcome sentence, FR budget, and deliberately
// deferred items are already stored on milestone_ref/milestone_deferral
// rows (migration 010) but were never copied into the rendered roadmap --
// renderRoadmapMD must emit all three, never just Delivers/Must not
// foreclose.
func TestRender_OutcomeFRBudgetAndDeferralsRenderFromMilestoneRows(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}

	milestoneID := uuid.New()
	outcome := "An Agent can do the thing"
	frBudget := 12

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       product,
		},
		MilestoneRefs: []store.MilestoneRef{{
			ID:       milestoneID,
			Name:     "M1",
			Kind:     store.MilestoneKindMilestone,
			Outcome:  &outcome,
			FRBudget: &frBudget,
		}},
		Deferrals: map[uuid.UUID][]store.MilestoneDeferral{
			milestoneID: {
				{Body: "design sessions", Destination: "M2"},
			},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1 — An Agent can do the thing")
	assert.Contains(t, files.RoadmapMD, "Deliberately deferred: design sessions (→ M2)")
	assert.Contains(t, files.RoadmapMD, "FR budget: 12")
}

// TestRender_MilepebbleRefsExcludedFromRoadmap is issue #2684's Testing
// section item 7: with a real kind="milepebble" MilestoneRef present
// alongside a kind="milestone" one (migration 011's own new row shape),
// Render must emit only the milestone into product/03-roadmap.md -- a
// milepebble is a sub-milestone container, never a roadmap entry of its
// own (see renderMilestones' own "must never silently render as a roadmap
// milestone" comment, krill/render.go).
func TestRender_MilepebbleRefsExcludedFromRoadmap(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}
	fsID := uuid.New()
	featureSet := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}

	f1 := newFeature("F1", 1)
	f1.FeatureSetID = fsID

	milestoneID := uuid.New()
	milepebbleID := uuid.New()

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       product,
			FeatureSets:   []slice.FeatureSetEntity{featureSet},
			Features:      []slice.FeatureEntity{f1},
		},
		MilestoneRefs: []store.MilestoneRef{
			{ID: milestoneID, Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: milepebbleID, Name: "cut 1", Kind: store.MilestoneKindMilepebble, ParentMilestoneID: &milestoneID},
		},
		Associations: map[uuid.UUID][]store.EntityMilestone{
			milestoneID:  {{EntityID: f1.ID}},
			milepebbleID: {{EntityID: f1.ID}},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1", "the real milestone must still render")
	assert.NotContains(t, files.RoadmapMD, "cut 1", "a milepebble's name must never appear in the roadmap")
	assert.NotContains(t, strings.ToLower(files.RoadmapMD), "milepebble", "no milepebble-shaped entry belongs in the rendered roadmap")
}

// TestRender_BacklogRefsExcludedFromRoadmap is issue #2687's Testing item
// 7: with a real kind="backlog" MilestoneRef present alongside a
// kind="milestone" one (migration 014's own new row shape), Render must
// emit only the milestone into product/03-roadmap.md -- the backlog
// bucket is a delivery-axis home for un-committed scope, never a roadmap
// entry of its own, the same posture TestRender_MilepebbleRefsExcludedFromRoadmap
// proves for a milepebble.
func TestRender_BacklogRefsExcludedFromRoadmap(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}
	fsID := uuid.New()
	featureSet := slice.FeatureSetEntity{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}

	f1 := newFeature("F1", 1)
	f1.FeatureSetID = fsID
	f2 := newFeature("F2 (backlogged)", 2)
	f2.FeatureSetID = fsID

	milestoneID := uuid.New()
	backlogID := uuid.New()

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       product,
			FeatureSets:   []slice.FeatureSetEntity{featureSet},
			Features:      []slice.FeatureEntity{f1, f2},
		},
		MilestoneRefs: []store.MilestoneRef{
			{ID: milestoneID, Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: backlogID, Name: "__backlog__", Kind: store.MilestoneKindBacklog},
		},
		Associations: map[uuid.UUID][]store.EntityMilestone{
			milestoneID: {{EntityID: f1.ID}},
			backlogID:   {{EntityID: f2.ID}},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1", "the real milestone must still render")
	assert.NotContains(t, files.RoadmapMD, "__backlog__", "the backlog bucket's own name must never appear in the roadmap")
	assert.NotContains(t, strings.ToLower(files.RoadmapMD), "backlog", "no backlog-bucket-shaped entry belongs in the rendered roadmap")
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
