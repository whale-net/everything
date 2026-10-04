package main

// The feature quick-look blade (FR f7eee645): the panel that opens OVER
// the Spec page from the Capabilities table's Feature link, one level deep.
//
// The assertions are deliberately about the SHAPE and the round-trip
// rather than about markup: what makes this feature work is that the
// served blade IS the region the link replaces (htmx's outerHTML deletes
// anything whose root is not its target), that a direct load renders the
// whole page under it rather than a bare fragment, and that the blade's
// citations are the ones the renderer would give. A builder that is right
// and a template that renders it into the wrong element are different
// defects, and only parsing the served fragment tells them apart.
//
// The FRn/NFRn agreement is checked against //krill/render's OWN output
// for the same slice.Document, not against a re-statement of the rule: the
// UI calls render.RequirementCitations rather than mirroring it, so this is
// the test that would catch that call being dropped and replaced with a
// local count that happens to agree today.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/whale-net/everything/krill/render"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// fixture
// ---------------------------------------------------------------------------

// bladeIDs are the fixture's entity ids, spelled out so a test names a
// feature or a requirement rather than indexing a slice.
var bladeIDs = struct {
	product    string
	featureSet string
	feature    string // the one the blade is opened for
	featureTwo string // a sibling, so a leak across features is visible
	milestone  string
}{
	product:    "44444444-0000-4000-8000-000000000001",
	featureSet: "44444444-0000-4000-8000-000000000002",
	feature:    "44444444-0000-4000-8000-000000000003",
	featureTwo: "44444444-0000-4000-8000-000000000004",
	milestone:  "44444444-0000-4000-8000-000000000021",
}

// bladeReqs are the fixture's requirement ids. They are named after the
// citation each one is EXPECTED to carry, which is the whole point: a
// feature's first FR is only "FR1" because no earlier feature in the
// document had one, so a per-feature numbering would agree for the first
// feature and silently disagree for the second.
var bladeReqs = struct {
	featureFirstFR  string // under feature:  FR1
	featureNFR      string // under feature:  NFR1
	otherFirstFR    string // under featureTwo: FR2
	otherNFR        string // under featureTwo: NFR2
	orphanRequirement string // under no feature: unnumbered
}{
	featureFirstFR:    "44444444-0000-4000-8000-000000000011",
	featureNFR:        "44444444-0000-4000-8000-000000000012",
	otherFirstFR:      "44444444-0000-4000-8000-000000000013",
	otherNFR:          "44444444-0000-4000-8000-000000000014",
	orphanRequirement: "44444444-0000-4000-8000-000000000015",
}

// bladeDoc is a slice whose requirement order is chosen so a per-feature
// count and the render package's per-KIND count disagree for the SECOND
// feature: `feature` carries an FR and an NFR, and `featureTwo` follows
// with its own FR and NFR. Counting per feature would call them FR1/NFR1
// again; counting per kind down the document calls them FR2/NFR2, which
// is what the rendered PRODUCT.md says.
func bladeDoc(t *testing.T) slice.Document {
	t.Helper()
	productID := mustID(t, bladeIDs.product)
	fsID := mustID(t, bladeIDs.featureSet)
	featureID := mustID(t, bladeIDs.feature)
	twoID := mustID(t, bladeIDs.featureTwo)
	orphan := mustID(t, "cccccccc-0000-4000-8000-0000000000ff")

	return slice.Document{
		Product:    &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID}, Name: "krill", Vision: "the substrate"},
		FeatureSets: []slice.FeatureSetEntity{
			{EntityRef: slice.EntityRef{ID: fsID}, ProductID: productID, Name: "Spec axis", Description: ptr("feature sets about the spec")},
		},
		Features: []slice.FeatureEntity{
			{EntityRef: slice.EntityRef{ID: featureID}, FeatureSetID: fsID, Name: "Scoped slice", Description: ptr("one query, four granularities"), DisplayNumber: 7},
			{EntityRef: slice.EntityRef{ID: twoID}, FeatureSetID: fsID, Name: "Delivery listing", Description: nil, DisplayNumber: 3},
		},
		Requirements: []slice.RequirementEntity{
			{EntityRef: slice.EntityRef{ID: mustID(t, bladeReqs.featureFirstFR)}, FeatureID: featureID, Kind: "FR", Name: "returns every child", Body: ptr("the body")},
			{EntityRef: slice.EntityRef{ID: mustID(t, bladeReqs.featureNFR)}, FeatureID: featureID, Kind: "NFR", Name: "stays cheap", Body: nil},
			{EntityRef: slice.EntityRef{ID: mustID(t, bladeReqs.otherFirstFR)}, FeatureID: twoID, Kind: "FR", Name: "delivery rows", Body: nil},
			{EntityRef: slice.EntityRef{ID: mustID(t, bladeReqs.otherNFR)}, FeatureID: twoID, Kind: "NFR", Name: "reads in one query", Body: nil},
			// An orphan: the render package leaves it unnumbered because
			// it has no position in the document, and the blade must not
			// invent a number for it either.
			{EntityRef: slice.EntityRef{ID: orphan}, FeatureID: mustID(t, "cccccccc-0000-4000-8000-0000000000ee"), Kind: "FR", Name: "orphan", Body: nil},
		},
	}
}

