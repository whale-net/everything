package main

// FR df5bffd1's tab mechanism at the handler level: the four URLs that
// ARE the four tab addresses, the strip they render, and the three
// HX-Target modes that route serves them in.
//
// The shape assertions parse the served fragment with a real HTML parser
// rather than counting tags, because "exactly one top-level element, and
// it is the swap target" is the invariant whose absence splices a
// duplicate strip into the page on every click -- and it is exactly the
// thing a substring check gets wrong.

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/whale-net/everything/krill/ui/pages"
)

// specTabURLs are the four URLs the Spec page is served at, paired with
// the tab each one must resolve to. Spelled as literals rather than
// derived from the path builders, so a builder that drifts fails here
// rather than agreeing with itself.
var specTabURLs = []struct {
	path string
	tab  string
}{
	{"/spec/products/{id}", pages.SpecTabCapabilities},
	{"/spec/products/{id}" + decisionsSuffix, pages.SpecTabDecisions},
	{"/spec/products/{id}" + personasSuffix, pages.SpecTabPersonas},
	{"/spec/products/{id}" + nonGoalsSuffix, pages.SpecTabNonGoals},
}

// specTabHref expands one of specTabURLs' paths against id.
func specTabHref(t *testing.T, pattern string, id string) string {
	t.Helper()
	return strings.ReplaceAll(pattern, "{id}", id)
}

