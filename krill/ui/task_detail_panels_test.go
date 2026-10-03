package main

// FR 7e463e31's two list panels, row by row: EVERY note carries its kind
// badge and its lifecycle-status badge, and EVERY dependency carries its
// title and its lane.
//
// Both cases are driven through the real handler and asserted against a
// parse of the served fragment, and both are asserted PER ROW -- a note's
// own badges are read out of that note's own element -- because the way
// this feature breaks is quietly. A panel that renders one badge pair for
// the list instead of one per note, or that pairs every row with the
// first row's kind, produces a page that still shows both words
// somewhere, so a substring assertion passes against a list an operator
// cannot read.
//
// The fixtures deliberately repeat a kind and vary the lifecycle status,
// and give every dependency a distinct title and lane: a row rendered
// from its neighbour's data has nothing left to get wrong.

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// nodesWithHookWithin is every DESCENDANT of n carrying data-krill="hook",
// so an assertion about one row's badges is about that row and not about
// a badge belonging to some other row on the same panel.
func nodesWithHookWithin(n *html.Node, hook string) []*html.Node {
	var found []*html.Node
	var walk func(*html.Node)
	walk = func(c *html.Node) {
		for k := c.FirstChild; k != nil; k = k.NextSibling {
			if k.Type == html.ElementNode {
				if v, ok := attrOfNode(k, "data-krill"); ok && v == hook {
					found = append(found, k)
				}
			}
			walk(k)
		}
	}
	walk(n)
	return found
}

// theOneWithin is the single descendant carrying hook, failing with the
// count so a panel that dropped a badge and one that rendered two are
// told apart.
func theOneWithin(t *testing.T, n *html.Node, hook string) *html.Node {
	t.Helper()
	got := nodesWithHookWithin(n, hook)
	require.Len(t, got, 1, "expected exactly one %q in this region, found %d", hook, len(got))
	return got[0]
}

// TestTaskDetailNotesPanelGivesEveryNoteBothBadges is the FR's "latest
// notes with kind and status badges" applied to the panel that lists
// them all: a note is only legible as a note if both facets are on it --
// what it IS (kind) and where it sits in its lifecycle (status). One
// without the other is a comment nobody can triage.
//
// Both badges come out of the page's ONE TaskNoteBadges, which is what
// keeps this panel and the Overview panel's latest notes from disagreeing
// about the same note. So each row is required to carry exactly one
// badge-pair region holding exactly one badge of each facet: a row with
// the pair inlined a second way would render identically here and
// differently on the Overview.
func TestTaskDetailNotesPanelGivesEveryNoteBothBadges(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "annotated", CurrentLane: store.LaneTesting})
	f.store.notes = []store.Note{
		{Kind: store.NoteKindScopeNote, Body: "discovered-while-implementing", CurrentStatus: store.NoteLifecycleStatusNoted},
		{Kind: store.NoteKindComment, Body: "a-first-comment", CurrentStatus: store.NoteLifecycleStatusCarriedOver},
		{Kind: store.NoteKindCheapExpensiveLater, Body: "cheap-until-it-is-not", CurrentStatus: store.NoteLifecycleStatusDeferred},
		// The same kind as the comment two rows up, under a different
		// lifecycle status: a panel that resolved the status from the
		// kind, or paired rows by kind rather than by position, renders
		// these two identically and one of them is wrong.
		{Kind: store.NoteKindComment, Body: "a-closed-comment", CurrentStatus: store.NoteLifecycleStatusClosed},
		// A kind and a status the vocabulary has no colour for. The store
		// rejects them on the way in, so a row can only reach the page by
		// outliving the enumeration it was written under -- and it must
		// still render BOTH badges rather than lose the facet the mapper
		// has no arm for, which would silently demote it to an
		// unlabelled note.
		{Kind: store.NoteKind("kind-from-the-future"), Body: "written-before-the-enumeration-grew", CurrentStatus: store.NoteLifecycleStatus("also-from-the-future")},
	}

	code, frag := f.getTabAt(task.ID.String(), "?tab="+pages.TaskTabNotes)
	require.Equal(t, 200, code, "body: %s", frag)

	panels := elementsWithHook(t, frag, "task-panel-notes")
	require.Len(t, panels, 1, "the notes tab renders its own panel host")

	rows := nodesWithHookWithin(panels[0], "task-note")
	require.Len(t, rows, len(f.store.notes),
		"the panel lists every note the read returned, no more and no fewer")

	// Indexed against the fixture on purpose: ListNotesForTask returns
	// ascending by created_at, id and the panel must not re-sort, so row i
	// is note i. Pairing by anything else would let a re-sorted list pass
	// a per-row assertion that only checked the words appeared.
	for i, n := range f.store.notes {
		row := rows[i]

		pair := theOneWithin(t, row, "note-badges")
		assert.Equal(t, string(n.Kind), textOf(theOneWithin(t, pair, "note-kind")),
			"note %d renders its own kind, not the kind of some other row", i)
		assert.Equal(t, string(n.CurrentStatus), textOf(theOneWithin(t, pair, "note-status")),
			"note %d renders its own lifecycle status: the status is the half that tells an operator whether it still needs them", i)
		assert.Equal(t, n.Body, textOf(theOneWithin(t, row, "note-body")),
			"note %d's badges are on the row carrying its own body", i)
	}
}

