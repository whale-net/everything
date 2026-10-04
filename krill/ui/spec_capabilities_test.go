package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// The Capabilities tab's feature-set SECTIONS: the collapse markup, the
// "first one open" default, the URL-carried expansion, and the empty state.
//
// The expansion tests drive parseCapabilityExpansion over real requests
// rather than over hand-built values, because the properties that matter
// here -- an absent/empty/unparseable value falling back to the default, a
// shared link restoring a reload, two sections open at once -- are
// properties of what the URL says to the handler, and a test that builds a
// capabilityExpansion directly would keep passing if the parse were
// dropped from the request path.

// capabilitiesTestDoc is a slice with THREE feature sets, so "the first is
// open" is a claim about one of several rather than about the only one.
// Each feature set has a different requirement count, so a count that came
// from the wrong feature's rows is visible.
func capabilitiesTestDoc(t *testing.T) (slice.Document, uuid.UUID, []uuid.UUID) {
	t.Helper()
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	fsIDs := []uuid.UUID{
		mustID(t, "22222222-2222-2222-2222-222222222222"),
		mustID(t, "33333333-3333-3333-3333-333333333333"),
		mustID(t, "44444444-4444-4444-4444-444444444444"),
	}
	featIDs := []uuid.UUID{
		mustID(t, "55555555-5555-5555-5555-555555555555"),
		mustID(t, "66666666-6666-6666-6666-666666666666"),
		mustID(t, "77777777-7777-7777-7777-777777777777"),
	}
	doc := slice.Document{
		Product: &slice.ProductEntity{EntityRef: slice.EntityRef{ID: productID}, Name: "krill", Vision: "the substrate"},
		FeatureSets: []slice.FeatureSetEntity{
			{EntityRef: slice.EntityRef{ID: fsIDs[0]}, ProductID: productID, Name: "Spec axis", Description: ptr("feature sets about the spec")},
			{EntityRef: slice.EntityRef{ID: fsIDs[1]}, ProductID: productID, Name: "Delivery axis", Description: ptr("feature sets about shipping")},
			{EntityRef: slice.EntityRef{ID: fsIDs[2]}, ProductID: productID, Name: "Empty axis", Description: ptr("no features yet")},
		},
		Features: []slice.FeatureEntity{
			{EntityRef: slice.EntityRef{ID: featIDs[0]}, FeatureSetID: fsIDs[0], Name: "Scoped slice", Description: ptr("one query, four granularities"), DisplayNumber: 7},
			{EntityRef: slice.EntityRef{ID: featIDs[1]}, FeatureSetID: fsIDs[1], Name: "Delivery listing", Description: ptr("what is planned versus spec'd"), DisplayNumber: 3},
			{EntityRef: slice.EntityRef{ID: featIDs[2]}, FeatureSetID: fsIDs[1], Name: "Task ledger", Description: nil, DisplayNumber: 4},
		},
	}
	// One requirement under the first feature, two under the second, and a
	// stray one whose FeatureID names no feature above -- the case a naive
	// "count everything" implementation would fold into some other row.
	doc.Requirements = []slice.RequirementEntity{
		{EntityRef: slice.EntityRef{ID: mustID(t, "88888888-8888-8888-8888-888888888888")}, FeatureID: featIDs[0], Kind: "FR", Name: "product granularity", Body: ptr("returns every child")},
		{EntityRef: slice.EntityRef{ID: mustID(t, "99999999-9999-9999-9999-999999999999")}, FeatureID: featIDs[1], Kind: "FR", Name: "delivery rows", Body: nil},
		{EntityRef: slice.EntityRef{ID: mustID(t, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")}, FeatureID: featIDs[1], Kind: "NFR", Name: "stays cheap", Body: nil},
		{EntityRef: slice.EntityRef{ID: mustID(t, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")}, FeatureID: mustID(t, "cccccccc-cccc-cccc-cccc-cccccccccccc"), Kind: "FR", Name: "orphan", Body: nil},
	}
	return doc, productID, fsIDs
}

// failingSpecReader is a specReadClient whose ProductSlice read FAILS. It
// is the counterpart to specStubReader (spec_page_test.go), which succeeds
// with empty data -- between them they separate "this product has no
// feature sets" from "we could not read whether it has any", which the
// capability map must never confuse.
type failingSpecReader struct{ specStubReader }

func (failingSpecReader) ProductSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, errors.New("the spec store is unavailable")
}

func (failingSpecReader) Product(context.Context, uuid.UUID) (store.Product, error) {
	return store.Product{}, errors.New("the spec store is unavailable")
}

// specFailureMux mounts the capabilities handler against the failing reader,
// at the path routes.go really registers.
func specFailureMux() *http.ServeMux {
	app := &App{spec: failingSpecReader{}, scopes: chromeScopes{}, tasks: chromeTaskCounter{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+specProductPath, app.handleCapabilityMap)
	return mux
}

// capabilityRequest builds a GET for the capabilities tab with the given
// raw query, so a test can state the URL a shared link would carry.
func capabilityRequest(t *testing.T, productID uuid.UUID, rawQuery string) *http.Request {
	t.Helper()
	target := productPath(productID)
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	return httptest.NewRequest(http.MethodGet, target, nil)
}

// renderCapabilities renders the capability map the handler renders for this
// query -- builder AND component together, so the tests cover the rendered
// markup rather than the view model alone.
func renderCapabilities(t *testing.T, doc slice.Document, productID uuid.UUID, rawQuery string) string {
	t.Helper()
	expansion := parseCapabilityExpansion(capabilityRequest(t, productID, rawQuery))
	return mustRenderComponent(pages.CapabilityMap(capabilityPageOf(doc, productID, expansion)))
}

// openFeatureSetIDs returns the feature-set ids whose section rendered in
// the open state, read off the markup's data-krill-feature-set-open hook.
// It reads the RENDER rather than the view model so a template that dropped
// or inverted the hook fails here.
func openFeatureSetIDs(t *testing.T, html string) []string {
	t.Helper()
	var out []string
	rest := html
	for {
		idx := strings.Index(rest, `data-krill-feature-set-id="`)
		if idx < 0 {
			return out
		}
		rest = rest[idx+len(`data-krill-feature-set-id="`):]
		end := strings.Index(rest, `"`)
		if end < 0 {
			t.Fatalf("unterminated data-krill-feature-set-id in: %s", html)
		}
		id := rest[:end]
		rest = rest[end:]
		// The hook sits on the section root, followed by its own open/closed
		// marker; a feature row carrying the same id is not a section.
		if marker := strings.Index(rest, `data-krill-feature-set-open="true"`); marker >= 0 &&
			marker < strings.Index(rest, `data-krill-feature-set-id="`) {
			out = append(out, id)
		}
	}
}

// TestCapabilitiesFirstSectionIsOpenAndTheRestAreClosed pins FR 18afc5a8's
// default on the bare URL: the FIRST feature set is open and every other
// one is shut. A page that opened all of them would defeat the point of
// collapsible sections; a page that opened none would make the first thing
// an operator sees a list of headings.
func TestCapabilitiesFirstSectionIsOpenAndTheRestAreClosed(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, "")

	open := openFeatureSetIDs(t, html)
	if len(open) != 1 {
		t.Fatalf("the default URL opened %d sections (%v), want exactly the first", len(open), open)
	}
	if open[0] != fsIDs[0].String() {
		t.Errorf("the default URL opened %s, want the first feature set %s", open[0], fsIDs[0])
	}
}

// TestCapabilitiesSectionsUseCollapseMarkup pins the daisyUI collapse
// shape: each section is a `collapse collapse-arrow` box, and the OPEN one
// additionally carries `collapse-open`. The open marker and the expander's
// aria-expanded are both read off the same Expanded flag, so this asserts
// the two agree rather than restating the class list twice.
func TestCapabilitiesSectionsUseCollapseMarkup(t *testing.T) {
	doc, productID, _ := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, "")

	if got := strings.Count(html, `class="collapse collapse-arrow bg-base-100 border border-base-300`); got != 3 {
		t.Errorf("rendered %d collapse sections, want one per feature set (3)", got)
	}
	if got := strings.Count(html, "collapse-open"); got != 1 {
		t.Errorf("rendered %d collapse-open sections, want exactly the first on the default URL", got)
	}
	// The expander's aria-expanded must agree with what rendered: exactly one
	// section claims to be expanded, and it is the one carrying the marker.
	if got := strings.Count(html, `aria-expanded="true"`); got != 1 {
		t.Errorf("rendered %d aria-expanded=true expanders, want 1", got)
	}
}

// TestCapabilitiesExpandHrefOpensANamedSectionAndPreservesTheTab pins the
// expand link on a CLOSED section: it points at this tab's own path (not
// another tab, and not the bare page) carrying that section's id ALONGSIDE
// the sections already open, so an operator following it lands on the same
// tab with that section open and the siblings they already had open still
// open.
func TestCapabilitiesExpandHrefOpensANamedSectionAndPreservesTheTab(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, "")

	// The default has the first section open, so expanding the second adds
	// to it rather than trading one for the other.
	want := productPath(productID) + "?" + capabilityExpansionQueryParam + "=" +
		fsIDs[0].String() + "," + fsIDs[1].String()
	if !strings.Contains(html, `href="`+want+`"`) {
		t.Errorf("no expand link to %s; the closed section's href must open it on the same tab without shutting the open one", want)
	}
	// An expand href that pointed at another tab would silently switch tabs.
	for _, otherTab := range []string{decisionsPath(productID), personasPath(productID), nonGoalsPath(productID)} {
		if strings.Contains(html, `href="`+otherTab) {
			t.Errorf("an expander points at %s; expansion must stay on the Capabilities tab", otherTab)
		}
	}
}

// TestCapabilitiesTwoSectionsCanBeOpenAtOnce pins the property that makes
// the expansion a SET: opening a second section does not shut the first.
// Comparing one feature set against another is why an operator opens a
// second, so an expander that traded one for the other would make the
// comparison impossible.
func TestCapabilitiesTwoSectionsCanBeOpenAtOnce(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, capabilityExpansionQueryParam+"="+fsIDs[0].String()+","+fsIDs[1].String())

	open := openFeatureSetIDs(t, html)
	if len(open) != 2 {
		t.Fatalf("a URL naming two sections opened %d (%v), want 2", len(open), open)
	}
	want := map[string]bool{fsIDs[0].String(): true, fsIDs[1].String(): true}
	for _, id := range open {
		if !want[id] {
			t.Errorf("opened %s, which the URL did not name", id)
		}
	}
}

// TestCapabilitiesUnparseableOpenValueFallsBackToTheDefault pins the
// milestone page's "absent, empty or unparseable means the default, never an
// error" rule. A hand-edited or stale ?open= is a reason to show the
// ordinary page -- never a 400, and never a map with every section shut,
// which would read as a broken page.
func TestCapabilitiesUnparseableOpenValueFallsBackToTheDefault(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)

	for _, raw := range []string{
		"",                                  // absent
		capabilityExpansionQueryParam + "=", // present but empty
		capabilityExpansionQueryParam + "=not-a-uuid", // unparseable
		capabilityExpansionQueryParam + "=,",          // separators with nothing between them
	} {
		t.Run(raw, func(t *testing.T) {
			html := renderCapabilities(t, doc, productID, raw)
			open := openFeatureSetIDs(t, html)
			if len(open) != 1 || open[0] != fsIDs[0].String() {
				t.Errorf("?%s opened %v, want the default: only the first section (%s)", raw, open, fsIDs[0])
			}
		})
	}
}

// TestCapabilitiesCollapseHrefKeepsEverythingElseOpen pins the collapse
// link's two halves. It must drop the section it was pressed on, and it
// must NOT drop the sibling sections the operator also had open -- an
// expander that re-spelled the set wrongly would quietly close a section
// the operator never touched.
func TestCapabilitiesCollapseHrefKeepsEverythingElseOpen(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, capabilityExpansionQueryParam+"="+fsIDs[0].String()+","+fsIDs[1].String())

	// Collapsing the FIRST of the two leaves the second open.
	firstAlone := productPath(productID) + "?" + capabilityExpansionQueryParam + "=" + fsIDs[1].String()
	if !strings.Contains(html, `href="`+firstAlone+`"`) {
		t.Errorf("no collapse link leaving only %s open; collapsing one section must not shut its sibling", fsIDs[1])
	}
}

