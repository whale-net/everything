// Acceptance coverage for the product-wide Board's cards (FR f6b62cc7):
// the title linking to detail, the milepebble badge on a cut milestone, a
// state badge per live state, "n of cap attempts", and the lease countdown
// -- plus the carve-out that leaves a Done card with nothing outstanding
// showing only its title and its milepebble.
//
// Every case drives the real route through the real registrations, so the
// card is asserted as the operator sees it rather than as a builder's
// struct. The relative lease wording is deliberately absent from the
// server's response and that is asserted, not assumed: a "Lease in 18 min"
// rendered into a fragment the Refresh button re-requests would be as old
// as the response and would differ between two identical reads, so the
// relative form is the head script's job (NFR 7b497d92).

package main

import (
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// relativeLeaseRE is the wording only the client-side script may produce:
// "Lease in 18 min" and "Lease expired 6 min ago". Its absence from a
// server response is the invariant; see the file comment.
var relativeLeaseRE = regexp.MustCompile(`Lease in \d|\d+ (?:seconds?|minutes?|hours?|days?) ago`)

// cardColumnOf is the lane of the column a card was rendered in, sliced
// out of the swimlane's own markup.
//
// It reads the column element each card is nested in rather than trusting
// the card: "in its own lane's column" is a claim about the markup's
// nesting, and a builder that put a card in the wrong column while
// labelling it correctly would pass any check that read the label.
func cardColumnOf(t *testing.T, lane, taskID string) string {
	t.Helper()
	cardAt := strings.Index(lane, `data-krill-task-id="`+taskID+`"`)
	require.NotEqual(t, -1, cardAt, "no card for task %s in:\n%s", taskID, lane)
	open := strings.LastIndex(lane[:cardAt], `<div data-krill="board-column" data-krill-lane="`)
	require.NotEqual(t, -1, open, "the card is in no column:\n%s", lane)
	m := regexp.MustCompile(`data-krill-lane="([A-Za-z]+)"`).FindStringSubmatch(lane[open:])
	require.Len(t, m, 2, "the column names no lane:\n%s", lane[open:])
	return m[1]
}

// cardFixture is the four-card board every case in this file renders: a
// Done task with nothing outstanding, a task holding a live claim, one
// whose lease has lapsed, and a cancelled task under a milepebble. The
// four differ in lane, in state and in whether a milepebble names them,
// which is what makes one case's markup unable to satisfy another's
// assertions.
type cardFixture struct {
	rows       []store.ProductTaskRow
	containers []store.ContainerTaskProgress

	claim   uuid.UUID
	lease   time.Time
	lapsed  time.Time
	kill    time.Time
	byTitle map[string]store.ProductTaskRow
}

func newCardFixture() cardFixture {
	now := time.Now()
	f := cardFixture{
		claim:  uuid.New(),
		lease:  now.Add(18 * time.Minute).UTC().Truncate(time.Second),
		lapsed: now.Add(-6 * time.Minute).UTC().Truncate(time.Second),
		kill:   now.Add(-time.Hour).UTC().Truncate(time.Second),
	}

	done := boardRow(productTaskNewestMilestone, "Newest milestone", "a finished card", store.LaneDone)
	claimed := boardRow(productTaskNewestMilestone, "Newest milestone", "a claimed card", store.LaneImplementation)
	claimed.ClaimID, claimed.LeaseExpiresAt, claimed.AttemptCount = &f.claim, &f.lease, 2
	expired := boardRow(productTaskNewestMilestone, "Newest milestone", "a lapsed card", store.LaneTesting)
	expired.ClaimID, expired.LeaseExpiresAt = &f.claim, &f.lapsed
	killed := boardMilepebbleRow(productTaskNewestMilestone, "Newest milestone",
		productTaskNewestMilepebble, "A cut", "a cancelled card", store.LaneValidation)
	killed.CancelledAt = &f.kill

	f.rows = []store.ProductTaskRow{done, claimed, expired, killed}
	f.containers = []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Done: 1, Implementation: 1, Testing: 1, Validation: 1}),
		{
			Milestone: store.ProductTaskMilestoneRef{ID: productTaskNewestMilestone, Name: "Newest milestone"},
			Milepebble: &store.ProductTaskMilepebbleRef{
				ID: productTaskNewestMilepebble, Name: "A cut", Status: store.MilestoneStatusInDesign,
			},
			PerLane: store.TaskLaneCounts{Validation: 1},
		},
	}
	f.byTitle = map[string]store.ProductTaskRow{
		done.Title: done, claimed.Title: claimed, expired.Title: expired, killed.Title: killed,
	}
	return f
}

