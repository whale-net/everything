package main

// FR 73ec4525 through the served page: the raw spec slice sits behind one
// closed "Spec slice (raw JSON)" disclosure, appears exactly once in the
// WHOLE rendered page at every tab, and is the very document get_task
// embeds -- slice.Querier.GetMilestoneDeliversSlice over the task's own
// milestone, verbatim.
//
// The two counts here are counts of a token that only the document carries,
// read off the served HTML rather than off a component in isolation. That
// is deliberate: the invariant the task states is about the PAGE ("the JSON
// must render nowhere else on the page, including the tab strip's Spec slice
// tab"), and a component-level assertion cannot see a second rendering that
// the strip, the header or the rail contributes.
//
// The disclosure attributes are read with a real HTML parser. A substring
// search cannot tell an attribute from the text that mentions it, which is
// the whole question when the question is whether `open` is present.

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	xhtml "golang.org/x/net/html"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/krill/work"
)

// taskDetailSliceDocument is a stand-in for what
// slice.Querier.GetMilestoneDeliversSlice returns: a schema_version
// envelope, the product, and one feature carrying a requirement, all with
// the as-of revision metadata the envelope carries verbatim. The marker is
// inside a requirement BODY, which is the one field long enough and
// specific enough that counting its occurrence counts renderings of the
// document and nothing else on the page.
func taskDetailSliceDocument(marker string) slice.Document {
	return slice.Document{
		SchemaVersion: slice.SchemaVersion,
		Product:       &slice.ProductEntity{Name: "krill", Vision: "the substrate"},
		FeatureSets:   []slice.FeatureSetEntity{{Name: "Task detail", Description: ptr("the detail page")}},
		Features: []slice.FeatureEntity{{
			Name:          "Raw slice disclosure",
			Description:   ptr("the raw JSON sits behind a closed disclosure"),
			DisplayNumber: 9,
		}},
		Requirements: []slice.RequirementEntity{{
			Kind: "FR",
			Name: "spec slice disclosure",
			Body: ptr("the document renders once: " + marker),
		}},
	}
}