// TestCapabilitiesCollapsingTheDefaultSectionIsExpressible pins the case a
// "just drop the parameter" collapse href gets wrong: the default state has
// the FIRST section open, so a collapse link that dropped ?open= would
// re-render that very section open. The link must therefore spell out the
// explicit "nothing open" value, and following it must actually leave every
// section shut.
func TestCapabilitiesCollapsingTheDefaultSectionIsExpressible(t *testing.T) {
	doc, productID, _ := capabilitiesTestDoc(t)

	html := renderCapabilities(t, doc, productID, "")
	closed := productPath(productID) + "?" + capabilityExpansionQueryParam + "=" + capabilityExpansionNone
	if !strings.Contains(html, `href="`+closed+`"`) {
		t.Fatalf("the open default section's collapse link is not %s; dropping the parameter would re-open it", closed)
	}
	// And the link has to work: following it must shut every section.
	after := renderCapabilities(t, doc, productID, capabilityExpansionQueryParam+"="+capabilityExpansionNone)
	if open := openFeatureSetIDs(t, after); len(open) != 0 {
		t.Errorf("?open=%s left %v open, want every section shut", capabilityExpansionNone, open)
	}
	// The feature content itself must still be in the markup: a closed
	// section is hidden by CSS, not deleted from the page, so a reload with
	// JavaScript off still shows it.
	if !strings.Contains(after, "Scoped slice") {
		t.Errorf("the collapsed page dropped the section's content; a closed section is hidden, not removed")
	}
}

