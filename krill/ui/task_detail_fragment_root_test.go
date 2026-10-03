// The task detail's served fragment must be exactly its swap target: the
// Refresh button does hx-get=<this page> hx-target="#krill-task-detail"
// hx-swap="outerHTML", so whatever top-level nodes the handler's htmx branch
// serves are all spliced into that one region. A second top-level node
// duplicates the header and the button on every click, without bound.
//
// The assertion that already exists for this (pages.TestSpecFragmentIsExactly
// ItsSwapTarget) cannot be trusted: its helper, pages.topLevelElements,
// misparses closing tags. For "</section>" it takes the tag name as the text
// between "<" and the first of " >/", which is the empty string, so the tag
// does not read as a closing tag at all -- it is counted as an opening tag and
// depth is incremented instead of decremented. Depth therefore only ever
// grows, every node after the first lands at depth >= 1, and the helper
// reports a single top-level element for a fragment with any number of them.
// The bug is pre-existing and belongs to another task; this file carries its
// own parser so the task detail's fragment shape is decided by something that
// actually counts.

package main

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// fragmentVoidElements are the HTML elements that never have a closing tag, so
// a parser that pushed them onto its stack would never pop them and would
// report every later element as nested.
var fragmentVoidElements = map[string]bool{
	"area": true, "base": true, "br": true, "col": true, "embed": true,
	"hr": true, "img": true, "input": true, "link": true, "meta": true,
	"param": true, "source": true, "track": true, "wbr": true,
}

// fragmentTopLevelElements returns the tag name of every top-level element in
// an HTML fragment, in document order, ignoring text, comments and doctypes.
//
// It is a stack machine over the tag stream: an opening tag pushes (unless the
// element is void or self-closing), a closing tag pops, and a tag is
// top-level exactly when the stack is empty as it opens.
func fragmentTopLevelElements(fragment string) []string {
	var top []string
	var stack []string
	for i := 0; i < len(fragment); {
		lt := strings.IndexByte(fragment[i:], '<')
		if lt < 0 {
			break
		}
		i += lt
		rest := fragment[i:]

		switch {
		case strings.HasPrefix(rest, "<!--"):
			end := strings.Index(rest, "-->")
			if end < 0 {
				return top
			}
			i += end + 3
			continue
		case strings.HasPrefix(rest, "<!"), strings.HasPrefix(rest, "<?"):
			end := strings.IndexByte(rest, '>')
			if end < 0 {
				return top
			}
			i += end + 1
			continue
		}

		// The tag name runs to the first space, "/" or ">". On a closing tag
		// the name begins after the "/", which is what the shared helper
		// drops.
		nameStart, closing := 1, false
		if strings.HasPrefix(rest, "</") {
			closing, nameStart = true, 2
		}
		nameEnd := nameStart
		for nameEnd < len(rest) && !strings.ContainsRune(" \t\n\r/>", rune(rest[nameEnd])) {
			nameEnd++
		}
		name := strings.ToLower(rest[nameStart:nameEnd])

		// The tag ends at the first ">", which is the tag's own: an attribute
		// value holding one is quoted, and the fragments this parses are
		// templ's output, which escapes ">" inside attribute values.
		gt := strings.IndexByte(rest, '>')
		if gt < 0 {
			return top
		}
		selfClosing := strings.HasSuffix(strings.TrimSpace(rest[:gt]), "/")

		if closing {
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		} else {
			if len(stack) == 0 {
				top = append(top, name)
			}
			if !selfClosing && !fragmentVoidElements[name] {
				stack = append(stack, name)
			}
		}
		i += gt + 1
	}
	return top
}

