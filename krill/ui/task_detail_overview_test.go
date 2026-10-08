package main

// FR 7e463e31's Overview panel at the handler level: the three explanation
// callouts, the Description card, and the Latest-notes card, driven
// through the real handler so what is asserted is the markup an operator
// receives rather than the view model's shape.
//
// The panel's own composition -- which states fire, and what each one
// says -- is the builder's work, so each case below sets the task's real
// inputs (an escalation event, an attempt count, a lease expiry) and
// asserts on the banner the operator reads.

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

// overviewPanelOf is the Overview panel's own markup, so an assertion about
// a callout is about that panel and not about whatever else on the page
// happens to carry the same words.
func overviewPanelOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-panel-overview"`, "</div></div></div>")
}

// escalate installs an escalation event on the fixture's store and points
// the task at it, which is the shape a genuinely escalated task has.
func (f *detailFixture) escalate(task store.Task, reason store.EscalationReason, counter, cap *int) store.Task {
	esc := uuid.New()
	task.CurrentEscalationID = &esc
	f.store.escalation = store.EscalationEvent{
		ID:           esc,
		TaskID:       task.ID,
		Reason:       reason,
		CounterValue: counter,
		CapValue:     cap,
		CreatedAt:    time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC),
	}
	return task
}

func intPtr(n int) *int { return &n }

// TestTaskDetailOverviewNamesEachCalloutState is the FR's first clause: the
// callout says WHICH of the three states it is explaining, not merely that
// something is wrong. Each case drives the real inputs one state needs.
func TestTaskDetailOverviewNamesEachCalloutState(t *testing.T) {
	t.Run("escalated-thrash-cap", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "thrashing", CurrentLane: store.LaneTesting})
		task = f.escalate(task, store.EscalationReasonThrashCap, intPtr(3), intPtr(3))
		f.add(task)

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="escalated"`)
		assert.Contains(t, panel, "Escalated after 3 failing verdicts (thrash cap of 3).",
			"the banner names the reason AND the counter that tripped it")
		assert.NotContains(t, panel, `data-krill-callout="capped"`,
			"an escalated task below the cap carries no attempt-cap banner")
		assert.NotContains(t, panel, `data-krill-callout="lease-expired"`)
	})

	t.Run("escalated-attempt-cap", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "cap-tripped", CurrentLane: store.LaneTesting})
		task = f.escalate(task, store.EscalationReasonAttemptCap, intPtr(3), intPtr(3))
		f.add(task)

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="escalated"`)
		assert.Contains(t, panel, "Escalated at the attempt cap (3 of 3).",
			"the attempt-cap reason names the attempt counter, not a verdict counter")
	})

	t.Run("escalated-manual", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "hand-escalated", CurrentLane: store.LaneTesting})
		task = f.escalate(task, store.EscalationReasonManual, nil, nil)
		f.add(task)

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="escalated"`)
		assert.Contains(t, panel, "Escalated (manual)",
			"a manual escalation has no counter to state, so the reason is the whole explanation")
	})

	t.Run("escalated-reason-unreadable", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "unread-escalation", CurrentLane: store.LaneTesting})
		esc := uuid.New()
		task.CurrentEscalationID = &esc
		f.add(task)
		f.store.escalationErr = assertErr{}

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="escalated"`,
			"the task row's claim that it is escalated is true even when the reason could not be read")
		assert.Contains(t, panel, "could not be read",
			"and the banner says so rather than inventing a reason")
	})

	t.Run("at-attempt-cap", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{
			Title: "capped", CurrentLane: store.LaneTesting,
			AttemptCount: store.DefaultAttemptCap,
		})

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="capped"`)
		assert.Contains(t, panel, "At the attempt cap (3 of 3).",
			"the banner states the cap it is at")
		assert.NotContains(t, panel, `data-krill-callout="escalated"`,
			"being at the cap is not the same claim as having been escalated")
	})

	t.Run("one-below-the-cap", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{
			Title: "nearly", CurrentLane: store.LaneTesting,
			AttemptCount: store.DefaultAttemptCap - 1,
		})

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		assert.NotContains(t, overviewPanelOf(t, html), `data-krill-callout="capped"`,
			"a task below the cap is not capped; the FR says at the cap")
	})

	t.Run("lease-expired", func(t *testing.T) {
		f := newDetailFixture(t)
		claim := uuid.New()
		lapsed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
		task := f.add(store.Task{
			Title: "lapsed", CurrentLane: store.LaneTesting,
			CurrentClaimID: &claim, LeaseExpiresAt: &lapsed,
		})
		f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill-callout="lease-expired"`)
		assert.Contains(t, panel, "The lease on this task expired at "+lapsed.Format(time.RFC3339),
			"the banner names the instant the lease lapsed")
	})

	t.Run("lease-still-held", func(t *testing.T) {
		f := newDetailFixture(t)
		claim := uuid.New()
		live := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
		task := f.add(store.Task{
			Title: "working", CurrentLane: store.LaneTesting,
			CurrentClaimID: &claim, LeaseExpiresAt: &live,
		})
		f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		assert.NotContains(t, overviewPanelOf(t, html), `data-krill-callout="lease-expired"`,
			"a live lease is not an expired one")
	})

	t.Run("all-three-at-once", func(t *testing.T) {
		f := newDetailFixture(t)
		claim := uuid.New()
		lapsed := time.Now().Add(-time.Hour).UTC().Truncate(time.Second)
		task := f.add(store.Task{
			Title: "triple", CurrentLane: store.LaneTesting,
			AttemptCount:   store.DefaultAttemptCap,
			CurrentClaimID: &claim,
			LeaseExpiresAt: &lapsed,
		})
		task = f.escalate(task, store.EscalationReasonManual, nil, nil)
		f.add(task)
		f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Equal(t, 3, strings.Count(panel, `data-krill="task-callout"`),
			"the three states are independent and each keeps its own banner")
		for _, key := range []string{pages.TaskCalloutEscalated, pages.TaskCalloutCapped, pages.TaskCalloutLeaseExpired} {
			assert.Contains(t, panel, `data-krill-callout="`+key+`"`)
		}
	})
}

