// The remaining clauses of FR f6b62cc7 that product_board_card_test.go
// leaves uncovered, each because a regression in it would be silent: the
// capped state badge, the lease as a usable absolute instant when scripting
// is off, the countdown script actually being shipped on the board's own
// page, and the carve-out being per card rather than per lane.
//
// Every case renders the board through the real route, so a case that passes
// is a statement about the markup an operator receives.

package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// leaseElemRE is one card's lease <time> element and its own text -- the
// element's content is what an operator sees with scripting off, so the
// capture group is the assertion's subject, not the attribute beside it.
var leaseElemRE = regexp.MustCompile(`<time data-krill="task-lease"[^>]*>([^<]*)</time>`)

// TestBoardCardBadgesCappedAtTheAttemptCap covers the one live state
// boardCardBadges derives that nothing else reached: a task that has spent
// its cap. The count is compared on both sides of the cap in one render, so
// the case cannot pass on a board where nothing is near the cap.
func TestBoardCardBadgesCappedAtTheAttemptCap(t *testing.T) {
	f := newCardFixture()

	atCap := boardRow(productTaskNewestMilestone, "Newest milestone", "a capped card", store.LaneScaffold)
	atCap.AttemptCount = store.DefaultAttemptCap
	beneath := boardRow(productTaskNewestMilestone, "Newest milestone", "a card with room left", store.LaneScaffold)
	beneath.AttemptCount = store.DefaultAttemptCap - 1

	rows := append(append([]store.ProductTaskRow{}, f.rows...), atCap, beneath)
	containers := append(append([]store.ContainerTaskProgress{}, f.containers...),
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Done: 1, Implementation: 1, Testing: 1, Validation: 1, Scaffold: 2}))
	mux, _ := boardMux(t, rows, len(rows), containers)
	lane := swimlaneOf(t, boardBody(t, mux, ""), productTaskNewestMilestone.String())

	capped := cardOf(t, lane, atCap.TaskID.String())
	assert.Contains(t, capped, `data-krill="task-badge-capped"`,
		"a task that has spent the cap badges itself")
	assert.Contains(t, capped, `<span class="badge badge-soft badge-warning badge-sm" data-krill="task-badge-capped">Capped</span>`,
		"and badges it in the shared mapper's own wording")
	assert.Contains(t, capped, ">"+taskAttemptsLabel(store.DefaultAttemptCap)+"<",
		"the capped card reads as having no attempts left")

	room := cardOf(t, lane, beneath.TaskID.String())
	assert.NotContains(t, room, `data-krill="task-badge-capped"`,
		"a task with an attempt still in hand is not capped")
	assert.Contains(t, room, ">"+taskAttemptsLabel(store.DefaultAttemptCap-1)+"<",
		"but it still states its own count against the cap")
}

// TestBoardCardLeaseIsUsableWithScriptingOff is the no-JavaScript half of
// the lease rule: the countdown is the client's, so the element's own
// content is the whole answer when the client never runs -- and it has to be
// an instant an operator can read, not the relative form the script would
// have written and not an empty node.
func TestBoardCardLeaseIsUsableWithScriptingOff(t *testing.T) {
	f := newCardFixture()
	lane := swimlaneOf(t, boardBody(t, f.mux(t), ""), productTaskNewestMilestone.String())

	for _, tc := range []struct{ title, want string }{
		{"a claimed card", f.lease.Format(time.RFC3339)},
		{"a lapsed card", f.lapsed.Format(time.RFC3339)},
	} {
		t.Run(tc.title, func(t *testing.T) {
			card := cardOf(t, lane, f.byTitle[tc.title].TaskID.String())
			m := leaseElemRE.FindStringSubmatch(card)
			require.Len(t, m, 2, "the card states no lease element:\n%s", card)
			assert.Equal(t, tc.want, m[1],
				"with scripting off the operator must still read when the lease runs out")
			assert.Empty(t, relativeLeaseRE.FindString(m[1]),
				"and must not read it as the response's age")
		})
	}
}

// TestBoardPageShipsTheLeaseCountdownScriptInTheHead pins the wiring, not
// just the script: buildHead returning the countdown is worth nothing if the
// layout the board renders through stops passing it on, and the failure
// would be invisible -- the cards would keep rendering their absolute
// instants and the countdown would simply never happen.
func TestBoardPageShipsTheLeaseCountdownScriptInTheHead(t *testing.T) {
	f := newCardFixture()
	mux := f.mux(t)

	page := boardBody(t, mux, "")
	head := page[:strings.Index(page, "</head>")]
	assert.Contains(t, head, `time[data-krill="task-lease"][datetime]`,
		"the board's page must carry the countdown script in its head")
	// Both wordings FR f6b62cc7 names, and the event that re-runs it over a
	// Refresh's newly swapped cards.
	assert.Contains(t, head, "Lease in ")
	assert.Contains(t, head, "Lease expired ")
	assert.Contains(t, head, " ago")
	assert.Contains(t, head, "htmx:afterSwap")

	// And it is head-only: a fragment swapped into the page must not carry
	// a second copy of it, or every Refresh would stack another listener.
	assert.NotContains(t, boardFragment(t, mux), `time[data-krill="task-lease"][datetime]`,
		"the countdown belongs to the document, not to the re-requested fragment")
}