// TestFragmentTopLevelElementsBites is the parser's own check, and the reason
// the assertion below can be believed. It runs the parser over fragments whose
// top-level shape is not in doubt and requires the shape back -- a parser that
// reports one top-level element for everything would satisfy a
// "the fragment has one top-level element" assertion no matter what the page
// rendered, which is exactly the failure this file exists to rule out.
func TestFragmentTopLevelElementsBites(t *testing.T) {
	for _, tc := range []struct {
		name     string
		fragment string
		want     []string
	}{
		{"one node", `<section id="a"><p>x</p></section>`, []string{"section"}},
		{"two nodes", `<section id="a"></section><p>x</p>`, []string{"section", "p"}},
		{
			"three nodes, nested",
			`<section id="a"><div><p>x</p></div></section><aside></aside><p>y</p>`,
			[]string{"section", "aside", "p"},
		},
		{"void element does not nest what follows", `<div><input><span>x</span></div>`, []string{"div"}},
		{"self-closing does not nest what follows", `<div><span/>x</div>`, []string{"div"}},
		{"text between nodes is not a node", `<p>a</p> text <p>b</p>`, []string{"p", "p"}},
		{"comment between nodes is not a node", `<p>a</p><!-- x --><p>b</p>`, []string{"p", "p"}},
		{"unclosed sibling is still counted", `<p>a</p><div>`, []string{"p", "div"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fragmentTopLevelElements(tc.fragment),
				"a parser that cannot count these cannot be trusted to count the detail's")
		})
	}
}

// TestTaskDetailFragmentIsExactlyItsSwapTarget runs the real detail through
// the handler's htmx branch and requires the served fragment to be the one
// node the Refresh button targets.
//
// It is asked of the fragment with every property the rail can render filled
// in, because the rail is the part of the page most likely to grow a second
// root: a card hoisted out of the <aside>, or a row that closes the dl early
// and leaves the rest of the card as a sibling. A page with nothing to render
// in the rail would see neither.
func TestTaskDetailFragmentIsExactlyItsSwapTarget(t *testing.T) {
	f := newDetailFixture(t)
	esc, claim := uuid.New(), uuid.New()
	lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	first := f.add(store.Task{Title: "first-dep", CurrentLane: store.Lane("Done")})
	second := f.add(store.Task{Title: "second-dep", CurrentLane: store.LaneTesting})
	body := "the description"
	// Every field set before the task goes into the fixture: add stores the
	// row by value, so a field set on the returned struct afterwards is a
	// field the store never sees -- which is how a test can pass on a
	// fragment the page rendered from half the inputs it meant to set.
	task := f.addOnMilepebble(store.Task{
		Title: "fully-loaded", CurrentLane: store.LaneTesting, Body: &body,
		AttemptCount: 2, CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})
	f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: time.Now().UTC()}
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
	f.store.deps = []store.TaskDependency{{DependsOnTaskID: first.ID}, {DependsOnTaskID: second.ID}}
	f.store.notes = []store.Note{{Kind: store.NoteKindScopeNote, Body: "a note", CurrentStatus: "noted"}}

	code, fragment := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", fragment)

	top := fragmentTopLevelElements(fragment)
	require.Equal(t, []string{"section"}, top,
		"the served fragment must be exactly the swap target, or every Refresh click splices the extra top-level nodes into the page (duplicating the h1 and the Refresh button):\n%s", fragment)

	// And the one node is the section the button names, not some other
	// section that happens to be the only root.
	assert.True(t, strings.HasPrefix(strings.TrimSpace(fragment), `<section id="`+pages.TaskDetailAnchor+`"`),
		"the fragment's single root must carry the id the Refresh button targets:\n%s", fragment)
	// One of each, so a swap cannot duplicate them.
	assert.Equal(t, 1, strings.Count(fragment, `data-krill="refresh"`),
		"exactly one Refresh button in the fragment, or a swap duplicates it")
	assert.Equal(t, 1, strings.Count(fragment, `data-krill="page-title"`),
		"exactly one page title in the fragment, or a swap duplicates it")
	// The rail is inside the fragment rather than beside it, which is the
	// shape whose failure this test is about -- and it is fully populated,
	// so the fixture above cannot have quietly rendered an empty one.
	for _, want := range []string{
		`data-krill="task-properties-rail"`,
		`data-krill="task-properties"`,
		`data-krill="task-depends-on"`,
		`data-krill="task-properties-escalated"`,
		`data-krill="task-lease"`,
	} {
		assert.Contains(t, fragment, want, "the fixture did not populate the rail, so the root count proves little")
	}
}
