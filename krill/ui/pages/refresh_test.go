package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func renderBody(t *testing.T, c templ.Component) string {
	t.Helper()
	var sb strings.Builder
	require.NoError(t, c.Render(context.Background(), &sb))
	return sb.String()
}

// topLevelElements returns the opening tag names of every top-level
// element in body, ignoring text and comments.
func topLevelElements(body string) []string {
	var tags []string
	depth := 0
	i := 0
	for i < len(body) {
		lt := strings.Index(body[i:], "<")
		if lt < 0 {
			break
		}
		i += lt
		rest := body[i:]
		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest, "-->")
			if end < 0 {
				return tags
			}
			i += end + 3
			continue
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			end := strings.Index(rest, ">")
			if end < 0 {
				return tags
			}
			i += end + 1
			continue
		}
		nameEnd := strings.IndexAny(rest, " >/")
		if nameEnd < 0 {
			return tags
		}
		name := rest[1:nameEnd]
		if strings.HasPrefix(name, "/") {
			// closing tag
			depth--
			if depth < 0 {
				depth = 0
			}
			end := strings.Index(rest, ">")
			if end < 0 {
				return tags
			}
			i += end + 1
			continue
		}
		// self-closing?
		gt := strings.Index(rest, ">")
		selfClosing := gt >= 0 && strings.HasSuffix(strings.TrimSpace(rest[:gt]), "/")
		if depth == 0 {
			tags = append(tags, name)
		}
		if !selfClosing {
			depth++
		}
		if gt < 0 {
			return tags
		}
		i += gt + 1
	}
	return tags
}

// TestSpecFragmentIsExactlyItsSwapTarget is the invariant that actually
// decides whether a Refresh click works, and getting it wrong is subtle
// in both directions.
//
// The Refresh button does hx-get=<this page> hx-target="#<anchor>"
// hx-swap="outerHTML". The handler's htmx branch serves the whole page
// component, so what htmx inserts is every TOP-LEVEL node of that
// response. Therefore:
//
//   - If the served fragment has MORE than one top-level element, htmx
//     splices them all into the target: the heading and the button
//     duplicate, once per click, without bound.
//   - The button is not destroyed by a swap even though it sits inside
//     the target, because the response carries a fresh one.
//
// The first point is a bug that was once introduced here and shipped
// green: hoisting the header out of the section left the response with
// two top-level nodes while the target was still only the section. So
// this asserts the response shape directly rather than reasoning about
// DOM containment.
func TestSpecFragmentIsExactlyItsSwapTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		anchor string
		page   templ.Component
	}{
		{"products", ProductsAnchor, Products(ProductsPage{Path: "/spec/products"})},
		{"capability map", CapabilityMapAnchor, CapabilityMap(CapabilityPage{Path: "/spec/products/p"})},
		{"decisions", DecisionsAnchor, Decisions(DecisionsPage{Path: "/spec/products/p"})},
		{"personas", PersonasAnchor, Personas(PersonasPage{Path: "/spec/products/p"})},
		{"non-goals", NonGoalsAnchor, NonGoals(NonGoalsPage{Path: "/spec/products/p"})},
		{"delivery", DeliveryAnchor, Delivery(DeliveryPage{Path: "/spec/products/p/delivery"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The fragment the handler's htmx branch serves.
			fragment := renderBody(t, tc.page)

			top := topLevelElements(fragment)
			require.Len(t, top, 1,
				"the served fragment must be exactly the swap target, or every Refresh click splices the extra top-level nodes into the page (duplicating the heading and the button)")
			assert.Equal(t, "section", top[0],
				"the fragment's single root must be the section hx-target names")

			// The target must actually be the root, not a descendant.
			assert.True(t, strings.HasPrefix(fragment, `<section id="`+tc.anchor+`"`),
				"the fragment's root element must carry the id the Refresh button targets")

			// And exactly one header, so a swap cannot duplicate one.
			assert.Equal(t, 1, strings.Count(fragment, `data-krill="refresh"`),
				"exactly one Refresh button in the fragment, or a swap duplicates it")
			assert.Equal(t, 1, strings.Count(fragment, `data-krill="page-title"`),
				"exactly one page title in the fragment, or a swap duplicates it")
		})
	}
}
