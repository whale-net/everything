// Coverage for the NEW DESIGN SESSION blade (FRs 44d7f1e2, 4304fe60): the
// panel that opens over the Design sessions list, its own URL, and the two
// modes that URL answers.
//
// Every assertion here is against served HTML rather than a view model, so
// a builder that fills a field correctly and a template that never renders
// it both fail. The blade is located by its region id (the swap target the
// list's action names) and its stable data-krill hooks, never by a daisyUI
// class, so restyling the blade cannot break it -- and the one structural
// claim, that the served fragment's ROOT is the swap target, is checked
// with an HTML parser rather than by matching markup bytes.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// bladeAnchor is the region id the list's action swaps into, spelled here
// as the raw marker the served HTML carries so this file states the claim
// independently of the constant under test.
const bladeAnchor = `id="` + bladeRegionID + `"`

// bladeRegionID is that region's id on its own, and bladeTarget is the
// same name in the form an hx-target takes it in.
const (
	bladeRegionID = "design-session-new-blade"
	bladeTarget   = "#" + bladeRegionID
)

// getHX is get with the HX-Request header htmx sets on every request it
// issues -- the half of a blade URL a browser never reaches.
func getHX(mux *http.ServeMux, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	mux.ServeHTTP(rec, req)
	return rec
}

// parseHTML parses a served body as a fragment, so an element can be found
// by its own id and attributes rather than by the bytes around it.
func parseHTML(t *testing.T, body string) []*html.Node {
	t.Helper()
	nodes, err := html.ParseFragment(strings.NewReader(body),
		&html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div})
	require.NoError(t, err)
	return nodes
}

// fragmentRoot is the served fragment's single top-level element.
//
// That is what htmx puts in place of the element named by hx-target, so a
// blade served without the region id on its ROOT deletes the region and
// leaves the operator with nothing to type into.
func fragmentRoot(t *testing.T, body string) *html.Node {
	t.Helper()
	var elems []*html.Node
	for _, n := range parseHTML(t, body) {
		if n.Type == html.ElementNode {
			elems = append(elems, n)
		}
	}
	require.Len(t, elems, 1, "a swap fragment must be exactly one element: %s", body)
	return elems[0]
}

// findByHook returns the first element carrying data-krill=<hook>, or nil.
// Tests locate every control by its stable hook rather than by a class or
// by its position in the markup.
func findByHook(root *html.Node, hook string) *html.Node {
	if root.Type == html.ElementNode {
		for _, a := range root.Attr {
			if a.Key == "data-krill" && a.Val == hook {
				return root
			}
		}
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findByHook(c, hook); found != nil {
			return found
		}
	}
	return nil
}

// elementWithHook is findByHook as a test assertion: it names the control
// under test and fails when the control is not rendered at all.
func elementWithHook(t *testing.T, body, hook string) *html.Node {
	t.Helper()
	for _, n := range parseHTML(t, body) {
		if found := findByHook(n, hook); found != nil {
			return found
		}
	}
	t.Fatalf("no element carries data-krill=%q", hook)
	return nil
}

// htmlAttr reads one attribute off a parsed element.
func htmlAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// htmlText is an element's visible text, its own and its descendants'.
func htmlText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			b.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}

// bladeSection is the open blade's own markup: from its region id to the
// end of the card it holds. Slicing keeps every assertion about "what the
// blade carries" off the list and the shell around it.
func bladeSection(t *testing.T, body string) string {
	t.Helper()
	i := strings.Index(body, bladeAnchor)
	require.NotEqual(t, -1, i, "body must carry the blade region %s", bladeAnchor)
	rest := body[i:]
	j := strings.Index(rest, "</article>")
	require.NotEqual(t, -1, j, "an open blade must contain its card")
	return rest[:j]
}

// assertInShellHTML fails unless body is a full shell page -- a status page
// is not a page an operator can navigate back out of.
func assertInShellHTML(t *testing.T, body string) {
	t.Helper()
	assert.Contains(t, body, "<!DOCTYPE html>", "the blade URL opened directly must serve the whole page in-shell")
	assert.Contains(t, body, "<nav")
	assert.Contains(t, body, "<main")
}

// TestDesignSessionNewBlade_DirectLoadRendersInShellOverTheList is
// FR 4304fe60's last sentence and FR 44d7f1e2's "a blade opens over the
// list": the blade URL opened directly is a whole page -- the shell, the
// session table the operator was reading, and the blade open over it.
func TestDesignSessionNewBlade_DirectLoadRendersInShellOverTheList(t *testing.T) {
	productID := uuid.New()
	rec := get(designReadMux(designListApp(productID)), designNewSessionBladePath(productID))
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, body)

	assertInShellHTML(t, body)
	assert.Contains(t, body, bladeAnchor, "the blade sits in its own region over the list")
	require.NotNil(t, findByHook(regionElement(t, body), "design-session-new-blade-body"),
		"the blade is open, not just its empty region")

	// The list underneath is the same list the list URL serves: a direct
	// load of the blade must not cost the operator the view they opened it
	// from.
	rows := parseDesignRows(t, body)
	require.Len(t, rows, len(designListSessions(productID)))
	assert.Equal(t, designSessionPath(productID, designListSessions(productID)[0].ID), rows[0].DetailPath)
}