// TestTaskDetailOverviewHasNoCalloutForAHealthyTask is the FR's second
// clause, and the one a panel cannot satisfy by rendering something: a
// task with nothing wrong gets NO banner. A banner that appeared for every
// task teaches an operator to read past it.
//
// Swept over the healthy shapes that exist, because "healthy" is more than
// one task state: never attempted, attempted once and below the cap, and
// claimed with a live lease. A banner keyed off any one of those -- which
// is what a builder checking the wrong field would produce -- has nowhere
// to hide.
func TestTaskDetailOverviewHasNoCalloutForAHealthyTask(t *testing.T) {
	live := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name string
		task func(f *detailFixture) store.Task
	}{
		{"never-attempted", func(f *detailFixture) store.Task {
			body := "just a description"
			return f.add(store.Task{Title: "healthy", CurrentLane: store.LaneTesting, Body: &body})
		}},
		{"attempted-below-the-cap", func(f *detailFixture) store.Task {
			body := "just a description"
			return f.add(store.Task{
				Title: "healthy", CurrentLane: store.LaneTesting, Body: &body,
				AttemptCount: store.DefaultAttemptCap - 1,
			})
		}},
		{"claimed-with-a-live-lease", func(f *detailFixture) store.Task {
			claim := uuid.New()
			task := f.add(store.Task{
				Title: "healthy", CurrentLane: store.LaneTesting,
				CurrentClaimID: &claim, LeaseExpiresAt: &live,
			})
			f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
			return task
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := tc.task(f)
			f.store.notes = []store.Note{
				{Kind: store.NoteKindScopeNote, Body: "a note", CurrentStatus: "noted"},
			}

			code, html := f.getProductScoped(task.ID.String(), true)
			require.Equal(t, 200, code, "body: %s", html)

			panel := overviewPanelOf(t, html)
			assert.Contains(t, panel, `data-krill-panel-host="overview"`,
				"the panel renders -- it is the banner that does not")
			assert.NotContains(t, panel, `data-krill="task-callout"`,
				"a healthy task renders no banner at all, not a neutral one")
			assert.NotContains(t, html, `data-krill-callout`,
				"and no callout claims a state anywhere on the page")
		})
	}
}