// TestBoardDoneCardHoldingAClaimIsNotQuiet is the carve-out's other side.
// FR f6b62cc7 quiets a Done card with NO live state; it does not quiet a
// Done task that is still claimed, escalated or cancelled. Keying the quiet
// on the lane instead of on the badges would hide a worker holding a lease
// on a task the board says is finished.
func TestBoardDoneCardHoldingAClaimIsNotQuiet(t *testing.T) {
	f := newCardFixture()

	holding := boardRow(productTaskNewestMilestone, "Newest milestone", "a done card still claimed", store.LaneDone)
	holding.ClaimID, holding.LeaseExpiresAt, holding.AttemptCount = &f.claim, &f.lease, 1
	rows := append(append([]store.ProductTaskRow{}, f.rows...), holding)
	containers := append(append([]store.ContainerTaskProgress{}, f.containers...),
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Done: 2, Implementation: 1, Testing: 1, Validation: 1}))
	mux, _ := boardMux(t, rows, len(rows), containers)
	lane := swimlaneOf(t, boardBody(t, mux, ""), productTaskNewestMilestone.String())

	card := cardOf(t, lane, holding.TaskID.String())
	assert.Equal(t, string(store.LaneDone), cardColumnOf(t, lane, holding.TaskID.String()),
		"a Done task is still a Done task")
	assert.Contains(t, card, `data-krill="task-badge-claimed"`,
		"a Done task still holding a live claim badges it -- the carve-out is about having no live state")
	assert.Contains(t, card, `datetime="`+f.lease.Format(time.RFC3339)+`"`,
		"and states the lease it is still holding")
	assert.Contains(t, card, ">"+taskAttemptsLabel(1)+"<",
		"and its attempts against the cap")

	// The neighbour it must not be confused with: the genuinely quiet card
	// in the very same column.
	assert.NotContains(t, cardOf(t, lane, f.byTitle["a finished card"].TaskID.String()), `data-krill="task-badge-`,
		"the Done card with nothing outstanding stays quiet beside it")
}

// TestBoardCardStateBadgesAgreeWithTheDetailMapper is the load-bearing half
// of FR de4d0e42 across views: the same task, read through two different
// store rows, must derive the same badges. It compares this card's markup
// against what the detail page's shared mapper produces for the equivalent
// task, so a change to either mapper that moves a badge is caught here
// rather than by an operator noticing one view disagreeing with another.
func TestBoardCardStateBadgesAgreeWithTheDetailMapper(t *testing.T) {
	f := newCardFixture()
	lane := swimlaneOf(t, boardBody(t, f.mux(t), ""), productTaskNewestMilestone.String())

	// One row per live state the fixture carries, and the detail page's own
	// derivation of the same task.
	for _, row := range f.rows {
		summary := store.TaskSummary{
			ID:                  row.TaskID,
			AttemptCount:        row.AttemptCount,
			CancelledAt:         row.CancelledAt,
			CurrentEscalationID: escalationIDOf(row.State),
		}
		if row.ClaimID != nil {
			summary.CurrentClaimID = row.ClaimID
			summary.LeaseExpiresAt = row.LeaseExpiresAt
		}

		card := cardOf(t, lane, row.TaskID.String())
		want := taskStateBadges(summary, time.Now().Add(time.Second))
		assert.Len(t, want, len(badgesIn(card)),
			"%q's card badges %d state(s) where the shared mapper derives %d",
			row.Title, len(badgesIn(card)), len(want))
		for _, b := range want {
			assert.Contains(t, card, `data-krill="task-badge-`+b.Key+`"`,
				"%q reads %q on the detail mapper but not on the board card", row.Title, b.Key)
		}
	}
}

// badgesIn is the state badges a card carries, by key.
func badgesIn(card string) []string {
	matches := regexp.MustCompile(`data-krill="task-badge-([a-z-]+)"`).FindAllStringSubmatch(card, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// escalationIDOf is a row's escalation state as the detail mapper reads it:
// TaskSummary carries the escalation id, the product row carries the state
// it was derived from, and this is the join between the two for a row that
// has one.
func escalationIDOf(state store.TaskState) *uuid.UUID {
	if state != store.TaskStateEscalated {
		return nil
	}
	id := uuid.New()
	return &id
}
