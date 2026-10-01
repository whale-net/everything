// Unit coverage for the current-state survey preamble handling and the
// deferred non-goals heading, driven through fakeSource.
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

func renderWith(t *testing.T, currentState *string, nonGoals []store.NonGoal) render.Files {
	t.Helper()
	src := &fakeSource{
		Doc: slice.Document{
			SchemaVersion: slice.SchemaVersion,
			Product: &slice.ProductEntity{
				EntityRef:    slice.EntityRef{ID: uuid.New(), RevisionID: uuid.New()},
				Name:         "Widgets",
				Vision:       "Make great widgets.",
				CurrentState: currentState,
			},
		},
		NonGoals: nonGoals,
	}
	files, err := render.Render(context.Background(), src, uuid.New(), src.Doc.Product.ID)
	require.NoError(t, err)
	return files
}

func countH1(md string) int {
	n := 0
	for _, line := range strings.Split(md, "\n") {
		if strings.HasPrefix(line, "# ") {
			n++
		}
	}
	return n
}

func TestRender_CurrentState_StoredBodyOwnTitleAndPartOfLineStripped(t *testing.T) {
	body := "# Current state\n\nPart of the [FCM product brief](../PRODUCT.md). Surveyed 2026-09-23 from the code.\n\nSecond paragraph.\n"
	md := renderWith(t, &body, nil).CurrentStateMD

	assert.Equal(t, 1, countH1(md), "exactly one H1")
	assert.NotContains(t, md, "Part of the")
	assert.True(t, strings.HasSuffix(md, "\n# Current state\n\nSurveyed 2026-09-23 from the code.\n\nSecond paragraph.\n"), md)
}

func TestRender_CurrentState_PartOfOnItsOwnLineStripped(t *testing.T) {
	body := "# Current state\n\nPart of the [FCM product brief](../PRODUCT.md).\n\nBody.\n"
	md := renderWith(t, &body, nil).CurrentStateMD

	assert.Equal(t, 1, countH1(md))
	assert.True(t, strings.HasSuffix(md, "\n# Current state\n\nBody.\n"), md)
}

func TestRender_CurrentState_BodyWithoutPreambleIsVerbatim(t *testing.T) {
	body := "Surveyed from the code.\n\n## Runtime\n\nPart of the story.\n"
	md := renderWith(t, &body, nil).CurrentStateMD

	assert.Equal(t, 1, countH1(md))
	assert.True(t, strings.HasSuffix(md, "\n# Current state\n\n"+body), md)
}

func TestRender_NonGoals_NoDeferredEntriesOmitsHeading(t *testing.T) {
	product := renderWith(t, nil, []store.NonGoal{
		{Kind: store.NonGoalKindPermanent, Name: "Perm", Body: strPtr("PERM-BODY")},
	}).ProductMD

	assert.Contains(t, product, "**Permanent:**")
	assert.Contains(t, product, "- **Perm.** PERM-BODY")
	assert.NotContains(t, product, "deferred, not foreclosed")
}