// TestTaskDetailOverviewDescriptionCardFollowsTheBody is FR's Description
// clause: the body verbatim with its whitespace kept, and no card at all
// for a task that has none.
func TestTaskDetailOverviewDescriptionCardFollowsTheBody(t *testing.T) {
	t.Run("with-body", func(t *testing.T) {
		f := newDetailFixture(t)
		body := "the task body\n\n    an indented line"
		task := f.add(store.Task{Title: "described", CurrentLane: store.LaneTesting, Body: &body})

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.Contains(t, panel, `data-krill="task-description"`)
		assert.Contains(t, panel, ">Description</h2>")
		assert.Contains(t, panel, "the task body")
		assert.Contains(t, panel, "<code>an indented line",
			"the body renders as markdown, so an indented line is a code block")
		assert.Contains(t, panel, "krill-md")
	})

	t.Run("without-body", func(t *testing.T) {
		f := newDetailFixture(t)
		task := f.add(store.Task{Title: "undescribed", CurrentLane: store.LaneTesting})

		code, html := f.getProductScoped(task.ID.String(), true)
		require.Equal(t, 200, code, "body: %s", html)

		panel := overviewPanelOf(t, html)
		assert.NotContains(t, panel, `data-krill="task-description"`,
			"an empty card claims a description exists and is blank")
		assert.NotContains(t, panel, ">Description</h2>")
		assert.Contains(t, panel, `data-krill-panel-host="overview"`,
			"the panel itself is unaffected")
	})
}

// TestTaskDetailOverviewLatestNotesAreAStatedSubset is FR's Latest-notes
// clause: the Overview shows the TAIL of the notes read and says so. The
// FR names "latest notes" for this tab and "every note" for the Notes tab,
// so a list presented as the whole one would be a lie in one of them.
func TestTaskDetailOverviewLatestNotesAreAStatedSubset(t *testing.T) {
	f := newDetailFixture(t)
	var all []store.Note
	for _, body := range []string{"one", "two", "three", "four", "five"} {
		all = append(all, store.Note{
			ID: uuid.New(), Kind: store.NoteKindComment, Body: body, CurrentStatus: "noted",
		})
	}
	task := f.add(store.Task{Title: "chatty", CurrentLane: store.LaneTesting})
	f.store.notes = all

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	panel := overviewPanelOf(t, html)
	assert.Contains(t, panel, `data-krill="task-latest-notes"`)
	assert.Equal(t, pages.LatestNotesLimit, strings.Count(panel, `data-krill="task-latest-note"`),
		"the Overview shows the most recent few, not every note")
	assert.Contains(t, panel, "Showing the 3 most recent of 5",
		"the card states that it is showing a subset, and of how many")
	assert.NotContains(t, panel, ">All 5 notes")

	// The tail, not the head: the three shown are the newest three.
	for _, want := range []string{"three", "four", "five"} {
		assert.Contains(t, panel, ">"+want+"<", "the Overview shows the most recent notes")
	}
	for _, old := range []string{">one<", ">two<"} {
		assert.NotContains(t, panel, old, "the oldest notes are dropped, not the newest")
	}

	// The Notes tab lists EVERY note, so the two tabs agree on the total
	// the Overview's count is measured against.
	code, notesTab := f.getTabAt(task.ID.String(), "?tab=notes")
	require.Equal(t, 200, code, "body: %s", notesTab)
	assert.Equal(t, 5, strings.Count(notesTab, `data-krill="task-note"`),
		"the Notes tab is the whole list the Overview is a subset of")
}

