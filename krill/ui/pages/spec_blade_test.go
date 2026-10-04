package pages

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// The feature quick-look blade at the COMPONENT level (FR f7eee645): the
// markup shape, independent of the handler that serves it.
//
// These cases exist because the handler-level tests in //krill/ui assert
// what the blade SHOWS, and a template that renders it correctly-shaped but
// wrong-anchored would still pass every one of them as long as the handler
// put the right bytes in the response. The invariants here are the ones
// that decide whether htmx can act on the blade at all.

// bladeFixture is a populated blade, so the shape assertions run against
// markup that has something in it rather than against an empty region.
func bladeFixture() SpecBladePage {
	return SpecBladePage{
		CloseHref: "/spec/products/11111111-1111-1111-1111-111111111111?open=22222222-2222-2222-2222-222222222222",
		Feature: &SpecBladeFeature{
			ID:          "33333333-3333-3333-3333-333333333333",
			Number:      7,
			Name:        "Scoped slice",
			Description: "one query, four granularities",
			Milestone:   CapabilityMilestone{Kind: CapabilityMilestoneNamed, Name: "P3b Milestones", Status: "in_progress", Count: 1},
			Requirements: []SpecBladeRequirement{
				{ID: "44444444-4444-4444-4444-444444444444", Citation: "FR1", Name: "returns every child"},
				{ID: "55555555-5555-5555-5555-555555555555", Citation: "NFR1", Name: "stays cheap"},
			},
		},
	}
}

// TestSpecBladeSlotCarriesItsSwapTarget is the guard that matters most:
// the slot's ROOT must be the region the Capabilities table's Feature link
// replaces, whether or not a blade is showing.
//
// Both spellings need it -- the closed placeholder is what a close swaps
// back in, and the open one is what a click swaps in -- so a region that
// carried the id only when open would lose it on the very first close and
// make every later click a no-op that never reaches the server.
func TestSpecBladeSlotCarriesItsSwapTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		page SpecBladePage
	}{
		{name: "closed", page: SpecBladePage{}},
		{name: "open", page: bladeFixture()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := renderBody(t, SpecBladeSlot(tc.page))
			assert.Contains(t, out, `id="`+SpecBladeAnchor+`"`,
				"the blade region's root must be the target its callers name")
			assert.Contains(t, out, `data-krill="spec-blade"`)
		})
	}
}

// TestSpecBladeClosedSlotCarriesNoBody pins the placeholder as empty. A
// closed region that rendered a stale blade would leave the previous
// feature's detail on screen after its Close, which is the one outcome the
// Close exists to prevent.
func TestSpecBladeClosedSlotCarriesNoBody(t *testing.T) {
	out := renderBody(t, SpecBladeSlot(SpecBladePage{}))

	assert.NotContains(t, out, `data-krill="spec-blade-body"`, "a closed region rendered a blade body")
	assert.NotContains(t, out, `data-krill="spec-blade-close"`, "a closed region rendered a Close control")
}

// TestSpecBladeShowsItsFeaturesFields pins the FR's own list at the
// component level: the display number and name, the milestone badge, the
// id as a copy chip, the description, and each requirement as
// `FRn name` / `NFRn name`.
func TestSpecBladeShowsItsFeaturesFields(t *testing.T) {
	out := renderBody(t, SpecBladeSlot(bladeFixture()))

	for _, want := range []string{
		`data-krill="spec-blade-name">C7 — Scoped slice`,
		`data-krill="feature-milestone-name">P3b Milestones`,
		"33333333-3333-3333-3333-333333333333",
		"one query, four granularities",
		"FR1",
		"NFR1",
		"returns every child",
		"stays cheap",
	} {
		assert.Contains(t, out, want)
	}
}

// TestSpecBladeReusesTheTablesMilestoneBadge pins that the blade's
// Milestone cell goes through the SAME component the Capabilities table's
// column does.
//
// This is one derivation, not two: a blade that reported a milestone by a
// second path could disagree with the row it was opened from, and the two
// would sit on screen at once. Asserting the table's own hook inside the
// blade is what makes the sharing checkable rather than merely intended.
func TestSpecBladeReusesTheTablesMilestoneBadge(t *testing.T) {
	out := renderBody(t, SpecBladeSlot(bladeFixture()))
	assert.Contains(t, out, `data-krill="feature-milestone-name"`,
		"the blade must render the milestone badge through the table's own component")

	// And the other three states too: a blade hardwired to the named case
	// would pass the assertion above and lose the blank, the count and
	// the failed read.
	for _, tc := range []struct {
		kind string
		want string
	}{
		{kind: CapabilityMilestoneSeveral, want: `data-krill="feature-milestone-count">2 milestones`},
		{kind: CapabilityMilestoneNone, want: `data-krill="feature-milestone-none"`},
		{kind: CapabilityMilestoneUnread, want: `data-krill="feature-milestone-unread"`},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			page := bladeFixture()
			page.Feature.Milestone = CapabilityMilestone{Kind: tc.kind, Count: 2, Message: "delivery could not be read"}
			out := renderBody(t, SpecBladeSlot(page))
			assert.Contains(t, out, tc.want)
		})
	}
}