// bladeListing delivers the fixture's feature, so the blade's Milestone
// badge has something real to name.
func bladeListing(t *testing.T) slice.DeliveryListing {
	t.Helper()
	return slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{{
		ID:     mustID(t, bladeIDs.milestone),
		Name:   "P3b Milestones",
		Status: store.MilestoneStatusInProgress,
		Delivers: slice.Document{
			Features: []slice.FeatureEntity{{EntityRef: slice.EntityRef{ID: mustID(t, bladeIDs.feature)}}},
		},
	}}}
}

// bladeReader is a specReadClient serving the fixture's slice and
// delivery listing, so the blade route can be driven end to end.
type bladeReader struct {
	specStubReader
	doc     slice.Document
	listing slice.DeliveryListing
}

// ProductSlice answers only for the product the fixture is built around,
// and reports store.ErrNotFound for any other -- the contract the real
// querier has, and the one that makes a blade URL naming a product this
// deployment does not have an in-shell 404 rather than a page built out of
// another product's spec.
func (r bladeReader) ProductSlice(_ context.Context, id uuid.UUID) (slice.Document, error) {
	if id != r.doc.Product.ID {
		return slice.Document{}, store.ErrNotFound
	}
	return r.doc, nil
}

// Products lists the fixture's one product, so a full-page render's
// sidebar switcher has something to render.
func (r bladeReader) Products(context.Context) ([]store.Product, error) {
	return []store.Product{{ID: r.doc.Product.ID, Name: r.doc.Product.Name, Vision: r.doc.Product.Vision}}, nil
}

func (r bladeReader) Product(context.Context, uuid.UUID) (store.Product, error) {
	return store.Product{ID: r.doc.Product.ID, Name: r.doc.Product.Name, Vision: r.doc.Product.Vision}, nil
}

func (r bladeReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return r.listing, nil
}

