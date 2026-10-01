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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
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

	// The placeholder has to tell a reader where current-state content
	// actually lives, and must not read as "krill dropped it" or promise
	// that a future entity type will fill the file.
	assert.Contains(t, files.CurrentStateMD, "This section is intentionally not rendered.")
	assert.Contains(t, files.CurrentStateMD, "ARCHITECTURE.md",
		"a reader must be told where the survey is, not merely that it is absent")
	assert.Contains(t, files.CurrentStateMD, "remains in this file's git history")
	assert.NotContains(t, files.CurrentStateMD, "No entity in krill's model backs this section",
		"that wording reads as a gap in krill's model and invites a new entity type")
	assert.NotContains(t, files.CurrentStateMD, "will be filled",
		"the placeholder must not promise a future entity type")

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
	beforeFiles, err := render.Render(ctx, before, scopeID, productID, render.WithDetail())
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
	afterFiles, err := render.Render(ctx, after, scopeID, productID, render.WithDetail())
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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1 — An Agent can do the thing")
	assert.Contains(t, files.RoadmapMD, "Deliberately deferred: design sessions (→ M2)")
	assert.Contains(t, files.RoadmapMD, "FR budget: 12")
}

// TestRender_ShipsAlongsideRendersPerMilestoneAndOmittedWhenEmpty: a
// milestone with Ships alongside rows gets a labelled line in order; a
// milestone with none gets no such line.
func TestRender_ShipsAlongsideRendersPerMilestoneAndOmittedWhenEmpty(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()
	product := &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"}

	withID, withoutID := uuid.New(), uuid.New()
	src := &fakeSource{
		Doc: slice.Document{SchemaVersion: slice.SchemaVersion, Product: product},
		MilestoneRefs: []store.MilestoneRef{
			{ID: withID, Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: withoutID, Name: "M2", Kind: store.MilestoneKindMilestone},
		},
		ShipsAlongside: map[uuid.UUID][]store.MilestoneShipsAlongside{
			withID: {{Body: "migration runbook"}, {Body: "dashboard tweak"}},
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "- Ships alongside: migration runbook; dashboard tweak\n")
	assert.Equal(t, 1, strings.Count(files.RoadmapMD, "Ships alongside:"), "a milestone with no rows must render no block")
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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
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
	f2 := newFeature("F2 (parked)", 2)
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

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
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

	_, err := render.Render(ctx, src, uuid.New(), productID, render.WithDetail())
	require.Error(t, err)
	assert.Contains(t, err.Error(), productID.String())
}

// TestRender_RoadmapCarriesCurrentStatus is the status signal the roadmap
// used to be blind to: a rendered M1..M3 must each say where they are, so
// a reader can tell shipped from in-progress from not-started without
// opening krill.
func TestRender_RoadmapCarriesCurrentStatus(t *testing.T) {
	ctx := context.Background()
	productID := uuid.New()
	scopeID := uuid.New()

	m1, m2, m3 := uuid.New(), uuid.New(), uuid.New()
	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"},
		},
		MilestoneRefs: []store.MilestoneRef{
			{ID: m1, Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: m2, Name: "M2", Kind: store.MilestoneKindMilestone},
			{ID: m3, Name: "M3", Kind: store.MilestoneKindMilestone},
		},
		Statuses: map[uuid.UUID]store.MilestoneStatus{
			m1: store.MilestoneStatusShipped,
			m2: store.MilestoneStatusInProgress,
			m3: store.MilestoneStatusNotStarted,
		},
	}

	files, err := render.Render(ctx, src, scopeID, productID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "### M1\n\n- Status: shipped\n")
	assert.Contains(t, files.RoadmapMD, "### M2\n\n- Status: in progress\n")
	assert.Contains(t, files.RoadmapMD, "### M3\n\n- Status: not started\n")
	assert.Contains(t, files.RoadmapMD, "**current** delivery status",
		"the rendered status must be unambiguous that it is the current one, not history")
}

