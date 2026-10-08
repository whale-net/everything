package pages

// FR 7e463e31's Overview panel at the component level: the three
// explanation callouts, the Description card, and the Latest-notes card.
//
// The markup is asserted through the tab region the panel actually lives
// in rather than by calling the panel directly, so a case also proves the
// Overview renders as the region's own host and not beside another tab's.

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// renderOverview is the Overview panel as the tab region serves it.
func renderOverview(t *testing.T, page TaskDetailPage) string {
	t.Helper()
	page.Tab = TaskTabOverview
	page.Tabs = tabsFor(TaskTabOverview)
	return renderBody(t, TaskDetailTabs(page))
}

// A healthy task: no callouts at all. The absence IS the state -- there is
// no neutral banner -- so this asserts both that the panel renders and
// that no element carrying the callout hook does.
func TestTaskOverviewRendersNoCalloutForAHealthyTask(t *testing.T) {
	fragment := renderOverview(t, TaskDetailPage{
		Title:    "a healthy task",
		Body:     "the description",
		Callouts: nil,
	})

	assert.Contains(t, fragment, `data-krill-panel-host="overview"`,
		"a healthy task still renders its Overview panel")
	assert.NotContains(t, fragment, `data-krill="task-callout"`,
		"a healthy task renders no banner at all -- not a neutral one")
	assert.NotContains(t, fragment, `data-krill-callout`,
		"no callout may claim a state the task is not in")
	assert.NotContains(t, fragment, "Nothing is wrong",
		"there is no 'nothing is wrong' banner to render")
}

// Each of the three states renders its own banner, carrying the key the
// markup asserts on, so a panel that quietly stopped explaining one of
// them fails here rather than passing as "some alert is present".
func TestTaskOverviewCalloutRendersOneBannerPerStateNamedByItsKey(t *testing.T) {
	fragment := renderOverview(t, TaskDetailPage{
		Title: "an escalated, capped, lapsed task",
		Callouts: []TaskCallout{
			{Key: TaskCalloutEscalated, Message: "Escalated after 3 failing verdicts (thrash cap of 3)."},
			{Key: TaskCalloutCapped, Message: "At the attempt cap (3 of 3)."},
			{Key: TaskCalloutLeaseExpired, Message: "The lease on this task expired at 2026-09-30T12:00:00Z."},
		},
	})

	assert.Equal(t, 3, strings.Count(fragment, `data-krill="task-callout"`),
		"one banner per explained state, no more and no fewer")
	for _, want := range []string{
		`data-krill-callout="escalated"`,
		"Escalated after 3 failing verdicts (thrash cap of 3).",
		`data-krill-callout="capped"`,
		"At the attempt cap (3 of 3).",
		`data-krill-callout="lease-expired"`,
		"The lease on this task expired at 2026-09-30T12:00:00Z.",
	} {
		assert.Contains(t, fragment, want)
	}

	// The three are independent states that can co-occur, and each is named
	// on its own line rather than merged: a merged sentence would pick one
	// of them to be the headline.
	assert.Less(t,
		strings.Index(fragment, `data-krill-callout="escalated"`),
		strings.Index(fragment, `data-krill-callout="capped"`))
	assert.Less(t,
		strings.Index(fragment, `data-krill-callout="capped"`),
		strings.Index(fragment, `data-krill-callout="lease-expired"`))
}

// A single state's banner renders even when it is the only one: a task can
// be escalated without being capped or holding a lapsed lease, and the
// other two banners must not be invented to fill the space.
func TestTaskOverviewCalloutRendersTheOneStateThatFired(t *testing.T) {
	fragment := renderOverview(t, TaskDetailPage{
		Title:    "only escalated",
		Callouts: []TaskCallout{{Key: TaskCalloutEscalated, Message: "Escalated (manual), so no worker can claim it."}},
	})

	assert.Equal(t, 1, strings.Count(fragment, `data-krill="task-callout"`))
	assert.Contains(t, fragment, `data-krill-callout="escalated"`)
	assert.NotContains(t, fragment, `data-krill-callout="capped"`)
	assert.NotContains(t, fragment, `data-krill-callout="lease-expired"`)
}

// The Description card is the task's own body, rendered as markdown, and
// it is omitted entirely for a task with no body -- an empty card claims a
// description exists and is blank, which is a different and untrue
// statement.
func TestTaskOverviewDescriptionCardFollowsTheTaskBody(t *testing.T) {
	withBody := renderOverview(t, TaskDetailPage{
		Title: "described", Body: "first line\n\n  second line",
	})
	assert.Contains(t, withBody, `data-krill="task-description"`)
	assert.Contains(t, withBody, ">Description</h2>")
	assert.Contains(t, withBody, "first line")
	assert.Contains(t, withBody, "<p>second line</p>",
		"the body renders as markdown, so its paragraph breaks survive")

	withoutBody := renderOverview(t, TaskDetailPage{Title: "undescribed"})
	assert.NotContains(t, withoutBody, `data-krill="task-description"`,
		"a task with no body renders no Description card at all")
	assert.NotContains(t, withoutBody, ">Description</h2>",
		"not even an empty card titled Description")

	// Whitespace-only is still a body: the operator typed something.
	blank := renderOverview(t, TaskDetailPage{Title: "blank", Body: " "})
	assert.Contains(t, blank, `data-krill="task-description"`)
}