// TestTaskDetailDependenciesPanelGivesEveryDependencyItsTitleAndLane is
// the Dependencies tab: one entry per TaskDepLink, each named and linked
// to its own detail and each carrying the lane it is sitting in.
//
// The lane is what turns a list of titles into a picture of the blockage:
// an operator scanning a blocked task is asking which of these is still
// running, not what they are called. And the address has to be the
// PRODUCT-scoped detail, because a dependency need not share this task's
// container -- the per-container form would send the operator to a
// redirect, or worse, to a page about a different task.
//
// The three fixtures carry three different lanes and three different
// titles, so a list that painted every row with the first dependency's
// lane, or linked every row to the first dependency's task, has nothing
// left to hide behind.
func TestTaskDetailDependenciesPanelGivesEveryDependencyItsTitleAndLane(t *testing.T) {
	f := newDetailFixture(t)
	queued := f.add(store.Task{Title: "queued-dep", CurrentLane: store.Lane("Scaffold")})
	running := f.add(store.Task{Title: "running-dep", CurrentLane: store.LaneTesting})
	shipped := f.add(store.Task{Title: "shipped-dep", CurrentLane: store.Lane("Done")})
	task := f.add(store.Task{Title: "blocked", CurrentLane: store.LaneTesting})
	f.store.deps = []store.TaskDependency{
		{DependsOnTaskID: queued.ID},
		{DependsOnTaskID: running.ID},
		{DependsOnTaskID: shipped.ID},
	}

	code, frag := f.getTabAt(task.ID.String(), "?tab="+pages.TaskTabDependencies)
	require.Equal(t, 200, code, "body: %s", frag)

	panels := elementsWithHook(t, frag, "task-panel-dependencies")
	require.Len(t, panels, 1, "the dependencies tab renders its own panel host")

	rows := nodesWithHookWithin(panels[0], "task-dep")
	require.Len(t, rows, len(f.store.deps),
		"the panel lists every dependency the read returned, no more and no fewer")

	for i, dep := range f.store.deps {
		row := rows[i]
		// The fixture's own row is the expectation, so a change to the
		// lane or the title has to be made here to be made at all.
		want := taskByID(t, f, dep.DependsOnTaskID)

		link := theOneWithin(t, row, "task-dep-link")
		assert.Equal(t, want.Title, textOf(link),
			"dependency %d is named by its own title: the anchor's own text, not whatever else the row says", i)

		href, ok := attrOfNode(link, "href")
		require.True(t, ok, "dependency %d's title is a link, not plain text", i)
		assert.Equal(t, productTaskDetailPath(f.pid, dep.DependsOnTaskID), href,
			"dependency %d links to its own PRODUCT-scoped detail: a dependency need not share this task's container", i)

		assert.Equal(t, string(want.CurrentLane), textOf(theOneWithin(t, row, "task-lane")),
			"dependency %d renders its own lane, not a neighbour's", i)
	}
}

// taskByID is the fixture's own row, so a test asserts a dependency's
// lane against the task it actually read rather than against a constant
// spelled twice.
func taskByID(t *testing.T, f *detailFixture, id uuid.UUID) store.Task {
	t.Helper()
	row, ok := f.store.tasks[id]
	require.True(t, ok, "the fixture must carry every dependency it declares")
	return row
}