// mux mounts the real board route over the fixture.
func (f cardFixture) mux(t *testing.T) *http.ServeMux {
	t.Helper()
	mux, _ := boardMux(t, f.rows, len(f.rows), f.containers)
	return mux
}

// TestBoardCardsCarryTheirTitleMilepebbleStateAttemptsAndLease is FR
// f6b62cc7's card content: each card shows what an operator needs to
// decide whether to pick the task up.
func TestBoardCardsCarryTheirTitleMilepebbleStateAttemptsAndLease(t *testing.T) {
	f := newCardFixture()
	body := boardBody(t, f.mux(t), "")
	lane := swimlaneOf(t, body, productTaskNewestMilestone.String())

	for title, want := range map[string]struct {
		lane     string
		badges   []string
		attempts string
		lease    string
	}{
		"a claimed card": {
			lane: string(store.LaneImplementation), badges: []string{"claimed"},
			attempts: "2 of 3", lease: f.lease.Format(time.RFC3339),
		},
		"a lapsed card": {
			lane: string(store.LaneTesting), badges: []string{"lease-expired"},
			attempts: "0 of 3", lease: f.lapsed.Format(time.RFC3339),
		},
		"a cancelled card": {
			lane: string(store.LaneValidation), badges: []string{"cancelled"},
			attempts: "0 of 3",
		},
	} {
		t.Run(title, func(t *testing.T) {
			row := f.byTitle[title]
			card := cardOf(t, lane, row.TaskID.String())

			assert.Equal(t, want.lane, cardColumnOf(t, lane, row.TaskID.String()),
				"the card must sit in the column of its own lane")
			assert.Contains(t, card, `data-krill="task-title">`+title)
			assert.Contains(t, card, taskDetailPath(productTaskProduct, productTaskNewestMilestone, row.TaskID),
				"the title links to the task's detail page")
			assert.Contains(t, card, ">"+want.attempts+"<",
				"the card states the attempt count against the cap")
			assert.Contains(t, card, `data-krill="task-attempts"`)
			for _, key := range want.badges {
				assert.Contains(t, card, `data-krill="task-badge-`+key+`"`,
					"the card badges its live state %q", key)
			}
			if want.lease == "" {
				assert.NotContains(t, card, `data-krill="task-lease"`,
					"a card with no claim states no lease")
				return
			}
			assert.Contains(t, card, `data-krill="task-lease"`,
				"a card holding a claim states its lease")
			assert.Contains(t, card, `datetime="`+want.lease+`"`,
				"the lease is the absolute instant, RFC3339, for the client script to relativise")
		})
	}
}

// TestBoardCardRendersEachStateAndEachCardExactlyOnce is FR f6b62cc7's
// placement rule and FR de4d0e42's mapper rule together: a card appears
// once, in the column of its own lane, and its state badge comes from the
// one shared mapper -- a lapsed lease is never badged Claimed, which is
// the load-bearing half of that FR.
func TestBoardCardRendersEachStateAndEachCardExactlyOnce(t *testing.T) {
	f := newCardFixture()
	body := boardBody(t, f.mux(t), "")
	lane := swimlaneOf(t, body, productTaskNewestMilestone.String())

	for _, row := range f.rows {
		card := cardOf(t, lane, row.TaskID.String())
		assert.Contains(t, card, `data-krill="task-title">`+row.Title,
			"the card keyed by this id is this task's own card")
		assert.Equal(t, 1, strings.Count(lane, `data-krill-task-id="`+row.TaskID.String()+`"`),
			"%q is on the board exactly once, never duplicated into a second column", row.Title)
		assert.Equal(t, string(row.CurrentLane), cardColumnOf(t, lane, row.TaskID.String()),
			"%q sits only in its own lane's column", row.Title)
	}
	// The rule is per card, not per page: the lapsed card reads
	// lease-expired and never Claimed, while the card whose lease still
	// holds reads Claimed. Asserting the page carries no claimed badge at
	// all would be satisfied by a board where nothing is claimed.
	lapsed := cardOf(t, lane, f.byTitle["a lapsed card"].TaskID.String())
	assert.NotContains(t, lapsed, `data-krill="task-badge-claimed"`,
		"a task whose lease expired is never badged Claimed")
	assert.Contains(t, lapsed, `data-krill="task-badge-lease-expired"`)

	// The state badge is the shared mapper's, so a claimed card reads the
	// way the list and the detail read it.
	claimed := cardOf(t, lane, f.byTitle["a claimed card"].TaskID.String())
	assert.Contains(t, claimed, `<span class="badge badge-info badge-sm" data-krill="task-badge-claimed">Claimed</span>`)
	assert.Contains(t, lapsed, `<span class="badge badge-warning badge-sm" data-krill="task-badge-lease-expired">Lease expired</span>`)
}