// firstElementWithHook is the first element in markup carrying
// data-krill="hook".
func firstElementWithHook(t *testing.T, markup, hook string) *xhtml.Node {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(markup))
	require.NoError(t, err)
	var found *xhtml.Node
	var walk func(*xhtml.Node)
	walk = func(n *xhtml.Node) {
		if found != nil {
			return
		}
		if n.Type == xhtml.ElementNode {
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

// countElementsWithHook is how many elements carry data-krill="hook".
func countElementsWithHook(t *testing.T, markup, hook string) int {
	t.Helper()
	doc, err := xhtml.Parse(strings.NewReader(markup))
	require.NoError(t, err)
	n := 0
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.ElementNode {
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

// disclosureText is the unescaped text the disclosure's document element
// holds -- what an operator sees after opening the box.
func disclosureText(t *testing.T, markup, hook string) string {
	t.Helper()
	n := firstElementWithHook(t, markup, hook)
	var sb strings.Builder
	var walk func(*xhtml.Node)
	walk = func(node *xhtml.Node) {
		if node.Type == xhtml.TextNode {
			sb.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	text := sb.String()
	require.NotEmpty(t, strings.TrimSpace(text), "data-krill=%q holds no text:\n%s", hook, markup)
	return text
}

// TestTaskDetailServesTheSpecSliceBehindOneClosedDisclosure (FR 73ec4525):
// the whole page, at the Spec slice tab, carries exactly one disclosure,
// labelled exactly "Spec slice (raw JSON)", with no `open` on it.
func TestTaskDetailServesTheSpecSliceBehindOneClosedDisclosure(t *testing.T) {
	f := newDetailFixture(t)
	f.spec.doc = taskDetailSliceDocument("disclosure-marker")
	task := f.addOnMilepebble(store.Task{Title: "sliced"})

	code, html := f.getFullAt(task.ID.String(), "?tab="+pages.TaskTabSlice)
	require.Equal(t, 200, code, "body: %s", html)

	details := firstElementWithHook(t, html, "task-slice-disclosure")
	assert.Equal(t, "details", details.Data)
	for _, a := range details.Attr {
		assert.NotEqual(t, "open", a.Key,
			"the disclosure must be closed on arrival: `open` would put the raw wire in front of the operator unasked")
	}
	assert.Equal(t, 1, countElementsWithHook(t, html, "task-slice-disclosure"),
		"one disclosure on the page, so the JSON has exactly one home")

	var summary *xhtml.Node
	for c := details.FirstChild; c != nil && summary == nil; c = c.NextSibling {
		if c.Type == xhtml.ElementNode && c.Data == "summary" {
			summary = c
		}
	}
	require.NotNil(t, summary, "the disclosure must carry a summary:\n%s", html)
	assert.Contains(t, summaryText(summary), "Spec slice (raw JSON)")
}

// summaryText is a <summary>'s own text.
func summaryText(n *xhtml.Node) string {
	var sb strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type == xhtml.TextNode {
			sb.WriteString(c.Data)
		}
	}
	return strings.TrimSpace(sb.String())
}

// TestTaskDetailRendersTheSpecSliceDocumentExactlyOnceOnTheWholePage (FR
// 73ec4525) is the hard rule: at every tab, the document renders exactly
// once -- once on the tab that owns it, zero on the others -- and never
// inline in the tab strip beside the tab that hosts the disclosure.
//
// The count is of a marker that only the document carries, and it is
// asserted against the FULL page the browser receives, not the fragment or
// the component: a second rendering contributed by the strip, the header or
// the rail is exactly what a narrower assertion would miss.
func TestTaskDetailRendersTheSpecSliceDocumentExactlyOnceOnTheWholePage(t *testing.T) {
	const marker = "renders-exactly-once-marker"

	for _, tc := range []struct {
		tab  string
		want int
	}{
		{pages.TaskTabOverview, 0},
		{pages.TaskTabNotes, 0},
		{pages.TaskTabDependencies, 0},
		{pages.TaskTabSlice, 1},
	} {
		t.Run("tab="+tc.tab, func(t *testing.T) {
			f := newDetailFixture(t)
			f.spec.doc = taskDetailSliceDocument(marker)
			task := f.addOnMilepebble(store.Task{Title: "sliced"})

			query := ""
			if tc.tab != pages.TaskTabOverview {
				query = "?tab=" + tc.tab
			}
			code, html := f.getFullAt(task.ID.String(), query)
			require.Equal(t, 200, code, "body: %s", html)

			assert.Equal(t, tc.want, strings.Count(html, marker),
				"the raw document must render %d time(s) on the whole page at tab=%s", tc.want, tc.tab)

			// The strip is inside the swap region and the page with it, so
			// the strip is checked in its own right: a tab that carried the
			// document inline would put it beside the disclosure that owns
			// it.
			strip := tabStripOf(t, html)
			assert.NotContains(t, strip, marker,
				"the tab strip hosts the tab, not the document")

			if tc.want == 0 {
				assert.Equal(t, 0, countElementsWithHook(t, html, "task-slice-json"),
					"an unselected tab must not carry the document's element either")
			} else {
				assert.Equal(t, 1, countElementsWithHook(t, html, "task-slice-json"),
					"exactly one element holds the document")
			}
		})
	}
}

// TestTaskDetailSpecSliceFailedReadAlertsRatherThanAnEmptyDisclosure (FR
// 73ec4525): a failed read renders the inline alert in the disclosure's
// place -- never an empty box, which reads as "this task has no slice".
func TestTaskDetailSpecSliceFailedReadAlertsRatherThanAnEmptyDisclosure(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "sliced"})
	f.spec.err = errors.New("slice-boom")

	code, html := f.getFullAt(task.ID.String(), "?tab="+pages.TaskTabSlice)
	require.Equal(t, 200, code, "body: %s", html)

	assert.Contains(t, html, "The spec slice could not be read")
	assert.Equal(t, 0, countElementsWithHook(t, html, "task-slice-disclosure"),
		"a failed read must not leave an empty disclosure behind")
	assert.Equal(t, 0, countElementsWithHook(t, html, "task-slice-json"))
}

// TestTaskDetailSpecSliceJSONIsTheDocumentGetTaskEmbeds is the FR's parity
// clause, in the spirit of spec_parity_test.go: what the page shows behind
// the disclosure must be the SAME document the work-axis assembler embeds
// in get_task's payload -- not a re-derived, re-scoped or hand-assembled
// shape that happens to look similar.
//
// It is asserted in two steps that can each fail alone:
//
//  1. The read. The page must ask for the task's OWN milestone id, which is
//     the one work.Assemble passes to
//     slice.Querier.GetMilestoneDeliversSlice (payload.go) -- a page that
//     read a product slice, or the milestone rather than the task's
//     container, would show a document no agent ever received.
//  2. The bytes. The text inside the disclosure equals the pretty-printed
//     form of that document, AND equals the `slice` member of the
//     assembler's marshalled Payload -- so a re-serialisation, a field drop
//     or a hand-built Document in the UI fails here.
func TestTaskDetailSpecSliceJSONIsTheDocumentGetTaskEmbeds(t *testing.T) {
	const marker = "get-task-parity-marker"

	doc := taskDetailSliceDocument(marker)
	f := newDetailFixture(t)
	f.spec.doc = doc
	task := f.addOnMilepebble(store.Task{Title: "sliced"})

	code, html := f.getFullAt(task.ID.String(), "?tab="+pages.TaskTabSlice)
	require.Equal(t, 200, code, "body: %s", html)

	// 1. the read: the task's own container, asked for exactly once.
	assert.Equal(t, []uuid.UUID{task.MilestoneID}, f.spec.askedMilestoneIDs,
		"the page must read the slice for the task's own milestone -- the same one work.Assemble reads")

	rendered := disclosureText(t, html, "task-slice-json")
	require.Contains(t, rendered, marker,
		"the fixture document must really render here, or this test proves nothing")

	// 2. the bytes: the rendered text IS the document, pretty-printed.
	pretty, err := json.MarshalIndent(doc, "", "  ")
	require.NoError(t, err)
	assert.Equal(t, string(pretty), rendered,
		"the disclosure must hold the document pretty-printed, byte for byte")

	// ...and the very same document the assembler embeds in get_task's
	// Payload.Slice, compared through the payload's own wire bytes rather
	// than through the struct, so a JSON tag or omitempty difference in
	// work.Payload would show up here rather than silently re-shaping the
	// document.
	wire, err := json.Marshal(work.Payload{
		Task:  work.TaskView{ID: task.ID, MilestoneID: task.MilestoneID},
		Slice: doc,
	})
	require.NoError(t, err)
	var payload struct {
		Slice json.RawMessage `json:"slice"`
	}
	require.NoError(t, json.Unmarshal(wire, &payload))
	var embedded bytes.Buffer
	require.NoError(t, json.Indent(&embedded, payload.Slice, "", "  "))
	assert.Equal(t, string(pretty), embedded.String(),
		"the page's document must equal the one get_task embeds, for the same task")
}