// TestSpecBladeCopyChipShipsDisabled pins the degradation the shared head
// script upgrades, at the level where the markup is written: the chip is
// disabled, names its reason, and sits in a <dd> beside the one status
// region the script announces into.
//
// The <dd> is the part a shape-only test would miss. The script's statusOf
// does btn.closest('dd'), so a chip outside one copies correctly and then
// confirms nothing at all -- the operator is told nothing and left
// guessing.
func TestSpecBladeCopyChipShipsDisabled(t *testing.T) {
	out := renderBody(t, SpecBladeSlot(bladeFixture()))

	for _, want := range []string{
		`data-krill="copy-task-id"`,
		`data-task-id="33333333-3333-3333-3333-333333333333"`,
		`aria-label="Copy feature id"`,
		`title="Copying the feature id needs JavaScript"`,
		`disabled`,
		`data-krill="copy-task-id-status"`,
		`role="status"`,
		`aria-live="polite"`,
	} {
		assert.Contains(t, out, want)
	}

	// The two hooks share one <dd>, which is the scope the script looks in.
	doc := bladeParse(t, out)
	var dd *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "dd" {
			dd = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.NotNil(t, dd, "the chip must sit in a <dd>")
	for _, hook := range []string{"copy-task-id", "copy-task-id-status"} {
		found := false
		var inner func(*html.Node)
		inner = func(n *html.Node) {
			if n.Type == html.ElementNode {
				for _, a := range n.Attr {
					if a.Key == "data-krill" && a.Val == hook {
						found = true
					}
				}
			}
			for c := n.FirstChild; c != nil; c = c.NextSibling {
				inner(c)
			}
		}
		inner(dd)
		assert.True(t, found, "%s is not inside the chip's <dd>", hook)
	}
}

// TestSpecBladeOpensNoSecondBlade pins the depth rule against the MARKUP,
// which is where a second blade would have to be written: no htmx verb and
// no link anywhere but Close.
//
// Close is exempt because closing is not opening -- it is the one way out,
// and it names the tab path rather than another blade. The requirement
// lines are plain text: a link there would be a blade opening a blade, and
// would make "back" a question with two answers.
func TestSpecBladeOpensNoSecondBlade(t *testing.T) {
	out := renderBody(t, SpecBladeSlot(bladeFixture()))
	doc := bladeParse(t, out)

	var blade *html.Node
	var find func(*html.Node)
	find = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "data-krill" && a.Val == "spec-blade-body" {
					blade = n
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			find(c)
		}
	}
	find(doc)
	require.NotNil(t, blade, "the fixture must render a blade body to walk")

	for n := blade; n != nil; n = n.NextSibling {
		bladeAssertLeafDeep(t, n)
	}
}

// bladeAssertLeafDeep walks one subtree of the blade, asserting every
// htmx verb and every link is either the Close or absent.
func bladeAssertLeafDeep(t *testing.T, n *html.Node) {
	if n.Type != html.ElementNode {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			bladeAssertLeafDeep(t, c)
		}
		return
	}
	isClose := false
	for _, a := range n.Attr {
		if a.Key == "data-krill" && a.Val == "spec-blade-close" {
			isClose = true
		}
	}
	if n.Data == "a" && !isClose {
		t.Errorf("the blade carries a link other than its Close: <%s>", n.Data)
	}
	if n.Data == "form" {
		t.Error("the blade carries a form: this path is read-only")
	}
	for _, a := range n.Attr {
		switch a.Key {
		case "hx-get", "hx-post", "hx-put", "hx-delete":
			if !isClose {
				t.Errorf("the blade carries %s=%q on a non-Close element: blades go one level deep", a.Key, a.Val)
			}
		case "href":
			if strings.Contains(a.Val, "/features/") {
				t.Errorf("the blade links to another blade (%q): blades go one level deep", a.Val)
			}
		}
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		bladeAssertLeafDeep(t, c)
	}
}

// TestSpecBladeCloseTargetsTheBladeRegion pins the close control's own
// shape: its href IS its hx-get (so a no-JS click navigates to the same
// view), it targets the BLADE region rather than the page around it, and
// it pushes that URL.
//
// Re-rendering the page on close is the specific failure worth guarding: it
// would rebuild the tab and the expansion from the URL, which is a
// different thing from returning to them, and would discard whatever the
// operator had open that the URL does not name.
func TestSpecBladeCloseTargetsTheBladeRegion(t *testing.T) {
	page := bladeFixture()
	out := renderBody(t, SpecBladeSlot(page))

	for _, want := range []string{
		`href="` + page.CloseHref + `"`,
		`hx-get="` + page.CloseHref + `"`,
		`hx-target="#` + SpecBladeAnchor + `"`,
		`hx-swap="outerHTML"`,
		`hx-push-url="true"`,
	} {
		assert.Contains(t, out, want)
	}
}

// TestSpecBladeRendersNoWriteAffordance is the read-only half stated on
// its own, over the lowercased markup so a case-different verb would not
// slip past.
func TestSpecBladeRendersNoWriteAffordance(t *testing.T) {
	out := strings.ToLower(renderBody(t, SpecBladeSlot(bladeFixture())))

	for _, forbidden := range []string{"<form", "hx-post", "hx-put", "hx-delete", `method="post"`} {
		assert.NotContains(t, out, forbidden, "the blade is read-only and must not carry %q", forbidden)
	}
}

// bladeParse is the rendered blade's parsed tree.
func bladeParse(t *testing.T, markup string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err, "the rendered blade must be parseable HTML:\n%s", markup)
	return doc
}