// A milestone with no recorded transition has no current status at all.
// CurrentStatuses already answers "not started" for it (FR8 derives it
// from the absence of history); a Source that omits the id entirely must
// land on the same honest answer rather than an empty `Status:` line.
func TestRender_MilestoneWithNoTransitionsRendersHonestStatus(t *testing.T) {
	for _, tc := range []struct {
		name     string
		statuses func(milestoneID uuid.UUID) map[uuid.UUID]store.MilestoneStatus
	}{
		{"store reports not started explicitly", func(id uuid.UUID) map[uuid.UUID]store.MilestoneStatus {
			return map[uuid.UUID]store.MilestoneStatus{id: store.MilestoneStatusNotStarted}
		}},
		{"source omits the id entirely", func(uuid.UUID) map[uuid.UUID]store.MilestoneStatus { return map[uuid.UUID]store.MilestoneStatus{} }},
		{"source returns a nil map", func(uuid.UUID) map[uuid.UUID]store.MilestoneStatus { return nil }},
		{"source returns an empty-string value", func(id uuid.UUID) map[uuid.UUID]store.MilestoneStatus {
			return map[uuid.UUID]store.MilestoneStatus{id: ""}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			milestoneID := uuid.New()
			src := &fakeSource{
				Doc: slice.Document{
					SchemaVersion: slice.SchemaVersion,
					Product:       &slice.ProductEntity{EntityRef: newRef(), Name: "Widgets", Vision: "v"},
				},
				MilestoneRefs: []store.MilestoneRef{{ID: milestoneID, Name: "M1", Kind: store.MilestoneKindMilestone}},
				Statuses:      tc.statuses(milestoneID),
			}

			files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
			require.NoError(t, err)

			assert.Contains(t, files.RoadmapMD, "Status: not started")
			assert.NotContains(t, files.RoadmapMD, "Status: \n", "an empty status line would tell a reader nothing")
		})
	}
}

// A whole-product roadmap is one pass over every milestone, so statuses
// must be read in a single batched call -- a per-milestone round trip is
// the wrong shape (FR11).
func TestRender_RoadmapReadsStatusesInOneBatch(t *testing.T) {
	ids := []uuid.UUID{uuid.New(), uuid.New(), uuid.New()}
	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: newRef(), Name: "Widgets", Vision: "v"},
		},
		MilestoneRefs: []store.MilestoneRef{
			{ID: ids[0], Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: ids[1], Name: "M2", Kind: store.MilestoneKindMilestone},
			{ID: ids[2], Name: "M3", Kind: store.MilestoneKindMilestone},
		},
	}

	_, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	require.Len(t, src.StatusCalls, 1, "statuses must be read in one batch, not one call per milestone")
	assert.ElementsMatch(t, ids, src.StatusCalls[0])
}

// A milepebble or backlog bucket never reaches the roadmap, so its id
// must not be dragged into the status read either.
func TestRender_RoadmapStatusBatchExcludesNonMilestoneRefs(t *testing.T) {
	real, cut := uuid.New(), uuid.New()
	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: newRef(), Name: "Widgets", Vision: "v"},
		},
		MilestoneRefs: []store.MilestoneRef{
			{ID: real, Name: "M1", Kind: store.MilestoneKindMilestone},
			{ID: cut, Name: "cut 1", Kind: store.MilestoneKindMilepebble},
		},
	}

	_, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	require.Len(t, src.StatusCalls, 1)
	assert.Equal(t, []uuid.UUID{real}, src.StatusCalls[0])
}

// mappingNoteFixture is the known-positive shape behind ce860827's guard
// test: the exact sentence whagent_net's three LoadBearingDecisions carry
// ("See the mapping note on this Product") plus the renumbering body that
// only exists as a note. It is the real defect, not a hypothetical.
const mappingNoteBody = "CAPABILITY RENUMBERING. krill C5 = brief C12 (/wpoll ad-hoc polls) Now."

