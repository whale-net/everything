package render_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// manyMilestonesSource returns a fake Source with n milestones (M1..Mn),
// the last one abandoned, so roadmap tests cover counts above five and
// abandoned milestones.
func manyMilestonesSource(n int) *fakeSource {
	productID := uuid.New()
	refs := make([]store.MilestoneRef, n)
	statuses := map[uuid.UUID]store.MilestoneStatus{}
	for i := range refs {
		refs[i] = store.MilestoneRef{ID: uuid.New(), Name: fmt.Sprintf("M%d", i+1), Kind: store.MilestoneKindMilestone}
	}
	statuses[refs[n-1].ID] = store.MilestoneStatusAbandoned
	return &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"},
		},
		MilestoneRefs: refs,
		Statuses:      statuses,
	}
}

func TestRender_RoadmapHasOneBlockPerMilestoneIncludingAbandoned(t *testing.T) {
	const n = 12
	src := manyMilestonesSource(n)
	files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
	require.NoError(t, err)

	var blocks int
	for _, l := range strings.Split(files.RoadmapMD, "\n") {
		if strings.HasPrefix(l, "### M") {
			blocks++
		}
	}
	assert.Equal(t, n, blocks)
	assert.Contains(t, files.RoadmapMD, "Status: abandoned")
}

func TestRender_RoadmapPreambleCountIsDerived(t *testing.T) {
	for _, n := range []int{3, 12} {
		src := manyMilestonesSource(n)
		files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID, render.WithDetail())
		require.NoError(t, err)
		assert.Contains(t, files.RoadmapMD, fmt.Sprintf("%d milestones (1 abandoned)", n))
	}
}

// A Later capability lists its recorded protecting decisions; one with none
// is listed as uncovered; a delivered capability is not in the section.
func TestRender_RoadmapLaterCoverage(t *testing.T) {
	productID, scopeID, fsID := uuid.New(), uuid.New(), uuid.New()
	delivered := newFeature("Delivered", 1)
	delivered.FeatureSetID = fsID
	covered := newFeature("Covered", 2)
	covered.FeatureSetID = fsID
	bare := newFeature("Bare", 3)
	bare.FeatureSetID = fsID
	lb := slice.DecisionEntity{EntityRef: slice.EntityRef{ID: uuid.New(), RevisionID: uuid.New()}, Name: "D", DisplayNumber: 7}
	mID := uuid.New()

	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "W", Vision: "v"},
			FeatureSets:   []slice.FeatureSetEntity{{EntityRef: slice.EntityRef{ID: fsID, RevisionID: uuid.New()}, Name: "Core"}},
			Features:      []slice.FeatureEntity{delivered, covered, bare},
			Decisions:     []slice.DecisionEntity{lb},
		},
		MilestoneRefs: []store.MilestoneRef{{ID: mID, Name: "M1", Kind: store.MilestoneKindMilestone}},
		Associations:  map[uuid.UUID][]store.EntityMilestone{mID: {{EntityID: delivered.ID, Relation: store.MilestoneRelationDelivers}}},
		Protects:      []store.LBProtectsFeature{{ID: uuid.New(), DecisionID: lb.ID, FeatureID: covered.ID}},
	}
	files, err := render.Render(context.Background(), src, scopeID, productID)
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "## Later coverage")
	assert.Contains(t, files.RoadmapMD, "- C2 — Covered: LB7")
	assert.Contains(t, files.RoadmapMD, "- C3 — Bare: uncovered")
	assert.NotContains(t, files.RoadmapMD, "Delivered:")
}