// bladeMux mounts the real shell registrations over the fixture, so the
// blade route under test is the one production registers rather than a
// copy that could drift from it.
func bladeMux(t *testing.T) *http.ServeMux {
	t.Helper()
	doc := bladeDoc(t)
	app := newTestApp(t)
	app.spec = bladeReader{doc: doc, listing: bladeListing(t)}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// bladeGet issues one request the way its caller would. hxTarget is what
// the control names; empty means a plain browser navigation.
func bladeGet(t *testing.T, mux *http.ServeMux, path, hxTarget string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if hxTarget != "" {
		req.Header.Set("HX-Request", "true")
		req.Header.Set("HX-Target", hxTarget)
	}
	mux.ServeHTTP(rec, req)
	return rec
}

func bladeURL(t *testing.T, query string) string {
	t.Helper()
	return productPath(mustID(t, bladeIDs.product)) + "/features/" + bladeIDs.feature + query
}

// ---------------------------------------------------------------------------
// the swap shape
// ---------------------------------------------------------------------------

// TestBladeFragmentIsItsOwnSwapTarget pins the invariant that makes the
// blade open at all: the served fragment's ROOT is the region the Feature
// link replaced, and it is the fragment's only top-level element.
//
// A root that is not the swap target is the worse failure -- htmx's
// outerHTML deletes what it was told to replace and splices in something
// with no matching id, so the second click has no target and never
// reaches the server again. A second top-level element splices itself in
// beside the blade, duplicating it on every press.
func TestBladeFragmentIsItsOwnSwapTarget(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	require.Len(t, topLevelTagsOf(t, body), 1,
		"a blade fragment with more than one root splices itself in again on the next open")
	root := topLevelElement(t, body)
	assert.Equal(t, pages.SpecBladeAnchor, attrOf(t, root, "id"),
		"the blade fragment's root must be the region the Feature link replaced")
	assert.Equal(t, "spec-blade", attrOf(t, root, "data-krill"),
		"the blade root must carry the spec-blade hook")
}

// TestBladeFragmentIsOnlyTheBlade pins what an htmx open must NOT carry:
// the Spec page under it is never re-rendered, because re-deriving it here
// would quietly hand back a different tab or a different open section than
// the one the operator chose.
func TestBladeFragmentIsOnlyTheBlade(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	assert.Empty(t, elementsWithHook(t, body, "spec-tabs"),
		"the blade fragment re-rendered the tab strip: the page under a blade must not be re-derived")
	assert.Empty(t, elementsWithHook(t, body, "feature-table"),
		"the blade fragment re-rendered the capability table under it")
}

// ---------------------------------------------------------------------------
// the round trip
// ---------------------------------------------------------------------------

// TestFeatureLinkIsTheBladesOwnURL pins the entry: the Feature cell's href
// IS the blade URL, and its hx-get is that same address targeting the
// blade region and pushing it.
//
// The href is what a reload, a shared link, the Back button and a
// no-JavaScript click all follow, so a link whose href and hx-get
// disagree can put the address bar somewhere the page never rendered.
func TestFeatureLinkIsTheBladesOwnURL(t *testing.T) {
	mux := bladeMux(t)
	body := fetch(t, mux, productPath(mustID(t, bladeIDs.product))).Body.String()

	links := elementsWithHook(t, body, "feature-quick-look")
	require.Len(t, links, 2, "both fixture features must link to their own quick look")

	// The expansion travels with the link so a blade opened over a
	// non-default section closes back onto THAT section; the default is
	// spelled too, because the default is a resolved state (first section
	// open) rather than "no query at all".
	base := productPath(mustID(t, bladeIDs.product)) + "/features/" +
		"?open=" + mustID(t, bladeIDs.featureSet).String()
	seen := map[string]bool{}
	for _, link := range links {
		href := attrOf(t, link, "href")
		// Each row links to ITS OWN feature's id: a link that reached for
		// the first feature every time would open the same blade twice and
		// the second feature would have no quick look at all.
		if !strings.HasPrefix(href, strings.TrimSuffix(base, "?open="+mustID(t, bladeIDs.featureSet).String())) {
			t.Errorf("a feature's quick look links to %q, want a URL under the blade prefix", href)
		}
		seen[href] = true
		assert.Equal(t, href, attrOf(t, link, "hx-get"),
			"hx-get must be the link's own href, or the address bar and the page disagree")
		assert.Equal(t, "#"+pages.SpecBladeAnchor, attrOf(t, link, "hx-target"),
			"a feature link must target the blade region, never the page around it")
		assert.Equal(t, "outerHTML", attrOf(t, link, "hx-swap"))
		assert.Equal(t, "true", attrOf(t, link, "hx-push-url"),
			"a feature link must push its own URL so a reload lands on the blade")
	}

	// Spelled out rather than read off the links: a test that derived its
	// expectation from the markup would pass with one feature's link
	// missing and the other duplicated.
	for _, id := range []string{bladeIDs.feature, bladeIDs.featureTwo} {
		want := productPath(mustID(t, bladeIDs.product)) + "/features/" + id + base[strings.Index(base, "?"):]
		if !seen[want] {
			t.Errorf("the table carries no quick-look link to %s; it carries %v", want, keysOf(seen))
		}
	}
}

// TestBladeDirectLoadRendersTheWholePageWithIt pins the second mode: a
// browser request for the blade URL answers 200 with the FULL Spec page --
// the tab strip, the capability table, the shell chrome -- AND the blade
// open over it.
//
// A bare fragment here would be the failure worth catching: it renders
// correctly in an htmx swap and is unusable as a shared link, a reload,
// and every no-JavaScript click, which is the whole reason the href
// exists.
func TestBladeDirectLoadRendersTheWholePageWithIt(t *testing.T) {
	mux := bladeMux(t)
	rec := bladeGet(t, mux, bladeURL(t, ""), "")

	require.Equal(t, http.StatusOK, rec.Code, "GET %s = %d, want 200", bladeURL(t, ""), rec.Code)
	body := rec.Body.String()

	assertShellChrome(t, bladeURL(t, ""), bladeURL(t, ""), body)
	for _, want := range []string{
		`data-krill="spec-tabs"`,  // the tab strip
		`data-krill="feature-table"`, // the capability table under the blade
		`data-krill="spec-blade-body"`, // and the blade itself
		"Scoped slice",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("GET %s is missing %q: the blade URL must render the whole page with the blade open", bladeURL(t, ""), want)
		}
	}
}

