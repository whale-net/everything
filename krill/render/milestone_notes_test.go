package render_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

func notesSource(notes *string) (*fakeSource, uuid.UUID) {
	productID := uuid.New()
	outcome := "An Agent can do the thing"
	frBudget := 12
	return &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product:       &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID, RevisionID: uuid.New()}, Name: "Widgets", Vision: "v"},
		},
		MilestoneRefs: []store.MilestoneRef{{
			ID: uuid.New(), Name: "M1", Kind: store.MilestoneKindMilestone,
			Outcome: &outcome, FRBudget: &frBudget, Notes: notes,
		}},
	}, productID
}

func TestRender_MilestoneNotesRenderByteIdenticalAfterBlock(t *testing.T) {
	notes := "## Why\n\n- `code` <b>&amp;</b> | table |\n\n```go\nx := 1\n```\n"
	src, pid := notesSource(&notes)
	files, err := render.Render(context.Background(), src, uuid.New(), pid, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, "- FR budget: 12\n\n"+notes+"\n")
}

func TestRender_MilestoneNotesLargeUnmodified(t *testing.T) {
	notes := strings.Repeat("line of design rationale with *markdown* & <html>\n", 3000)
	require.GreaterOrEqual(t, len(notes), 100*1024)
	src, pid := notesSource(&notes)
	files, err := render.Render(context.Background(), src, uuid.New(), pid, render.WithDetail())
	require.NoError(t, err)

	assert.Contains(t, files.RoadmapMD, notes)
}

func TestRender_MilestoneWithoutNotesEmitsNothingExtra(t *testing.T) {
	empty := ""
	for _, n := range []*string{nil, &empty} {
		src, pid := notesSource(n)
		files, err := render.Render(context.Background(), src, uuid.New(), pid, render.WithDetail())
		require.NoError(t, err)
		assert.NotContains(t, strings.ToLower(files.RoadmapMD), "notes")
		assert.Contains(t, files.RoadmapMD, "- FR budget: 12\n\n")
		assert.NotContains(t, files.RoadmapMD, "- FR budget: 12\n\n\n")
	}
}