// TestBoardCardNamesTheMilepebbleWhenTheMilestoneIsCut: a cut
// milestone's tasks are aggregated under it on the board, so the card is
// the only place left that says which milepebble the work belongs to.
func TestBoardCardNamesTheMilepebbleWhenTheMilestoneIsCut(t *testing.T) {
	f := newCardFixture()
	body := boardBody(t, f.mux(t), "")
	lane := swimlaneOf(t, body, productTaskNewestMilestone.String())

	cut := f.byTitle["a cancelled card"]
	cutCard := cardOf(t, lane, cut.TaskID.String())
	assert.Contains(t, cutCard, `data-krill="task-milepebble"`,
		"a milepebble task's card names the milepebble it came from")
	assert.Contains(t, cutCard, `class="badge badge-outline badge-sm">A cut<`,
		"and names it in a badge, not as loose text")

	// A milestone's own task has no milepebble to name, and an uncut
	// milestone has none at all.
	own := f.byTitle["a claimed card"]
	assert.NotContains(t, cardOf(t, lane, own.TaskID.String()), `data-krill="task-milepebble"`,
		"a milestone's own task badges no milepebble")
}

// TestBoardDoneCardWithNoLiveStateShowsOnlyTitleAndMilepebble is FR
// f6b62cc7's explicit carve-out: a Done task with nothing outstanding is
// finished work, and a card badging its attempts and a lease reads as work
// still to do.
func TestBoardDoneCardWithNoLiveStateShowsOnlyTitleAndMilepebble(t *testing.T) {
	f := newCardFixture()
	// Give the Done card a milepebble, so "only its title and its
	// milepebble" has both halves to show and the absence assertions
	// below cannot pass by the milepebble simply being missing.
	rows := make([]store.ProductTaskRow, len(f.rows))
	copy(rows, f.rows)
	for i := range rows {
		if rows[i].CurrentLane == store.LaneDone {
			rows[i].Milepebble = &store.ProductTaskMilepebbleRef{
				ID: productTaskNewestMilepebble, Name: "A cut", Status: store.MilestoneStatusInDesign,
			}
			f.byTitle[rows[i].Title] = rows[i]
		}
	}
	mux, _ := boardMux(t, rows, len(rows), f.containers)
	body := boardBody(t, mux, "")
	lane := swimlaneOf(t, body, productTaskNewestMilestone.String())

	done := f.byTitle["a finished card"]
	card := cardOf(t, lane, done.TaskID.String())
	assert.Equal(t, string(store.LaneDone), cardColumnOf(t, lane, done.TaskID.String()))

	assert.Contains(t, card, `data-krill="task-milepebble"`, "the milepebble is one of the two things it keeps")
	assert.NotContains(t, card, `data-krill="task-badge-`,
		"a Done task with nothing outstanding has no state to badge")
	assert.NotContains(t, card, `data-krill="task-attempts"`,
		"a finished task's attempts are not work left to do")
	assert.NotContains(t, card, `data-krill="task-lease"`,
		"a finished task states no lease")
	assert.Equal(t, 1, strings.Count(card, "<a "), "its title is still the card's one link")

	// The carve-out is about a card with NO live state: a Done task that
	// is escalated still says so, or the board would hide work that needs
	// a human from the lane that says it is finished.
	escalated := done
	escalated.TaskID = uuid.New()
	escalated.Title = "an escalated done card"
	escalated.State = store.TaskStateEscalated
	reason := store.EscalationReasonManual
	escalated.EscalationReason = &reason
	withEscalation := append(append([]store.ProductTaskRow{}, f.rows...), escalated)
	mux2, _ := boardMux(t, withEscalation, len(withEscalation), f.containers)
	lane2 := swimlaneOf(t, boardBody(t, mux2, ""), productTaskNewestMilestone.String())
	assert.Contains(t, cardOf(t, lane2, escalated.TaskID.String()), `data-krill="task-badge-escalated"`,
		"a Done task that is escalated badges it, carve-out or not")
}

