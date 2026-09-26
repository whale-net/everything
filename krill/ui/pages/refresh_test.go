package pages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// refreshAnchorRegion returns the substring of body spanning the
// <section> whose id is anchor -- i.e. exactly the extent of one
// hx-swap="outerHTML" against that anchor.
//
// These pages do not nest sections (a page's region wraps <ol>/<ul> and
// its own component markup), so the first </section> after the id is the
// matching close.
func refreshAnchorRegion(t *testing.T, body, anchor string) string {
	t.Helper()
	open := `id="` + anchor + `"`
	idAt := strings.Index(body, open)
	require.NotEqual(t, -1, idAt, "expected the swapped region %s in the rendered page", anchor)

	start := strings.LastIndex(body[:idAt], "<section")
	require.NotEqual(t, -1, start, "expected the anchor to sit on a <section>")

	end := strings.Index(body[idAt:], "</section>")
	require.NotEqual(t, -1, end, "expected the swapped region %s to be closed", anchor)

	return body[start : idAt+end+len("</section>")]
}

// TestSpecRefreshButtonSurvivesItsOwnSwap is the guard on the bug where
// every spec page's Refresh button lived INSIDE the <section> its own
// hx-swap="outerHTML" replaced. The first click refreshed correctly and
// then destroyed the button, so a second refresh needed a full page load.
//
// The button must therefore be a sibling of the region it targets, never
// a descendant. This asserts DOM containment, which is what actually
// decides the outcome -- asserting on markup order would not.
func TestSpecRefreshButtonSurvivesItsOwnSwap(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor string
		body   string
	}{
		{"products", ProductsAnchor, renderBody(t, Products(ProductsPage{Path: "/spec/products"}))},
		{"capability map", CapabilityMapAnchor, renderBody(t, CapabilityMap(CapabilityPage{Path: "/spec/products/p"}))},
		{"decisions", DecisionsAnchor, renderBody(t, Decisions(DecisionsPage{Path: "/spec/products/p"}))},
		{"personas", PersonasAnchor, renderBody(t, Personas(PersonasPage{Path: "/spec/products/p"}))},
		{"non-goals", NonGoalsAnchor, renderBody(t, NonGoals(NonGoalsPage{Path: "/spec/products/p"}))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Contains(t, tc.body, `data-krill="refresh"`, "the page must offer a refresh")

			region := refreshAnchorRegion(t, tc.body, tc.anchor)
			assert.NotContains(t, region, `data-krill="refresh"`,
				"the Refresh button is inside the region its own swap replaces, so the first click destroys it")
		})
	}
}

// TestDeliveryRefreshButtonSurvivesItsOwnSwap is the same guard for the
// delivery page, which reuses spec.templ's pageHeader/specRefresh.
func TestDeliveryRefreshButtonSurvivesItsOwnSwap(t *testing.T) {
	body := renderBody(t, Delivery(DeliveryPage{Path: "/spec/products/p/delivery"}))

	assert.Contains(t, body, `data-krill="refresh"`)
	region := refreshAnchorRegion(t, body, DeliveryAnchor)
	assert.NotContains(t, region, `data-krill="refresh"`,
		"the Refresh button is inside the region its own swap replaces, so the first click destroys it")
}
