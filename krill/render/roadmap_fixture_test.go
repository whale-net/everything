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