// TestBladeDirectLoadRestoresTheURLsExpansion pins that the tab under a
// direct load is expanded as THIS url names, not by whatever the default
// happens to be. A blade URL that dropped the query would open over a
// collapsed section and its Close would then close the wrong one.
func TestBladeDirectLoadRestoresTheURLsExpansion(t *testing.T) {
	mux := bladeMux(t)
	fsID := mustID(t, bladeIDs.featureSet)
	path := bladeURL(t, "?"+capabilityExpansionQueryParam+"="+fsID.String())
	body := fetch(t, mux, path).Body.String()

	sections := elementsWithHook(t, body, "feature-set")
	require.NotEmpty(t, sections)
	for _, section := range sections {
		assert.Equal(t, "true", attrOf(t, section, "data-krill-feature-set-open"),
			"GET %s: the section the URL names must be open under the blade", path)
	}
}

// TestBladeCloseReturnsToTheSameTabAndSection pins the round trip's other
// half. The Close names the blade REGION, so the page under it is never
// re-rendered and the tab and open section are exactly what they were --
// and it carries the tab's own path with the expansion this blade was
// opened over, so a close on a directly-loaded blade lands on the section
// its own URL named.
func TestBladeCloseReturnsToTheSameTabAndSection(t *testing.T) {
	mux := bladeMux(t)
	fsID := mustID(t, bladeIDs.featureSet)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{name: "default expansion", query: ""},
		{name: "the URL's own expansion", query: "?" + capabilityExpansionQueryParam + "=" + fsID.String()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := bladeGet(t, mux, bladeURL(t, tc.query), "#"+pages.SpecBladeAnchor).Body.String()
			closes := elementsWithHook(t, body, "spec-blade-close")
			require.Len(t, closes, 1, "an open blade must carry exactly one close control")

			href := attrOf(t, closes[0], "href")
			assert.True(t, strings.HasPrefix(href, productPath(mustID(t, bladeIDs.product))),
				"Close points at %q, want the Capabilities tab the blade was opened from", href)
			assert.Equal(t, href, attrOf(t, closes[0], "hx-get"))
			assert.Equal(t, "#"+pages.SpecBladeAnchor, attrOf(t, closes[0], "hx-target"),
				"Close must swap the BLADE region: re-rendering the page would lose the tab and the open section")
			assert.Equal(t, "outerHTML", attrOf(t, closes[0], "hx-swap"))
			assert.Equal(t, "true", attrOf(t, closes[0], "hx-push-url"),
				"Close must push the tab's URL, or the address bar keeps naming a blade that is gone")

			// And the href must be the tab's own path carrying THIS
			// blade's resolved expansion. Both cases resolve to the same
			// set here, which is the point: arriving with no query means
			// "the default", and the default IS the first section open --
			// so a Close that read it as "nothing is open" would drop the
			// operator onto an all-shut page.
			want := productPath(mustID(t, bladeIDs.product)) +
				"?" + capabilityExpansionQueryParam + "=" + mustID(t, bladeIDs.featureSet).String()
			assert.Equal(t, want, href)
		})
	}
}

// TestBladeCloseWithNothingOpenSaysSo pins the one case a bare path gets
// wrong: the page's DEFAULT is the first section open, so a Close that
// dropped the query would land on the very section the operator closed.
func TestBladeCloseWithNothingOpenSaysSo(t *testing.T) {
	closed := capabilityExpansion{Path: productPath(mustID(t, bladeIDs.product)), Open: map[uuid.UUID]bool{}, Closed: true}
	assert.Equal(t,
		productPath(mustID(t, bladeIDs.product))+"?"+capabilityExpansionQueryParam+"="+capabilityExpansionNone,
		productPath(mustID(t, bladeIDs.product))+closed.query(),
		"a blade opened over an all-shut page must close to the all-shut page")
}