// TestTaskDetailOverviewLatestNotesSaysAllWhenItIsTheWholeList is the other
// arm of the count: a short task has nothing truncated, and the card says
// that rather than implying a boundary that does not exist.
func TestTaskDetailOverviewLatestNotesSaysAllWhenItIsTheWholeList(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "quiet", CurrentLane: store.LaneTesting})
	f.store.notes = []store.Note{
		{Kind: store.NoteKindScopeNote, Body: "only note", CurrentStatus: "noted"},
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	panel := overviewPanelOf(t, html)
	assert.Contains(t, panel, "All 1 note.")
	assert.NotContains(t, panel, "most recent")
}

// TestTaskDetailOverviewLatestNotesAreAbsentWhenTheReadFailed: an
// unreadable list must not render as an empty one. The Notes tab owns that
// alert, and the Overview's card stays away entirely.
func TestTaskDetailOverviewLatestNotesAreAbsentWhenTheReadFailed(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "unreadable", CurrentLane: store.LaneTesting})
	f.store.notesErr = assertErr{}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	panel := overviewPanelOf(t, html)
	assert.NotContains(t, panel, `data-krill="task-latest-notes"`)
	assert.NotContains(t, panel, "All 0 notes.")
	assert.Contains(t, panel, `data-krill-panel-host="overview"`,
		"a failed section costs the section, not the panel")

	// The Notes tab carries the alert in its own place.
	_, notesTab := f.getTabAt(task.ID.String(), "?tab=notes")
	assert.Contains(t, notesTab, "The notes could not be read")
}

// TestTaskDetailOverviewLatestNotesCarryTheirBadges: each note in the
// Overview's card carries its kind and lifecycle status, through the one
// shared component the Notes tab renders the same pair with.
func TestTaskDetailOverviewLatestNotesCarryTheirBadges(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "badged", CurrentLane: store.LaneTesting})
	f.store.notes = []store.Note{
		{ID: uuid.New(), Kind: store.NoteKindScopeNote, Body: "a scope note", CurrentStatus: "noted"},
		{ID: uuid.New(), Kind: store.NoteKindCheapExpensiveLater, Body: "a cost note", CurrentStatus: "deferred"},
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	panel := overviewPanelOf(t, html)
	assert.Equal(t, 2, strings.Count(panel, `data-krill="note-badges"`),
		"one shared kind+status badge pair per note")
	assert.Equal(t, 2, strings.Count(panel, `data-krill="note-kind"`))
	assert.Equal(t, 2, strings.Count(panel, `data-krill="note-status"`))
	for _, want := range []string{"scope-note", "cheap-expensive-later", "noted", "deferred"} {
		assert.Contains(t, panel, ">"+want+"<",
			"a note's kind and status are both readable on the card")
	}

	// The same two notes, read the same way, on the Notes tab.
	_, notesTab := f.getTabAt(task.ID.String(), "?tab=notes")
	for _, hook := range []string{`data-krill="note-badges"`, `data-krill="note-kind"`, `data-krill="note-status"`} {
		assert.Equal(t, strings.Count(panel, hook), strings.Count(notesTab, hook),
			"the Overview and the Notes tab render the same badge pair per note")
	}
}

// TestTaskDetailOverviewIsThePanelTheTabStripHosts pins the Overview as one
// fillable host region with the tab's own panel marker, so the sibling
// Notes/Dependencies/Slice tasks have something to fill that is not a
// restructuring.
func TestTaskDetailOverviewIsThePanelTheTabStripHosts(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "tabbed", CurrentLane: store.LaneTesting})

	code, frag := f.getTabAt(task.ID.String(), "")
	require.Equal(t, 200, code, "body: %s", frag)

	assert.Contains(t, frag, `data-krill="task-panel-overview"`)
	assert.Contains(t, frag, `data-krill-panel-host="overview"`)
	assert.Equal(t, 1, strings.Count(frag, `data-krill="task-panel-overview"`),
		"exactly one Overview host in the fragment")
	// The Overview's own content does not travel on another tab.
	for _, other := range []string{"notes", "dependencies", "slice"} {
		_, otherTab := f.getTabAt(task.ID.String(), "?tab="+other)
		assert.NotContains(t, otherTab, `data-krill="task-panel-overview"`,
			"the Overview's contents belong to the Overview tab alone")
	}
}