// findByID returns the first element carrying id=<id>, or nil. The search
// is recursive because a full page puts the region inside the shell's own
// landmarks rather than at the fragment's top level.
func findByID(root *html.Node, id string) *html.Node {
	if root.Type == html.ElementNode && htmlAttr(root, "id") == id {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

// regionElement is the blade region on a served page, wherever the page's
// own landmarks put it.
func regionElement(t *testing.T, body string) *html.Node {
	t.Helper()
	for _, n := range parseHTML(t, body) {
		if found := findByID(n, bladeRegionID); found != nil {
			return found
		}
	}
	t.Fatalf("no element with id=%q", bladeRegionID)
	return nil
}

// TestDesignSessionNewBlade_HXAnswersTheBareRegionWhoseRootIsTheSwapTarget
// is the other mode: the list's action names the blade region as its
// hx-target, so the htmx half answers that region alone -- and the page
// under it is NOT re-rendered, because the rows the operator was reading
// are what they chose.
func TestDesignSessionNewBlade_HXAnswersTheBareRegionWhoseRootIsTheSwapTarget(t *testing.T) {
	productID := uuid.New()
	rec := getHX(designReadMux(designListApp(productID)), designNewSessionBladePath(productID))
	body := rec.Body.String()
	require.Equal(t, http.StatusOK, rec.Code, body)

	assert.NotContains(t, body, "<main", "the htmx half answers the region alone, with no shell chrome")
	assert.NotContains(t, body, "<!DOCTYPE html>")
	assert.NotContains(t, body, "<table", "the list under the blade must not be re-rendered into the fragment")

	root := fragmentRoot(t, body)
	assert.Equal(t, "div", root.Data)
	assert.Equal(t, bladeRegionID, htmlAttr(root, "id"),
		"the served fragment's ROOT must carry the id the opener's hx-target names")
	assert.NotNil(t, findFirstTag(root, "form"),
		"the fragment is the blade itself, not the empty region")
}

// TestDesignSessionList_ActionOpensTheBlade is the opener, doubled the way
// every mutating control here is: an href for the no-JS browser and an
// hx-get of that same URL for htmx, both naming the blade region.
//
// The anchor is the load-bearing half. A <button> would have no
// destination to fall back on, which is exactly the regression the
// no-JavaScript half exists to prevent.
func TestDesignSessionList_ActionOpensTheBlade(t *testing.T) {
	productID := uuid.New()
	bladePath := designNewSessionBladePath(productID)
	body := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID)).Body.String()

	action := elementWithHook(t, body, "design-sessions-new")
	assert.Equal(t, "a", action.Data, "the action is a link, so a no-JS click has somewhere to go")
	assert.Equal(t, bladePath, htmlAttr(action, "href"))
	assert.Equal(t, bladePath, htmlAttr(action, "hx-get"),
		"htmx fetches the blade's own URL -- the same address the href names")
	assert.Equal(t, bladeTarget, htmlAttr(action, "hx-target"))
	assert.Equal(t, "outerHTML", htmlAttr(action, "hx-swap"))
	assert.Equal(t, "true", htmlAttr(action, "hx-push-url"),
		"the blade is an address: a reload or a copied link reopens it")

	// The closed list still carries the region, so the hx-target resolves
	// whether or not a blade is showing.
	assert.NotNil(t, regionElement(t, body))
	assert.Nil(t, findByHook(regionElement(t, body), "design-session-new-blade-body"),
		"a blade nobody opened renders as its empty region")
}

// TestDesignSessionList_EmptyStateActionOpensTheSameBlade is the empty
// state's half: the one action it offers is that same link, so there is
// exactly one place to open a session from.
func TestDesignSessionList_EmptyStateActionOpensTheSameBlade(t *testing.T) {
	productID := uuid.New()
	app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
	body := get(designReadMux(app), designProductSessionsPath(productID)).Body.String()

	empty := elementWithHook(t, body, "design-sessions-empty")
	assert.NotEmpty(t, htmlText(empty), "an empty product renders the empty state, not an empty table")

	action := findFirstTag(empty, "a")
	require.NotNil(t, action, "the empty state offers one action")
	assert.Equal(t, designNewSessionBladePath(productID), htmlAttr(action, "href"),
		"the empty state's action is the blade URL")
}