// ---------------------------------------------------------------------------
// the blade's own body
// ---------------------------------------------------------------------------

// TestBladeShowsTheFeaturesOwnFields pins FR f7eee645's list: the display
// number and name, the milestone badge, the id, the description through
// renderMarkdown, and the requirements as `FRn name` / `NFRn name` lines.
func TestBladeShowsTheFeaturesOwnFields(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	for _, want := range []string{
		`data-krill="spec-blade-name">C7 — Scoped slice`,
		`data-krill="feature-milestone-name">P3b Milestones`,
		bladeIDs.feature,
		"one query, four granularities",
		`data-krill="spec-blade-requirement"`,
		"FR1",
		"NFR1",
		"returns every child",
		"stays cheap",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("the blade is missing %q", want)
		}
	}
}

// TestBladeShowsOnlyItsOwnFeaturesRequirements is the counterpart: a blade
// that leaked a sibling's requirement would show a line whose citation
// belongs to another capability, which is precisely the confusion the
// citation is there to prevent.
func TestBladeShowsOnlyItsOwnFeaturesRequirements(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	lines := elementsWithHook(t, body, "spec-blade-requirement")
	require.Len(t, lines, 2, "the fixture's feature carries exactly two requirements")

	for _, other := range []string{"delivery rows", "reads in one query", "orphan"} {
		if strings.Contains(body, other) {
			t.Errorf("the blade shows %q, which belongs to another feature", other)
		}
	}
}

// TestBladeRequirementCitationsMatchTheRenderPackage is the citation rule
// itself. The blade's numbers are checked against
// render.RequirementCitations for the SAME slice.Document -- the function
// the UI calls -- so this fails the moment the UI stops sharing it and
// starts numbering its own way, even if the two agree today.
//
// The fixture is the load-bearing half: `feature`'s requirements are FR1
// and NFR1 under EITHER rule, so only the sibling's rows distinguish a
// per-feature count from the render package's per-KIND count down the
// document. Asserting only on `feature` would pass against the very
// implementation this is here to catch.
func TestBladeRequirementCitationsMatchTheRenderPackage(t *testing.T) {
	doc := bladeDoc(t)
	productID := mustID(t, bladeIDs.product)
	listing := bladeListing(t)

	rendered := render.RequirementCitations(doc)
	require.Equal(t, "FR2", rendered[mustID(t, bladeReqs.otherFirstFR)],
		"the fixture must make the render package's per-kind count differ from a per-feature one")

	for _, key := range []string{bladeIDs.feature, bladeIDs.featureTwo} {
		t.Run(key, func(t *testing.T) {
			page, ok := specBladePageOf(doc, productID, mustID(t, key),
				capabilityExpansion{Path: productPath(productID)}, listing, nil)
			require.True(t, ok)
			require.NotNil(t, page.Feature)
			require.NotEmpty(t, page.Feature.Requirements)

			for _, line := range page.Feature.Requirements {
				want := rendered[mustID(t, line.ID)]
				require.NotEmpty(t, want, "the render package leaves %s unnumbered", line.ID)
				assert.Equal(t, want, line.Citation,
					"the blade cites %s as %q where the rendered PRODUCT.md cites %q",
					line.ID, line.Citation, want)
			}
		})
	}
}

// TestBladeOrphanRequirementIsNotNumbered pins the render package's own
// edge: a requirement whose parent feature is not in the slice has no
// position in the document to count from, so the renderer leaves it
// unnumbered. The blade shows it by KIND rather than inventing a number
// nobody else uses.
func TestBladeOrphanRequirementIsNotNumbered(t *testing.T) {
	doc := bladeDoc(t)
	assert.NotContains(t, render.RequirementCitations(doc), mustID(t, bladeReqs.orphanRequirement),
		"the render package must leave an orphan unnumbered, or this fixture is not testing what it claims")

	// The orphan is not under the fixture's feature, so the blade cannot
	// show it at all -- and the builder must not panic reaching for it.
	page, ok := specBladePageOf(doc, mustID(t, bladeIDs.product), mustID(t, bladeIDs.feature),
		capabilityExpansion{Path: productPath(mustID(t, bladeIDs.product))}, slice.DeliveryListing{}, nil)
	require.True(t, ok)
	for _, line := range page.Feature.Requirements {
		assert.NotEqual(t, "FR", line.Citation,
			"a bare kind is the fallback for an unnumbered requirement, never a numbered one")
	}
}