// TestCapabilitiesExpandHrefIsStable pins that one open set always yields
// one address. An href that reordered its ids between renders would push a
// different history entry for the same state, so an operator pressing Back
// would land somewhere they never asked for.
func TestCapabilitiesExpandHrefIsStable(t *testing.T) {
	doc, productID, fsIDs := capabilitiesTestDoc(t)

	forward := renderCapabilities(t, doc, productID,
		capabilityExpansionQueryParam+"="+fsIDs[0].String()+","+fsIDs[1].String())
	reverse := renderCapabilities(t, doc, productID,
		capabilityExpansionQueryParam+"="+fsIDs[1].String()+","+fsIDs[0].String())

	// Both orderings name the same set, so the third section's expand href
	// must be the same address in both -- spelled in id order.
	sorted := productPath(productID) + "?" + capabilityExpansionQueryParam + "=" +
		fsIDs[0].String() + "," + fsIDs[1].String() + "," + fsIDs[2].String()
	for name, html := range map[string]string{"forward": forward, "reverse": reverse} {
		if !strings.Contains(html, `href="`+sorted+`"`) {
			t.Errorf("%s render has no expand href for the set in id order: %s", name, sorted)
		}
	}
}

// TestCapabilitiesRequirementCountComesFromTheSlice pins the one-read
// rule: the Requirements column is the count of the SAME slice rows the
// nested requirement list renders, never a second read. A second read is a
// second number that can disagree with the list the operator is about to
// open, and the count here is that list's length -- including a requirement
// whose feature is not on the page, which must NOT be folded into some
// other feature's total.
func TestCapabilitiesRequirementCountComesFromTheSlice(t *testing.T) {
	doc, productID, _ := capabilitiesTestDoc(t)
	page := capabilityPageOf(doc, productID, parseCapabilityExpansion(capabilityRequest(t, productID, "")))

	want := map[string]int{"Scoped slice": 1, "Delivery listing": 2, "Task ledger": 0}
	seen := 0
	for _, fs := range page.FeatureSets {
		for _, f := range fs.Features {
			expect, ok := want[f.Name]
			if !ok {
				t.Fatalf("unexpected feature %q in the built page", f.Name)
			}
			seen++
			if f.RequirementCount != expect {
				t.Errorf("%s: RequirementCount = %d, want %d (the slice's own rows for it)", f.Name, f.RequirementCount, expect)
			}
			if f.RequirementCount != len(f.Requirements) {
				t.Errorf("%s: RequirementCount %d disagrees with the %d requirements rendered beneath it",
					f.Name, f.RequirementCount, len(f.Requirements))
			}
		}
	}
	if seen != len(want) {
		t.Errorf("checked %d features, want %d", seen, len(want))
	}

	// The rendered markup must show those numbers, in the cell the table
	// header names.
	html := mustRenderComponent(pages.CapabilityMap(page))
	if !strings.Contains(html, `<th class="text-right">Requirements</th>`) {
		t.Errorf("the table has no Requirements header")
	}
	if !strings.Contains(html, `data-krill="feature-requirement-count">2</td>`) {
		t.Errorf("the Delivery listing row does not render its requirement count of 2 in the Requirements cell")
	}
	if !strings.Contains(html, `data-krill="feature-requirement-count">0</td>`) {
		t.Errorf("a feature with no requirements does not render a 0 in its Requirements cell")
	}
}