// TestDesignSessionNewBlade_ProductIsReadOnlyFromTheURL is FR 44d7f1e2's
// "no product id field exists": the blade names the product the URL
// resolved and offers nothing to change it.
//
// The absence assertions are the point. A field the operator can type a
// product id into is how a session ends up opened against a product
// nobody was looking at.
func TestDesignSessionNewBlade_ProductIsReadOnlyFromTheURL(t *testing.T) {
	productID := uuid.New()
	body := get(designReadMux(designListApp(productID)), designNewSessionBladePath(productID)).Body.String()

	blade := bladeSection(t, body)
	assert.Equal(t, "krill", htmlText(elementWithHook(t, blade, "design-session-new-product")),
		"the blade shows the product the URL resolved, read-only")

	assert.NotContains(t, blade, "<input",
		"the blade carries no input at all: no product, no session id, no identity")
	for _, forbidden := range []string{"product_id", "session_id", "iss", "sub", "on_behalf_of"} {
		assert.NotContains(t, blade, `name="`+forbidden+`"`,
			"the browser supplies no "+forbidden+" field")
	}
}

// TestDesignSessionNewBlade_FormIsDoubledAndPostsOnlyTheSubmission pins
// the form's two halves and the one field it sends.
//
// The attribute checks are separate assertions rather than one markup
// match: attribute serialisation order is not a contract.
func TestDesignSessionNewBlade_FormIsDoubledAndPostsOnlyTheSubmission(t *testing.T) {
	productID := uuid.New()
	body := get(designReadMux(designListApp(productID)), designNewSessionBladePath(productID)).Body.String()
	blade := bladeSection(t, body)

	form := findFirstTag(elementWithHook(t, blade, "design-session-new-blade-body"), "form")
	require.NotNil(t, form, "the blade must carry its form")
	assert.Equal(t, "post", htmlAttr(form, "method"), "the no-JS half needs a real method and action")
	assert.Equal(t, designProductSessionsPath(productID), htmlAttr(form, "action"))
	assert.Equal(t, designProductSessionsPath(productID), htmlAttr(form, "hx-post"))
	assert.Equal(t, bladeTarget, htmlAttr(form, "hx-target"),
		"a refusal re-renders the whole blade, so the target is the region and not the form")
	assert.Equal(t, "outerHTML", htmlAttr(form, "hx-swap"))

	// One labelled textarea, named exactly what the write reads.
	assert.Contains(t, htmlText(form), "What should be designed?")
	assert.Equal(t, 1, strings.Count(blade, "<textarea"),
		"the blade's only field is its textarea")

	textarea := findFirstTag(form, "textarea")
	require.NotNil(t, textarea, "the blade must carry its submission textarea")
	assert.Equal(t, "opening_submission", htmlAttr(textarea, "name"))
	assert.Empty(t, htmlText(textarea), "a blade nobody typed into is empty")

	// The two controls: Cancel goes back to the list, and Open session
	// submits.
	cancel := elementWithHook(t, blade, "design-session-new-cancel")
	assert.Equal(t, "a", cancel.Data)
	assert.Equal(t, designProductSessionsPath(productID), htmlAttr(cancel, "href"),
		"Cancel returns to the list the blade was opened over")
	submit := elementWithHook(t, blade, "design-session-new-submit")
	assert.Equal(t, "button", submit.Data)
	assert.Equal(t, "submit", htmlAttr(submit, "type"))
	assert.Equal(t, "Open session", htmlText(submit))
}

// findFirstTag returns the first element with the given tag name in the
// subtree, or nil.
func findFirstTag(root *html.Node, tag string) *html.Node {
	if root.Type == html.ElementNode && root.Data == tag {
		return root
	}
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if found := findFirstTag(c, tag); found != nil {
			return found
		}
	}
	return nil
}

// TestDesignSessionNewBlade_GoesOneLevelDeep is the blade rule that keeps
// "back" a single answer: the blade's only link is its Cancel, so nothing
// inside it can open a second blade.
func TestDesignSessionNewBlade_GoesOneLevelDeep(t *testing.T) {
	productID := uuid.New()
	body := get(designReadMux(designListApp(productID)), designNewSessionBladePath(productID)).Body.String()
	blade := bladeSection(t, body)

	assert.Equal(t, 1, strings.Count(blade, "<a "), "the blade's only link is Cancel: %s", blade)
	assert.NotContains(t, blade, "hx-get", "nothing inside a blade fetches another blade")
}

// TestDesignSessionNewBlade_ErrorPaths covers what the blade URL refuses
// outright. A malformed product id is a 400 and nothing else -- there is
// no product to resolve a blade against.
func TestDesignSessionNewBlade_ErrorPaths(t *testing.T) {
	app := designListApp(uuid.New())
	rec := get(designReadMux(app), "/design/products/not-a-uuid/design-sessions/new")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
	assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
}