// ---------------------------------------------------------------------------
// one level deep
// ---------------------------------------------------------------------------

// TestBladeOpensNoSecondBlade pins FR f7eee645's depth rule. Nothing in
// the blade is a link, an hx-get or a control that would open another
// blade: the requirement lines are text, and the only navigation out is
// Close.
//
// It is asserted over the PARSED subtree rather than the whole response,
// so the shell chrome's own links cannot satisfy or fail it by accident.
func TestBladeOpensNoSecondBlade(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	root := topLevelElement(t, body)
	doc := parsedBody(t, body)

	var blades []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && attrValue(n, "data-krill") == "spec-blade-body" {
			blades = append(blades, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.Len(t, blades, 1, "the fragment must carry exactly one blade body")

	for _, n := range descendants(blades[0]) {
		switch n.Type {
		case html.ElementNode:
			switch n.Data {
			case "a":
				if attrValue(n, "data-krill") != "spec-blade-close" {
					t.Errorf("the blade carries a link other than its Close: <%s>", n.Data)
				}
			case "form":
				t.Error("the blade carries a form: this path is read-only")
			}
			if strings.Contains(strings.ToLower(n.Data), "button") && attrValue(n, "data-krill") == "" {
				t.Error("the blade carries a button that is neither Close nor the copy chip")
			}
			for _, attr := range []string{"hx-get", "hx-post", "hx-put", "hx-delete"} {
				if v, ok := attrOfNode(n, attr); ok && attrValue(n, "data-krill") != "spec-blade-close" {
					t.Errorf("the blade carries %s=%q on a non-Close element: blades go one level deep", attr, v)
				}
			}
		case html.TextNode:
			continue
		}
	}
	assert.NotNil(t, root)
}

// TestBladeCarriesNoWriteAffordance is the read-only half stated on its
// own, because it is a property of the whole served blade rather than of
// any one element: no form, no hx-post, no operator route anywhere on this
// path.
func TestBladeCarriesNoWriteAffordance(t *testing.T) {
	mux := bladeMux(t)
	body := strings.ToLower(bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String())

	for _, forbidden := range []string{"<form", "hx-post", "hx-put", "hx-delete", "method=\"post\""} {
		assert.NotContains(t, body, forbidden, "the blade is read-only and must not carry %q", forbidden)
	}
}

// ---------------------------------------------------------------------------
// the copy chip
// ---------------------------------------------------------------------------

// TestBladeCopyChipShipsDisabledWithItsReason pins the degradation the
// shared head script upgrades: the chip is served DISABLED, saying in its
// title that copying needs JavaScript, because a control that is
// guaranteed to fail must not look live.
//
// It also pins the two hooks the existing script binds by --
// data-krill="copy-task-id" and the sibling status span -- and the <dd>
// scope the script's statusOf() looks in. A chip outside a <dd> copies
// correctly and then confirms nothing at all, which is why the ancestor is
// asserted rather than assumed.
func TestBladeCopyChipShipsDisabledWithItsReason(t *testing.T) {
	mux := bladeMux(t)
	body := bladeGet(t, mux, bladeURL(t, ""), "#"+pages.SpecBladeAnchor).Body.String()

	doc := parsedBody(t, body)

	var chip *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && attrValue(n, "data-krill") == "copy-task-id" {
			chip = n
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.NotNil(t, chip, "the blade carries no copy chip")

	assert.Equal(t, "Copying the feature id needs JavaScript", attrOf(t, chip, "title"),
		"the chip must say why it is disabled")
	if _, ok := attrOfNode(chip, "disabled"); !ok {
		t.Error("the chip must ship disabled; the head script is what enables it")
	}
	assert.Equal(t, "Copy feature id", attrOf(t, chip, "aria-label"))
	assert.Equal(t, bladeIDs.feature, attrOf(t, chip, "data-task-id"))
	assert.Equal(t, "button", chip.Data)

	// The status span the script announces into, inside the same <dd>.
	dd := ancestorTag(chip, "dd")
	require.NotNil(t, dd, "the chip must sit in a <dd>: that is the scope the shared script looks in")
	statuses := elementsWithHook(t, innerHTML(t, dd), "copy-task-id-status")
	require.Len(t, statuses, 1, "the chip's <dd> must carry the one status region the script writes into")
	assert.Equal(t, "status", attrOf(t, statuses[0], "role"))
	assert.Equal(t, "polite", attrOf(t, statuses[0], "aria-live"))
}

// ---------------------------------------------------------------------------
// the failures
// ---------------------------------------------------------------------------

// TestBladeUnknownFeatureIsAnInShell404 pins the two failure modes apart.
// An id that parses but names no current feature of this product is a 404
// the operator can act on; an id that does not parse is a 400. Both render
// inside the shell, because a bare http.Error string here would leave an
// operator on a page with no way back.
func TestBladeUnknownFeatureIsAnInShell404(t *testing.T) {
	mux := bladeMux(t)
	base := productPath(mustID(t, bladeIDs.product)) + "/features/"

	for _, tc := range []struct {
		name string
		path string
		want int
	}{
		{name: "an id of no feature in this slice", path: base + "99999999-0000-4000-8000-00000000dead", want: http.StatusNotFound},
		{name: "a feature of no product", path: productPath(uuid.New()) + "/features/" + bladeIDs.feature, want: http.StatusNotFound},
		{name: "an unparseable feature id", path: base + "not-a-uuid", want: http.StatusBadRequest},
		{name: "an unparseable product id", path: "/spec/products/not-a-uuid/features/" + bladeIDs.feature, want: http.StatusBadRequest},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, mux, tc.path)
			require.Equal(t, tc.want, rec.Code, "GET %s = %d, want %d", tc.path, rec.Code, tc.want)

			body := rec.Body.String()
			if !strings.Contains(body, `data-krill="workspace-shell"`) {
				t.Errorf("GET %s answered %d outside the shell; an operator would be stranded", tc.path, rec.Code)
			}
			assert.NotContains(t, body, `data-krill="spec-blade-body"`,
				"GET %s rendered a blade for an id that names no feature", tc.path)
		})
	}
}

