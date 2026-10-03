package pages

// FR 73ec4525 at the component level: the raw spec slice sits behind a
// CLOSED disclosure labelled "Spec slice (raw JSON)", renders exactly once
// in the whole panel region, and a failed read alerts rather than showing an
// empty box.
//
// The assertions parse the rendered markup with a real HTML parser and read
// attributes and text off the tree. That is not ceremony: the two facts at
// stake -- the disclosure carrying no `open`, and the label reading exactly
// one string -- are both things a substring search reports wrongly (a
// substring cannot tell an attribute from the text that mentions it), and a
// hand-rolled tag scanner is known to misparse closing tags in this
// package's own helpers.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

// sliceJSONMarker is a token that appears in the fixture document and in
// nothing else on the page, so counting its occurrences counts how many
// times the DOCUMENT renders -- not how many times some other element
// happens to be on the page.
const sliceJSONMarker = "SLICE-DOCUMENT-MARKER-7f3a9c21"

// sliceTabPage is a Spec slice tab carrying document.
func sliceTabPage(document, sliceErr string) TaskDetailPage {
	return TaskDetailPage{Tab: TaskTabSlice, Tabs: tabsFor(TaskTabSlice), SliceJSON: document, SliceError: sliceErr}
}

// elementWithHook returns the first element carrying data-krill="hook".
func elementWithHook(t *testing.T, markup, hook string) *html.Node {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err)
	var found *html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if found != nil {
			return
		}
		if n.Type == html.ElementNode {
			for _, a := range n.Attr {
				if a.Key == "data-krill" && a.Val == hook {
					found = n
					return
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	require.NotNil(t, found, "no element carries data-krill=%q in:\n%s", hook, markup)
	return found
}

// elementCountWithHook is how many elements carry data-krill="hook".
func elementCountWithHook(t *testing.T, markup, hook string) int {
	t.Helper()
	doc, err := html.Parse(strings.NewReader(markup))
	require.NoError(t, err)
	n := 0
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.ElementNode {
			for _, a := range node.Attr {
				if a.Key == "data-krill" && a.Val == hook {
					n++
					break
				}
			}
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return n
}

// textOf is an element's own text, which for the summary is the label the
// operator reads.
func textOf(n *html.Node) string {
	var sb strings.Builder
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if node.Type == html.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(sb.String())
}

// firstTagWithin is the first descendant element of n whose tag is tag.
func firstTagWithin(t *testing.T, n *html.Node, tag string) *html.Node {
	t.Helper()
	var found *html.Node
	var walk func(*html.Node)
	walk = func(node *html.Node) {
		if found != nil {
			return
		}
		if node.Type == html.ElementNode && node.Data == tag {
			found = node
			return
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	require.NotNil(t, found, "no <%s> inside the node", tag)
	return found
}

// hasAttr is whether n carries attr at all -- the distinction between a
// disclosure that is closed (`open` absent) and one that is spelled closed
// (`open=""` would still open it).
func hasAttr(n *html.Node, attr string) bool {
	for _, a := range n.Attr {
		if a.Key == attr {
			return true
		}
	}
	return false
}

// TestSpecSliceDisclosureIsClosedAndExactlyLabelled (FR 73ec4525): the
// document sits behind a <details> whose summary reads exactly "Spec slice
// (raw JSON)", with no `open` attribute -- so the raw wire is something an
// operator asks for rather than the first thing the page says.
func TestSpecSliceDisclosureIsClosedAndExactlyLabelled(t *testing.T) {
	fragment := renderBody(t, TaskDetailTabs(sliceTabPage(`{"marker":"`+sliceJSONMarker+`"}`, "")))

	details := elementWithHook(t, fragment, "task-slice-disclosure")
	assert.Equal(t, "details", details.Data)
	assert.False(t, hasAttr(details, "open"),
		"the disclosure must be CLOSED by default: the raw document is the page's technical detail, not its first impression")

	summary := firstTagWithin(t, details, "summary")
	assert.Equal(t, "Spec slice (raw JSON)", textOf(summary),
		"the label is the operator-facing name of what is behind it, quoted from the design")

	// And the document is inside this disclosure, not beside it: the
	// summary is the first thing the box holds, the <pre> comes after it.
	var pre *html.Node
	for c := details.FirstChild; c != nil && pre == nil; c = c.NextSibling {
		for d := c.FirstChild; d != nil; d = d.NextSibling {
			if d.Type == html.ElementNode && d.Data == "pre" {
				pre = d
				break
			}
		}
	}
	require.NotNil(t, pre, "the document must sit INSIDE the disclosure:\n%s", fragment)
	assert.NotNil(t, summary.NextSibling, "the summary must precede the document")
}

// TestSpecSliceDocumentRendersExactlyOnceInThePanelRegion (FR 73ec4525):
// the document is behind that disclosure and nowhere else on the panel
// region -- counting a marker that only the document contains, so the count
// cannot be satisfied by the label, the tab strip or the chrome.
func TestSpecSliceDocumentRendersExactlyOnceInThePanelRegion(t *testing.T) {
	fragment := renderBody(t, TaskDetailTabs(sliceTabPage(`{"marker":"`+sliceJSONMarker+`"}`, "")))

	assert.Equal(t, 1, strings.Count(fragment, sliceJSONMarker),
		"the document must render exactly once in the slice panel")
	assert.Equal(t, 1, elementCountWithHook(t, fragment, "task-slice-json"),
		"exactly one element holds the document")
	assert.LessOrEqual(t, elementCountWithHook(t, fragment, "task-slice-disclosure"), 1,
		"one disclosure, not the document repeated behind several")
}

// TestSpecSliceDisclosureIsNotRenderedOnAnotherTab: the document is not in
// the strip, the header or any other panel -- a page that carried the raw
// wire in the tab strip and again in its panel would render it twice.
func TestSpecSliceDisclosureIsNotRenderedOnAnotherTab(t *testing.T) {
	for _, tab := range []string{TaskTabOverview, TaskTabNotes, TaskTabDependencies} {
		t.Run(tab, func(t *testing.T) {
			page := sliceTabPage(`{"marker":"`+sliceJSONMarker+`"}`, "")
			page.Tab = tab
			page.Tabs = tabsFor(tab)
			fragment := renderBody(t, TaskDetailTabs(page))
			assert.NotContains(t, fragment, sliceJSONMarker)
			assert.Equal(t, 0, elementCountWithHook(t, fragment, "task-slice-json"))
		})
	}
}

// TestSpecSliceReadFailureAlertsInsteadOfAnEmptyDisclosure (FR 73ec4525):
// a failed slice read alerts in the disclosure's place. An empty disclosure
// would read as "this task has no slice" -- a claim about the task that a
// failed read is not entitled to make.
func TestSpecSliceReadFailureAlertsInsteadOfAnEmptyDisclosure(t *testing.T) {
	fragment := renderBody(t, TaskDetailTabs(sliceTabPage("", "The spec slice could not be read. See the logs.")))

	assert.Contains(t, fragment, "The spec slice could not be read")
	assert.Equal(t, 0, elementCountWithHook(t, fragment, "task-slice-disclosure"),
		"a failed read must not leave a closed, empty disclosure behind -- that reads as 'no slice'")
	assert.Equal(t, 0, elementCountWithHook(t, fragment, "task-slice-json"))
}