func mappingNoteSrc() *fakeSource {
	productID := uuid.New()
	return &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "whagent_net", Vision: "v"},
			Decisions: []slice.DecisionEntity{{
				EntityRef:     newRef(),
				Name:          "Capability numbering is krill's, not the brief's",
				DisplayNumber: 1,
				Body:          strPtr("The C-numbers changed at onboarding. See the mapping note on this Product."),
			}},
		},
		Notes: []store.Note{{
			ID:            uuid.MustParse("bd9197eb-2b84-469a-b112-bfbd134fc67e"),
			Kind:          store.NoteKindComment,
			CurrentStatus: store.NoteLifecycleStatusNoted,
			Body:          mappingNoteBody,
		}},
	}
}

func TestRender_ProductNotesRenderVerbatim(t *testing.T) {
	src := mappingNoteSrc()
	productID := src.Doc.Product.ID

	files, err := render.Render(context.Background(), src, uuid.New(), productID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "## Notes")
	assert.Contains(t, files.ProductMD, "bd9197eb-2b84-469a-b112-bfbd134fc67e", "a note's own id must be citable")
	assert.Contains(t, files.ProductMD, "comment", "the note's kind must be visible")
	assert.Contains(t, files.ProductMD, "status: noted", "the note's lifecycle status must be visible")
	assert.Contains(t, files.ProductMD, mappingNoteBody,
		"a body must render verbatim -- a renumbering mapping is useless as a summary")
}

// A note whose body is a whole forensic record (paragraphs, a table, a
// multi-line mapping) must survive intact, not be flattened to a line.
func TestRender_ProductNoteMultilineBodyRendersIntact(t *testing.T) {
	body := "Line one.\n\n  krill C1  = brief C1   (/wai)   Now\n  krill C5  = brief C12  (/wpoll) Now\n\nLine four."
	src := mappingNoteSrc()
	src.Notes[0].Body = body

	files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, body)
}

// A note in a terminal or transitional lifecycle state must not read as
// current fact. `closed` in particular means the note is history.
func TestRender_ProductNoteLifecycleStatusIsRendered(t *testing.T) {
	for _, status := range []store.NoteLifecycleStatus{
		store.NoteLifecycleStatusNoted,
		store.NoteLifecycleStatusCarriedOver,
		store.NoteLifecycleStatusDeferred,
		store.NoteLifecycleStatusClosed,
	} {
		t.Run(string(status), func(t *testing.T) {
			src := mappingNoteSrc()
			src.Notes[0].CurrentStatus = status

			files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
			require.NoError(t, err)

			assert.Contains(t, files.ProductMD, "status: "+string(status))
		})
	}
}

func TestRender_ProductWithNoNotesRendersEmptySection(t *testing.T) {
	src := mappingNoteSrc()
	src.Notes = nil

	files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "## Notes")
	assert.Contains(t, files.ProductMD, "_No notes are recorded against this Product in krill._",
		"an empty section must say so rather than render a bare heading")
}

// The whole point of the section: the "See the mapping note on this
// Product" sentences in the rendered LB bodies must lead somewhere that
// exists in the same document.
func TestRender_MappingNotePointerResolves(t *testing.T) {
	src := mappingNoteSrc()

	files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
	require.NoError(t, err)

	pointer := "See the mapping note on this Product"
	require.Contains(t, files.ProductMD, pointer, "the fixture must carry the real pointer sentence")
	notesAt := strings.Index(files.ProductMD, "## Notes")
	pointerAt := strings.Index(files.ProductMD, pointer)
	require.NotEqual(t, -1, notesAt)
	assert.Greater(t, notesAt, pointerAt, "the pointer must not point forward past the end of the document")
	assert.Contains(t, files.ProductMD[notesAt:], "CAPABILITY RENUMBERING",
		"the section the pointer names must actually contain the mapping")
}