// TestCapabilitiesFeatureCellRendersNumberNameAndDescription pins the
// Feature column's contents: the display number and name together as the
// "Cn -- name" citation shape delivery_page.go already renders, plus the
// feature's description as a subtitle.
func TestCapabilitiesFeatureCellRendersNumberNameAndDescription(t *testing.T) {
	doc, productID, _ := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, "")

	for _, want := range []string{
		"C7 — Scoped slice",
		"C3 — Delivery listing",
		"one query, four granularities",
		"what is planned versus spec'd",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the feature table missing %q", want)
		}
	}
}

// TestCapabilitiesHeadingCarriesNameCountAndDescription pins the section
// heading's three parts (FR 18afc5a8): the feature set's name, its feature
// count as a badge, and its description through the same renderMarkdown the
// page already used.
func TestCapabilitiesHeadingCarriesNameCountAndDescription(t *testing.T) {
	doc, productID, _ := capabilitiesTestDoc(t)
	html := renderCapabilities(t, doc, productID, "")

	for _, want := range []string{
		`data-krill="feature-set-name">Spec axis`,
		`data-krill="feature-set-count">1 features</span>`,
		`data-krill="feature-set-count">2 features</span>`,
		"feature sets about the spec",
	} {
		if !strings.Contains(html, want) {
			t.Errorf("the section heading missing %q", want)
		}
	}
}