// specTabRequest issues one request against the four spec handlers the
// way the named caller would: a tab swap names the swap region, the
// panel's Refresh button names the panel's content region.
func specTabRequest(t *testing.T, path, hxTarget string) *httptest.ResponseRecorder {
	t.Helper()
	mux := specModeMux()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("HX-Request", "true")
	if hxTarget != "" {
		req.Header.Set("HX-Target", hxTarget)
	}
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSpecTabSwapFragmentIsTheSwapRegion pins the invariant that makes a
// tab click repeatable: the fragment served to a tab names the swap
// region, and that region is the fragment's ONLY top-level element.
//
// A second root would be spliced in beside it on every click. A root that
// is not the swap target is worse still: htmx's outerHTML deletes what
// it replaced, so the next click has no target and never reaches the
// server.
func TestSpecTabSwapFragmentIsTheSwapRegion(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"

	for _, u := range specTabURLs {
		path := specTabHref(t, u.path, id)
		body := specTabRequest(t, path, pages.SpecPanelAnchor).Body.String()

		root := topLevelElement(t, body)
		assert.Equal(t, pages.SpecPanelAnchor, attrOf(t, root, "id"),
			"GET %s (tab swap): the fragment's root must be the region the tab replaced", path)
		assert.Equal(t, "spec-panel", attrOf(t, root, "data-krill"),
			"GET %s (tab swap): the root must carry the spec-panel hook", path)
	}
}

// TestSpecTabLinksAreRealNavigation pins the whole tab link: a real href
// on its own path AND an hx-get of that same path, targeting the swap
// region and pushing that same URL. The href is what a reload, a shared
// link, the browser's Back button and a no-JS click all follow, so a tab
// whose href and hx-get disagree can put the address bar somewhere the
// page never rendered.
func TestSpecTabLinksAreRealNavigation(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	productID := mustID(t, id)
	body := specTabRequest(t, productPath(productID), pages.SpecPanelAnchor).Body.String()

	tabs := elementsWithHook(t, body, "spec-tab")
	require.Len(t, tabs, len(specTabURLs), "the strip must carry one tab per spec URL")

	seen := map[string]bool{}
	for _, tab := range tabs {
		key := attrOf(t, tab, "data-krill-spec-tab")
		href := attrOf(t, tab, "href")

		assert.Equal(t, "tab", attrOf(t, tab, "role"), "tab %s must carry role=tab", key)
		assert.Equal(t, href, attrOf(t, tab, "hx-get"),
			"tab %s: hx-get must be the tab's own href, or the address bar and the page can disagree", key)
		assert.Equal(t, "#"+pages.SpecPanelAnchor, attrOf(t, tab, "hx-target"),
			"tab %s must target the swap region", key)
		assert.Equal(t, "outerHTML", attrOf(t, tab, "hx-swap"),
			"tab %s must replace the region, not nest inside it", key)
		assert.Equal(t, "true", attrOf(t, tab, "hx-push-url"),
			"tab %s must push its own URL so a reload lands on this tab", key)
		assert.Equal(t, pages.SpecPanelBodyAnchor, attrOf(t, tab, "aria-controls"),
			"tab %s must name the panel it controls", key)

		seen[href] = true
	}
	// Spelled out rather than iterated from the list above: a test that
	// reads its expectations off the thing it is checking passes even
	// when a tab is deleted.
	for _, u := range specTabURLs {
		want := specTabHref(t, u.path, id)
		assert.True(t, seen[want], "the strip carries no tab linking to %s", want)
	}

	strip := elementsWithHook(t, body, "spec-tabs")
	require.Len(t, strip, 1, "the page must carry exactly one tablist")
	assert.Equal(t, "tablist", attrOf(t, strip[0], "role"))
}

// TestSpecURLsLandOnTheirOwnTab pins that each of the four URLs answers
// 200 with ITS tab marked, and with the other three present but not
// marked -- the property that makes a shared /decisions link open the
// Decisions tab rather than Capabilities.
func TestSpecURLsLandOnTheirOwnTab(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")

	for _, u := range specTabURLs {
		path := specTabHref(t, u.path, productID.String())
		rec := specTabRequest(t, path, pages.SpecPanelAnchor)
		require.Equal(t, http.StatusOK, rec.Code, "GET %s = %d, want 200", path, rec.Code)

		body := rec.Body.String()
		for _, tab := range elementsWithHook(t, body, "spec-tab") {
			key := attrOf(t, tab, "data-krill-spec-tab")
			selected := attrOf(t, tab, "aria-selected")
			active := tabIsActive(t, tab)

			if key == u.tab {
				assert.Equal(t, "true", selected, "GET %s: tab %s must be the selected one", path, key)
				assert.True(t, active, "GET %s: tab %s must carry the active marking", path, key)
				continue
			}
			assert.Equal(t, "false", selected,
				"GET %s: tab %s must not claim to be selected", path, key)
			assert.False(t, active, "GET %s: tab %s must not carry the active marking", path, key)
		}
	}
}

// TestSpecRefreshStaysOnItsTab pins the second mode. The panel's Refresh
// button targets the panel's CONTENT region, not the swap region, so the
// answer is that section alone -- strip untouched -- and because the
// button re-requests its own tab's path, a Refresh taken on Decisions
// leaves the operator on Decisions rather than dropping back to
// Capabilities.
//
// Each panel's own anchor is the one named, because a panel whose button
// and answer disagreed would delete the swap target and freeze the page
// on the next press.
func TestSpecRefreshStaysOnItsTab(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")

	for _, anchor := range []string{
		pages.CapabilityMapAnchor,
		pages.DecisionsAnchor,
		pages.PersonasAnchor,
		pages.NonGoalsAnchor,
	} {
		path := refreshPathFor(t, anchor, productID)

		rec := specTabRequest(t, path, anchor)
		require.Equal(t, http.StatusOK, rec.Code, "refresh on %s = %d, want 200", path, rec.Code)
		body := rec.Body.String()

		root := topLevelElement(t, body)
		assert.Equal(t, anchor, attrOf(t, root, "id"),
			"GET %s (refresh): the answer must be the region the button replaced", path)
		assert.Empty(t, elementsWithHook(t, body, "spec-tabs"),
			"GET %s (refresh): a refresh must not re-render the strip into the panel", path)
	}
}

// refreshPathFor is the tab whose panel renders under anchor -- the path
// that panel's Refresh button re-requests.
func refreshPathFor(t *testing.T, anchor string, productID uuid.UUID) string {
	t.Helper()
	switch anchor {
	case pages.CapabilityMapAnchor:
		return productPath(productID)
	case pages.DecisionsAnchor:
		return decisionsPath(productID)
	case pages.PersonasAnchor:
		return personasPath(productID)
	case pages.NonGoalsAnchor:
		return nonGoalsPath(productID)
	}
	t.Fatalf("no spec panel renders under anchor %q", anchor)
	return ""
}

// TestSpecTabSwapIsRepeatable proves the fragment shape holds on every
// request, not just the first: an operator clicking through the tabs
// asks the same route for the same shape four times, and the fourth
// answer is what duplicates a strip that the first three did not.
func TestSpecTabSwapIsRepeatable(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")

	for round := 0; round < 3; round++ {
		for _, u := range specTabURLs {
			path := specTabHref(t, u.path, productID.String())
			body := specTabRequest(t, path, pages.SpecPanelAnchor).Body.String()

			require.Len(t, topLevelTagsOf(t, body), 1,
				"round %d, GET %s: a fragment with more than one root splices itself in again", round, path)
			require.Len(t, elementsWithHook(t, body, "spec-tabs"), 1,
				"round %d, GET %s: the answer must carry exactly one strip", round, path)
		}
	}
}

// TestSpecTabOfResolvesUnknownValuesToCapabilities pins the whole of the
// unknown-tab policy. The tab is read off whatever path arrived, so a
// hand-edited, stale or mistyped suffix reaches this resolver as readily
// as a copied one, and it must answer a tab rather than an error.
func TestSpecTabOfResolvesUnknownValuesToCapabilities(t *testing.T) {
	id := "11111111-1111-1111-1111-111111111111"
	base := "/spec/products/" + id

	for _, c := range []struct{ path, want string }{
		{base, pages.SpecTabCapabilities},
		{base + decisionsSuffix, pages.SpecTabDecisions},
		{base + personasSuffix, pages.SpecTabPersonas},
		{base + nonGoalsSuffix, pages.SpecTabNonGoals},
		// A query string is not part of the tab.
		{base + "?tab=" + pages.SpecTabDecisions, pages.SpecTabCapabilities},
		// Suffixes this page does not have.
		{base + "/roadmap", pages.SpecTabCapabilities},
		{base + "/Decisions", pages.SpecTabCapabilities},
		{base + "/", pages.SpecTabCapabilities},
		{base + decisionsSuffix + decisionsSuffix, pages.SpecTabDecisions},
		{"/spec/products", pages.SpecTabCapabilities},
	} {
		req := httptest.NewRequest(http.MethodGet, c.path, nil)
		assert.Equal(t, c.want, specTabOf(req), "specTabOf(%q)", c.path)
	}
}

// TestSpecTabStripCarriesNoAttentionContent pins the read-only half of
// the requirement: nothing in the served spec body escalates, and nothing
// in it can write. The sidebar's Needs-attention badge is shell chrome
// and lives outside the region this slices.
func TestSpecTabStripCarriesNoAttentionContent(t *testing.T) {
	productID := mustID(t, "11111111-1111-1111-1111-111111111111")

	for _, u := range specTabURLs {
		path := specTabHref(t, u.path, productID.String())
		body := specTabRequest(t, path, pages.SpecPanelAnchor).Body.String()

		for _, forbidden := range []string{"<form", "hx-post", "hx-put", "hx-delete"} {
			assert.NotContains(t, strings.ToLower(body), forbidden,
				"GET %s: the spec body is read-only and carries %q", path, forbidden)
		}
		for _, forbidden := range []string{"Needs attention", "escalated", "Escalate"} {
			assert.NotContains(t, body, forbidden,
				"GET %s: the spec body carries attention content (%q)", path, forbidden)
		}
	}
}

// TestSidebarSpecLinksLandOnTheirOwnTab pins the other half of the URL
// contract from the nav's side: the sidebar's four Spec links ARE the
// four tab addresses, so each one lands on its matching tab without the
// nav knowing anything about tabs.
func TestSidebarSpecLinksLandOnTheirOwnTab(t *testing.T) {
	mux := navMux(t)
	pid := navProductID

	body := fetchPage(t, mux, productPath(pid)).Body.String()
	region := primaryNavRegion(body)
	if region == "" {
		t.Fatalf("GET %s rendered no %q region to check", productPath(pid), `data-krill="primary-nav"`)
	}

	for _, href := range []string{
		productPath(pid),
		decisionsPath(pid),
		personasPath(pid),
		nonGoalsPath(pid),
	} {
		if !strings.Contains(region, `href="`+href+`"`) {
			t.Errorf("the sidebar has no Spec link to %s; it would not land on that tab", href)
		}
	}
}

// attrOf reads one attribute off a parsed element, failing the test when
// it is absent -- so a renamed or dropped attribute reads as a missing
// value rather than as an empty string that happens to match.
func attrOf(t *testing.T, n *html.Node, key string) string {
	t.Helper()
	v, ok := attrOfNode(n, key)
	if !ok {
		t.Fatalf("element <%s> has no %s attribute", n.Data, key)
	}
	return v
}

// tabIsActive reports whether a parsed tab carries the active marking.
//
// It matches a whole class token off the parsed attribute rather than a
// substring of the serialised tag: "tab" is a prefix of "tab-active", so
// a substring test cannot tell the active tab from an inactive one.
func tabIsActive(t *testing.T, tab *html.Node) bool {
	t.Helper()
	for _, c := range strings.Fields(attrOf(t, tab, "class")) {
		if c == "tab-active" {
			return true
		}
	}
	return false
}