// reqDoc builds a whole-product slice with one FeatureSet, the given
// Features, and the given Requirements -- the shape renderCapabilityMapMD
// walks.
func reqDoc(features []slice.FeatureEntity, reqs []slice.RequirementEntity) slice.Document {
	fsID := uuid.New()
	for i := range features {
		features[i].FeatureSetID = fsID
	}
	return slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       &slice.ProductEntity{EntityRef: newRef(), Name: "Widgets", Vision: "v"},
		FeatureSets:   []slice.FeatureSetEntity{{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core", Position: 1}},
		Features:      features,
		Requirements:  reqs,
	}
}

func req(featureID uuid.UUID, kind, name string, body *string) slice.RequirementEntity {
	return slice.RequirementEntity{EntityRef: newRef(), FeatureID: featureID, Kind: kind, Name: name, Body: body}
}

// The bodies are the deliverable. krill's richest forensic content --
// prohibitions, refuted-hypothesis records, mandatory-ordering
// constraints -- lives only in a Requirement body, so an elided body
// reproduces the exact problem this rendering exists to fix.
func TestRender_RequirementBodiesRenderInFull(t *testing.T) {
	f1 := newFeature("F1", 1)
	body := "A requirement body with:\n\n- a prohibition: never do X\n- a refuted hypothesis: we tried Y, it does not work\n- a mandatory ordering: Z must precede W\n"
	src := &fakeSource{Doc: reqDoc(
		[]slice.FeatureEntity{f1},
		[]slice.RequirementEntity{req(f1.ID, "FR", "Handle the thing", &body)},
	)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "**FR1** — Handle the thing")
	assert.Contains(t, files.CapabilityMapMD, body, "the body must render in full, not summarized")
	assert.Contains(t, files.CapabilityMapMD, "a refuted hypothesis: we tried Y, it does not work")
}

// FRs and NFRs are counted separately -- an NFR3 must not be FR3, and a
// product's first NFR must be NFR1 however many FRs precede it.
func TestRender_RequirementsNumberPerKind(t *testing.T) {
	f1, f2 := newFeature("F1", 1), newFeature("F2", 2)
	src := &fakeSource{Doc: reqDoc(
		[]slice.FeatureEntity{f1, f2},
		[]slice.RequirementEntity{
			req(f1.ID, "FR", "first", nil),
			req(f1.ID, "NFR", "a non-functional one", nil),
			req(f1.ID, "FR", "second", nil),
			req(f2.ID, "FR", "third", nil),
			req(f2.ID, "NFR", "another non-functional one", nil),
		},
	)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	cap := files.CapabilityMapMD
	assert.Contains(t, cap, "**FR1** — first")
	assert.Contains(t, cap, "**FR2** — second")
	assert.Contains(t, cap, "**FR3** — third")
	assert.Contains(t, cap, "**NFR1** — a non-functional one")
	assert.Contains(t, cap, "**NFR2** — another non-functional one")
	assert.NotContains(t, cap, "**FR4**")
}

// The rendered number has to be reconstructible by a reader. krill stores
// no Requirement display number, so the document has to say where this one
// comes from -- and carry the id that resolves it exactly.
func TestRender_RequirementNumberingIsDocumentedAndResolvable(t *testing.T) {
	f1 := newFeature("F1", 1)
	r := req(f1.ID, "FR", "Handle the thing", strPtr("body"))
	src := &fakeSource{Doc: reqDoc([]slice.FeatureEntity{f1}, []slice.RequirementEntity{r})}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "krill stores no display number for a Requirement",
		"the file must say where FRn comes from, or the citation does not resolve")
	assert.Contains(t, files.CapabilityMapMD, "counting down this file")
	assert.Contains(t, files.CapabilityMapMD, r.ID.String(), "a citation must resolve exactly, not just by position")
}

// A Requirement with no body still names itself. Dropping it would hide a
// requirement krill holds; a broken heading would look like a render bug.
func TestRender_RequirementWithNoBodyRendersCleanly(t *testing.T) {
	f1 := newFeature("F1", 1)
	src := &fakeSource{Doc: reqDoc(
		[]slice.FeatureEntity{f1},
		[]slice.RequirementEntity{
			req(f1.ID, "FR", "nil body", nil),
			req(f1.ID, "FR", "whitespace body", strPtr("   \n\t  ")),
		},
	)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "**FR1** — nil body")
	assert.Contains(t, files.CapabilityMapMD, "**FR2** — whitespace body")
	assert.Equal(t, 2, strings.Count(files.CapabilityMapMD, "_No body recorded._"),
		"a bodiless requirement must say so rather than look truncated")
}

// A Requirement imported or hand-created with its own "FR7 — " prefix in
// Name must not render that stale prefix next to the recomputed citation.
func TestRender_RequirementNamePrefixStripped(t *testing.T) {
	f1 := newFeature("F1", 1)
	src := &fakeSource{Doc: reqDoc(
		[]slice.FeatureEntity{f1},
		[]slice.RequirementEntity{req(f1.ID, "FR", "FR7 — Handle the thing", strPtr("body"))},
	)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "**FR1** — Handle the thing")
	assert.NotContains(t, files.CapabilityMapMD, "FR7 — Handle the thing",
		"the stored prefix is not the citation; the render-time number is")
}

// A product with no requirements keeps the compact one-line-per-capability
// form and does not carry a numbering preamble it has nothing to number.
func TestRender_ProductWithNoRequirementsKeepsBulletForm(t *testing.T) {
	src := &fakeSource{Doc: reqDoc([]slice.FeatureEntity{newFeature("F1", 1)}, nil)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "- **C1** — F1")
	assert.NotContains(t, files.CapabilityMapMD, "krill stores no display number for a Requirement")
}

// By default the capability map is headlines only: a count per capability,
// no requirement bodies, and a pointer at the MCP read path.
func TestRender_CapabilityMapDefaultsToHeadlines(t *testing.T) {
	f1 := newFeature("F1", 1)
	src := &fakeSource{Doc: reqDoc(
		[]slice.FeatureEntity{f1},
		[]slice.RequirementEntity{
			req(f1.ID, "FR", "first", strPtr("SECRET-BODY")),
			req(f1.ID, "NFR", "second", strPtr("other body")),
			req(f1.ID, "FR", "third", nil),
		},
	)}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New())
	require.NoError(t, err)

	assert.Contains(t, files.CapabilityMapMD, "- **C1** — F1 (2 FR, 1 NFR)")
	assert.Contains(t, files.CapabilityMapMD, "get_feature_slice")
	assert.NotContains(t, files.CapabilityMapMD, "SECRET-BODY")
	assert.NotContains(t, files.CapabilityMapMD, "**FR1**")
}