// TestBladeUnreadableDeliveryIsNeverABlankCell is the same safety property
// the Capabilities table's column carries: a delivery listing that could
// not be read must say so in the blade too, because a blank cell there
// asserts nothing delivers the feature -- a claim about delivery nobody
// could make.
func TestBladeUnreadableDeliveryIsNeverABlankCell(t *testing.T) {
	doc := bladeDoc(t)
	page, ok := specBladePageOf(doc, mustID(t, bladeIDs.product), mustID(t, bladeIDs.feature),
		capabilityExpansion{Path: productPath(mustID(t, bladeIDs.product))},
		slice.DeliveryListing{}, assert.AnError)

	require.True(t, ok)
	require.NotNil(t, page.Feature)
	assert.Equal(t, pages.CapabilityMilestoneUnread, page.Feature.Milestone.Kind)
	assert.Equal(t, capabilityMilestoneUnreadMessage, page.Feature.Milestone.Message)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// keysOf is a set's members, for a failure message that says what WAS
// there rather than only what was wanted.
func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// attrValue is attrOfNode's non-failing form, for walking a subtree
// without a *testing.T in hand.
func attrValue(n *html.Node, key string) string {
	v, _ := attrOfNode(n, key)
	return v
}

// descendants is every element beneath n, n itself excluded.
func descendants(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == html.ElementNode {
			out = append(out, c)
		}
		out = append(out, descendants(c)...)
	}
	return out
}

// ancestorTag is the nearest ancestor-or-self with the given element name,
// or nil.
func ancestorTag(n *html.Node, tag string) *html.Node {
	for cur := n; cur != nil; cur = cur.Parent {
		if cur.Type == html.ElementNode && cur.Data == tag {
			return cur
		}
	}
	return nil
}

// innerHTML re-serialises one element, so an assertion about a chip's own
// <dd> can run the same parse-and-find helpers the whole-fragment
// assertions use.
func innerHTML(t *testing.T, n *html.Node) string {
	t.Helper()
	var b strings.Builder
	require.NoError(t, html.Render(&b, n))
	return b.String()
}