// TestBoardCardCarriesNoRelativeLeaseWording is NFR 7b497d92's rule for
// this card: the absolute instant is in the markup, and the relative
// "Lease in 18 min" is the client script's. A relative string inside a
// fragment the Refresh button re-requests would be as old as the response
// and would differ between two identical reads of unchanged state.
func TestBoardCardCarriesNoRelativeLeaseWording(t *testing.T) {
	f := newCardFixture()
	mux := f.mux(t)

	page := boardBody(t, mux, "")
	// The absolute instant is what the server does emit, so its presence
	// is what makes the absence below a statement about the relative form
	// rather than about a card with no lease at all.
	require.Contains(t, page, `datetime="`+f.lease.Format(time.RFC3339)+`"`)
	for _, row := range f.rows {
		card := cardOf(t, swimlaneOf(t, page, productTaskNewestMilestone.String()), row.TaskID.String())
		assert.Empty(t, relativeLeaseRE.FindString(card),
			"%q's card must not carry a server-rendered relative lease", row.Title)
	}

	// The fragment an htmx re-request gets back is the whole of what
	// Refresh swaps in, so it must carry the instant and nothing relative.
	fragment := boardFragment(t, mux)
	assert.Contains(t, fragment, `datetime="`+f.lease.Format(time.RFC3339)+`"`)
	assert.Empty(t, relativeLeaseRE.FindString(fragment),
		"the re-requested fragment must be free of relative lease wording")

	// And the relative wording is produced outside every fragment, by a
	// script in the document head, so a Refresh picks it up without a
	// reload. The script is where the wording lives, which is what makes
	// it the one place that has to say it.
	head := buildHead()
	assert.Contains(t, head, `time[data-krill="task-lease"][datetime]`)
	assert.Contains(t, head, "Lease in ")
	assert.Contains(t, head, "htmx:after:swap")
}

// TestBoardFragmentDiffersOnlyByItsFreshnessInstant is the same rule from
// the other end. Two reads of an unchanged board differ in exactly one
// place -- the "Updated N ago" element's instant, which is meant to move --
// and nowhere else. A server-rendered relative lease would put a second
// difference there, one that grew with the clock rather than with state.
func TestBoardFragmentDiffersOnlyByItsFreshnessInstant(t *testing.T) {
	f := newCardFixture()
	mux := f.mux(t)

	raw := boardFragment(t, mux)
	first := freshnessStripped(raw)
	second := freshnessStripped(boardFragment(t, mux))
	assert.Equal(t, first, second,
		"two reads of an unchanged board must differ only in the freshness instant")
	// Guard: stripping removed something, so the equality above is not
	// passing because the comparison was empty.
	assert.NotEqual(t, raw, first, "the freshness instant must be what was stripped")
}

var updatedAtRE = regexp.MustCompile(
	`data-krill-updated-at="[^"]*"|title="\d{4}-\d{2}-\d{2}T[^"]*"|>\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z<`)

// freshnessStripped is a board fragment with the freshness element's
// instant removed, so two reads of unchanged state can be compared for the
// differences NFR 7b497d92 is about.
func freshnessStripped(fragment string) string {
	return updatedAtRE.ReplaceAllString(fragment, "<instant>")
}

// boardFragment fetches the Board the way the Refresh button and the scope
// control do -- an htmx request -- and returns the fragment the region
// swaps in, which is not the whole shell page.
func boardFragment(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, boardURL(""), nil)
	req.Header.Set("HX-Request", "true")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// TestBoardCardRendersNoWriteAffordance keeps the card read-only (FR
// f41a352d): a card that grew content must not have grown a control that
// changes the task.
func TestBoardCardRendersNoWriteAffordance(t *testing.T) {
	f := newCardFixture()
	body := boardBody(t, f.mux(t), "")
	region := body[strings.Index(body, `<section id="`+pages.ProductTasksAnchor+`"`):strings.Index(body, "</main>")]

	for _, forbidden := range []string{"hx-post", "hx-put", "hx-delete", "draggable", `method="post"`} {
		assert.NotContains(t, region, forbidden, "the board still offers nothing that writes or moves a card")
	}
}