// By default PRODUCT.md carries headlines only: no persona/decision/note bodies.
func TestRender_ProductMDDefaultsToHeadlines(t *testing.T) {
	src := &fakeSource{
		Doc: reqDoc(nil, nil),
		Notes: []store.Note{{
			ID:            uuid.New(),
			Kind:          store.NoteKind("scope-note"),
			CurrentStatus: store.NoteLifecycleStatus("noted"),
			Body:          "First sentence. SECOND-SENTENCE-BODY\n\nmore",
		}},
	}
	src.Doc.Decisions = []slice.DecisionEntity{{EntityRef: newRef(), Name: "LB1 — Pick X", DisplayNumber: 1, Body: strPtr("DECISION-BODY")}}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "- **LB1** — Pick X")
	assert.Contains(t, files.ProductMD, "First sentence.")
	assert.NotContains(t, files.ProductMD, "DECISION-BODY")
	assert.NotContains(t, files.ProductMD, "SECOND-SENTENCE-BODY")
}

// Headings inside a note body must nest under the document's own outline.
func TestRender_NoteBodyHeadingsAreDemoted(t *testing.T) {
	src := &fakeSource{
		Doc: reqDoc(nil, nil),
		Notes: []store.Note{{
			ID:            uuid.New(),
			Kind:          store.NoteKind("scope-note"),
			CurrentStatus: store.NoteLifecycleStatus("noted"),
			Body:          "# Top\n\ntext\n\n```\n# not a heading\n```\n\n## Sub",
		}},
	}

	files, err := render.Render(context.Background(), src, uuid.New(), uuid.New(), render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.ProductMD, "\n#### Top\n")
	assert.Contains(t, files.ProductMD, "\n##### Sub")
	assert.Contains(t, files.ProductMD, "\n# not a heading\n", "fenced content is untouched")
	assert.NotContains(t, files.ProductMD, "\n# Top\n")
}