// TestCapabilitiesEmptyStateOnlyOnASuccessfulEmptyRead pins the rule that
// separates the two claims. A read that SUCCEEDED and found no feature sets
// renders the designed empty state; a read that FAILED must not, because
// "this product has no feature sets" is a claim about the product that a
// failed read is not entitled to make -- the operator would read a broken
// store as a product with no spec.
//
// Both paths go through the real handler, so this covers the wiring as well
// as the two components.
func TestCapabilitiesEmptyStateOnlyOnASuccessfulEmptyRead(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")
	const emptyState = "No feature sets yet."

	t.Run("a successful empty read renders the designed empty state", func(t *testing.T) {
		rec := fetch(t, specModeMux(), productPath(productID))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET the empty product = %d, want 200", rec.Code)
		}
		if body := rec.Body.String(); !strings.Contains(body, emptyState) {
			t.Errorf("a read that succeeded and found no feature sets did not render %q", emptyState)
		}
	})

	t.Run("a failed read never renders the empty state", func(t *testing.T) {
		mux := specFailureMux()
		rec := fetch(t, mux, productPath(productID))
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("GET a product whose spec read failed = %d, want 500", rec.Code)
		}
		body := rec.Body.String()
		if strings.Contains(body, emptyState) {
			t.Errorf("a FAILED read rendered %q; that is a claim about the product nobody could make", emptyState)
		}
		if !strings.Contains(body, "Could not load the spec") {
			t.Errorf("a failed read did not render the alert path; body: %s", body)
		}
	})
}