// The Latest-notes card is a SUBSET of the notes the Notes tab lists, and
// it states which subset -- a truncated list presented with no visible
// boundary is how an operator concludes there are three notes when there
// are thirty.
func TestTaskOverviewLatestNotesStatesWhetherItIsTheWholeList(t *testing.T) {
	shown := []TaskNoteRow{
		{Kind: "scope-note", Status: "noted", Body: "third newest"},
		{Kind: "comment", Status: "carried-over", Body: "second newest"},
		{Kind: "cheap-expensive-later", Status: "deferred", Body: "newest"},
	}

	truncated := renderOverview(t, TaskDetailPage{
		Title: "chatty", Notes: append([]TaskNoteRow{
			{Kind: "comment", Status: "noted", Body: "an older note"},
			{Kind: "comment", Status: "noted", Body: "oldest note"},
		}, shown...), LatestNotes: shown, NotesTotal: 5,
	})
	assert.Contains(t, truncated, `data-krill="task-latest-notes"`)
	assert.Contains(t, truncated, ">Latest notes</h2>")
	assert.Contains(t, truncated, "Showing the 3 most recent of 5",
		"a truncated list says so, and says how much of it is shown")
	assert.NotContains(t, truncated, ">All 5 notes",
		"a truncated list must never be labelled as the whole one")
	assert.Equal(t, LatestNotesLimit, strings.Count(truncated, `data-krill="task-latest-note"`),
		"the Overview shows the tail, capped at LatestNotesLimit")

	whole := renderOverview(t, TaskDetailPage{
		Title: "quiet", Notes: shown[:2], LatestNotes: shown[:2], NotesTotal: 2,
	})
	assert.Contains(t, whole, "All 2 notes.",
		"a list that is the whole one says so")
	assert.NotContains(t, whole, "most recent",
		"there is nothing truncated to point at")

	one := renderOverview(t, TaskDetailPage{
		Title: "lone", Notes: shown[:1], LatestNotes: shown[:1], NotesTotal: 1,
	})
	assert.Contains(t, one, "All 1 note.", "a single note is not plural")
}

// Every note in the card carries its kind and lifecycle status through the
// ONE badge component the Notes tab shares, so the two facets cannot
// disagree about what a note is.
func TestTaskOverviewLatestNotesShareTheOneNoteBadgeComponent(t *testing.T) {
	notes := []TaskNoteRow{
		{Kind: "scope-note", Status: "noted", Body: "a scope note"},
		{Kind: "cheap-expensive-later", Status: "deferred", Body: "a cost note"},
	}
	overview := renderOverview(t, TaskDetailPage{
		Title: "badged", Notes: notes, LatestNotes: notes, NotesTotal: 2,
	})

	assert.Equal(t, 2, strings.Count(overview, `data-krill="note-badges"`),
		"one shared badge pair per note")
	assert.Equal(t, 2, strings.Count(overview, `data-krill="note-kind"`))
	assert.Equal(t, 2, strings.Count(overview, `data-krill="note-status"`))
	for _, want := range []string{"scope-note", "cheap-expensive-later", "noted", "deferred"} {
		assert.Contains(t, overview, ">"+want+"<",
			"a note's kind and status are both readable on the card")
	}

	// And the Notes tab renders the same pair through the same component:
	// the same note cannot read one way on one tab and another on the other.
	notesTab := renderBody(t, TaskDetailTabs(TaskDetailPage{
		Tab: TaskTabNotes, Tabs: tabsFor(TaskTabNotes), Notes: notes,
	}))
	for _, hook := range []string{`data-krill="note-badges"`, `data-krill="note-kind"`, `data-krill="note-status"`} {
		assert.Equal(t, strings.Count(overview, hook), strings.Count(notesTab, hook),
			"the Overview and the Notes tab must render the same badge pair per note")
	}
}

// A failed notes read renders no Latest-notes card rather than an empty
// one. The Notes tab owns that alert, and the same rule applies here: an
// unreadable list must never render as a read one.
func TestTaskOverviewLatestNotesAreAbsentWhenTheReadFailedOrReadNothing(t *testing.T) {
	failed := renderOverview(t, TaskDetailPage{
		Title:      "unreadable",
		NotesError: "The notes could not be read. See the logs.",
	})
	assert.NotContains(t, failed, `data-krill="task-latest-notes"`,
		"a failed read must not render as an empty card")
	assert.NotContains(t, failed, "All 0 notes.",
		"and must not claim to have read zero notes")

	none := renderOverview(t, TaskDetailPage{Title: "unnoted"})
	assert.NotContains(t, none, `data-krill="task-latest-notes"`,
		"a task with no notes has no Latest-notes card to show")

	// The panel itself is still there in both cases: a lost section is not
	// a lost panel.
	for name, fragment := range map[string]string{"failed": failed, "empty": none} {
		t.Run(name, func(t *testing.T) {
			host, ok := attrOf(t, fragment, "task-panel-overview", "data-krill-panel-host")
			require.True(t, ok)
			assert.Equal(t, "overview", host)
		})
	}
}
