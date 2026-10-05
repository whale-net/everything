// The Claimed tab's contract (FR a149d28f): the row's columns -- task,
// milestone, lane, claimant, claimed-since, lease expiry and the three
// actions legal for a claimed task -- the claim id each row carries for its
// actions' guard, and the self-terminating poll that watches a lease near
// expiry and stops by itself once none is.
//
// The fixture is the Needs attention page's own (newNeedsAttentionFixture,
// krill/ui/needs_attention_test.go): the page's real shell, routes and
// store surface, held at a fixed clock, so a rendered row is the row the
// page's own read returned rather than a restatement of it.
package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

// claimedTabRow is the store row the tab is asked to render: one claimed
// task, held by "swarm worker-3 (human)" for "kc alex", in the
// Implementation lane, with a lease an hour out and a claim id to guard on.
func claimedTabRow() store.ClaimedTaskRow {
	return store.ClaimedTaskRow{
		TaskID:             uuid.MustParse("aaaaaaaa-1111-2222-3333-444444444444"),
		Title:              "Paginate ListClaimedTasks",
		DeliveryRef:        store.ClaimedTaskDeliveryRef{Kind: store.MilestoneKindMilestone, Title: "M5 Escalation console"},
		ClaimantActing:     store.Subject{Iss: "swarm", Sub: "worker-3", Kind: store.SubjectKindHuman},
		ClaimantOnBehalfOf: store.Subject{Iss: "kc", Sub: "alex"},
		CurrentLane:        store.LaneImplementation,
		ClaimedAt:          needsAttentionNow.Add(-42 * time.Minute),
		LeaseExpiresAt:     needsAttentionNow.Add(time.Hour),
		ClaimID:            uuid.MustParse("bbbbbbbb-1111-2222-3333-444444444444"),
	}
}

// claimedTabPage fetches the Claimed tab as a browser would: the whole
// page, shell and strip included.
func claimedTabPage(t *testing.T, f *needsAttentionFixture) string {
	t.Helper()
	return fetch(t, f.mux, f.path(needsAttentionTabClaimed)).Body.String()
}

// claimedResultsBlockID is the id the claimed results block carries, spelled
// as a literal here so the polls these tests issue name the target the
// markup renders independently of the production constant.
const claimedResultsBlockID = "ops-results"

// claimedTabPoll is one poll of the tab: an htmx request naming the results
// block, which is exactly the request the fragment's own hx-target makes.
func claimedTabPoll(t *testing.T, f *needsAttentionFixture) *httptest.ResponseRecorder {
	t.Helper()
	return fetchHX(t, f.mux, f.path(needsAttentionTabClaimed), claimedResultsBlockID)
}

// ---------------------------------------------------------------------------
// 1. the row's columns
// ---------------------------------------------------------------------------

// TestClaimedTabRendersTheFRsColumns walks the FR's column list against one
// rendered row: Task (the title, linking to the product-scoped detail
// page), Milestone, the lane badge, the claimant as "by X for Y",
// claimed-since, the lease expiry, and the actions.
func TestClaimedTabRendersTheFRsColumns(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := claimedTabRow()
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	body := claimedTabPage(t, f)

	for _, header := range []string{
		"Task", "Milestone", "Lane", "Claimant", "Claimed since", "Lease expires", "Actions",
	} {
		assert.Contains(t, body, "<th>"+header+"</th>", "the %s column is offered", header)
	}

	assert.Contains(t, body, row.Title, "the task title is rendered")
	assert.Contains(t, body, `href="`+productTaskDetailPath(f.pid, row.TaskID)+`"`,
		"the title links to the product-scoped detail page")
	assert.Contains(t, body, row.DeliveryRef.Title, "the milestone is rendered")
	assert.Contains(t, body, `data-krill="task-lane"`, "the lane renders through the shared lane badge")
	assert.Contains(t, body, `>Implementation</span>`, "the lane badge names the lane")

	assert.Contains(t, body, "by swarm worker-3 (human) for kc alex",
		"the claimant column reads 'by <acting> for <on-behalf-of>'")
	assert.Contains(t, body, opsTime(row.ClaimedAt), "claimed-since is the claim's own instant")
	assert.Contains(t, body, opsTime(row.LeaseExpiresAt), "the lease expiry is rendered")

	// The row's actions, all three, since a claimed task in a working lane
	// can be released, escalated or cancelled.
	assert.Contains(t, body, ">Release</button>")
	assert.Contains(t, body, ">Escalate</button>")
	assert.Contains(t, body, ">Cancel</button>")
}

// TestClaimedTabLeaseIsAnExactInstantTheBrowserMakesRelative is how the FR's
// "lease expiry as a relative time (exact on hover)" is met without putting
// a shifting string in a polled fragment.
//
// The server renders the exact UTC instant as the element's content, its
// datetime and its title, on the very element the head's leaseCountdownScript
// selects (time[data-krill="task-lease"][datetime]); the script is what
// turns it into "Lease in 18 minutes" in the browser, and the title is what
// answers "exactly when" on hover. Two polled responses for one unchanged
// state are therefore byte-identical, which a server-rendered "in 3m" would
// have made impossible.
func TestClaimedTabLeaseIsAnExactInstantTheBrowserMakesRelative(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := claimedTabRow()
	f.tasks.claimed = []store.ClaimedTaskRow{row}
	exact := opsTime(row.LeaseExpiresAt)

	body := claimedTabPage(t, f)

	assert.Contains(t, body, `data-krill="task-lease" datetime="`+exact+`"`,
		"the lease element is the one the head script upgrades")
	assert.Contains(t, body, `title="`+exact+`"`, "the exact instant is the hover title")
	assert.Contains(t, body, `>`+exact+`</time>`,
		"and it is the element's own server-rendered text, so the page reads without JavaScript")
	assert.Contains(t, body, `time[data-krill="task-lease"][datetime]`,
		"the shell ships the script that derives the relative form")

	// Nothing in the polled fragment is a relative string: a "3 min ago"
	// would differ between two reads of the same state.
	fragment := claimedTabPoll(t, f).Body.String()
	assert.NotContains(t, fragment, "ago", "no shifting relative text is rendered into the polled fragment")
	assert.Contains(t, fragment, exact, "the fragment carries the instant itself")
}

// ---------------------------------------------------------------------------
// 2. the claim id each row carries
// ---------------------------------------------------------------------------

// TestClaimedRowCarriesTheObservedClaimIDOnEveryAction is the FR's "each row
// carries the claim id it observed for its actions' guard": the row states it
// once, and each of the three controls posts it back -- Release and Escalate
// as a hidden field on their own forms, and Cancel as a hidden field on BOTH
// halves of its doubled control, so the htmx half posts it to the cancel route
// and the no-JS half hands it to the confirmation page, whose own form posts
// it on from there.
func TestClaimedRowCarriesTheObservedClaimIDOnEveryAction(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := claimedTabRow()
	f.tasks.claimed = []store.ClaimedTaskRow{row}
	observed := row.ClaimID.String()

	body := claimedTabPage(t, f)

	assert.Contains(t, body, `data-krill-claim-id="`+observed+`"`, "the row states the claim it observed")
	assert.Equal(t, 3, strings.Count(body, `name="expected_claim_id" value="`+observed+`"`),
		"all three controls (Release, Escalate and Cancel) carry it as a hidden field")
	assert.Contains(t, body, `hx-post="/ops/tasks/`+row.TaskID.String()+`/cancel"`,
		"Cancel's htmx half posts the cancel route")
	assert.Contains(t, body, `action="/ops/tasks/`+row.TaskID.String()+`/cancel/confirm"`,
		"and its no-JS half opens the confirmation page")
}

// ---------------------------------------------------------------------------
// 3. the actions' legality
// ---------------------------------------------------------------------------

// TestClaimedDoneLaneRowOffersOnlyRelease pins the FR's legality rule: a
// claimed task in the Done lane is offered neither Escalate nor Cancel --
// there is nothing to flag for attention and nothing to dead-letter about a
// task that already finished -- while Release stays available, because the
// task still holds a claim.
func TestClaimedDoneLaneRowOffersOnlyRelease(t *testing.T) {
	for _, tc := range []struct {
		name         string
		lane         store.Lane
		wantEscalate bool
		wantCancel   bool
	}{
		{name: "Done lane", lane: store.LaneDone},
		{name: "working lane", lane: store.LaneImplementation, wantEscalate: true, wantCancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNeedsAttentionFixture(t)
			row := claimedTabRow()
			row.CurrentLane = tc.lane
			f.tasks.claimed = []store.ClaimedTaskRow{row}

			body := claimedTabPage(t, f)

			assert.Contains(t, body, ">Release</button>", "a claimed task is always releasable")
			if tc.wantEscalate {
				assert.Contains(t, body, ">Escalate</button>")
			} else {
				assert.NotContains(t, body, ">Escalate</button>", "a Done-lane task offers no Escalate")
			}
			if tc.wantCancel {
				assert.Contains(t, body, ">Cancel</button>")
			} else {
				assert.NotContains(t, body, ">Cancel</button>", "a Done-lane task offers no Cancel")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 4. the self-terminating poll
// ---------------------------------------------------------------------------

// TestClaimedTabPollsOnlyWhileALeaseIsNearExpiry is the FR's second
// paragraph: a lease within one third of the default lease duration of
// expiry arms the poll, and once no row is that close the fragment comes
// back without the attributes -- so the loop stops by itself, with no
// client-side bookkeeping, because the poll's own response is this
// fragment.
//
// The boundary is the FR's own: a lease exactly one third of the default
// lease duration away still polls, a minute further out does not.
func TestClaimedTabPollsOnlyWhileALeaseIsNearExpiry(t *testing.T) {
	for _, tc := range []struct {
		name     string
		lease    time.Time
		wantPoll bool
	}{
		{
			name:     "lease within one third of the lease of expiry",
			lease:    needsAttentionNow.Add(store.DefaultLeaseDuration / 3),
			wantPoll: true,
		},
		{
			name:     "lease comfortably held",
			lease:    needsAttentionNow.Add(store.DefaultLeaseDuration/3 + time.Minute),
			wantPoll: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newNeedsAttentionFixture(t)
			row := claimedTabRow()
			row.LeaseExpiresAt = tc.lease
			f.tasks.claimed = []store.ClaimedTaskRow{row}

			fragment := claimedTabPoll(t, f).Body.String()

			if tc.wantPoll {
				assert.Contains(t, fragment, `hx-get="`+f.path(needsAttentionTabClaimed)+`"`,
					"the poll re-requests the tab's own URL, not a fixed route")
				assert.Contains(t, fragment, `hx-trigger="every 3s"`)
				assert.Contains(t, fragment, `hx-swap="outerHTML"`)
			} else {
				assert.NotContains(t, fragment, "hx-trigger",
					"a settled fragment carries no poll attributes, so the loop stops by itself")
			}
			// Either way the fragment is the results block alone -- the
			// strip, whose freshness stamp moves every read, never travels
			// with a poll.
			assert.Contains(t, fragment, `id="`+claimedResultsBlockID+`"`)
			assert.NotContains(t, fragment, "needs-attention-updated-at")
		})
	}
}

// TestClaimedTabPollResponsesAreByteIdenticalForUnchangedState is the same
// rule from the operator's side: while the poll is armed, two reads of an
// unchanged queue must be the same bytes even though the clock moved between
// them -- which is exactly the situation a polled fragment is in, since a
// poll is a second read of a state that did not change.
//
// The clock is deliberately advanced between the two reads. A fragment that
// rendered anything derived from the read instant -- a relative "3 min ago",
// a generated id, a map's order -- would differ, and the operator's page
// would then rewrite itself every three seconds for no reason.
func TestClaimedTabPollResponsesAreByteIdenticalForUnchangedState(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := claimedTabRow()
	// Inside the horizon at both reads, so the fragment really is the polled
	// one and the poll decision itself does not change between them.
	row.LeaseExpiresAt = needsAttentionNow.Add(store.DefaultLeaseDuration / 3)
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	at := needsAttentionNow
	f.app.now = func() time.Time { return at }

	first := claimedTabPoll(t, f)
	at = at.Add(90 * time.Second) // a poll interval or two later
	second := claimedTabPoll(t, f)

	assert.Equal(t, http.StatusOK, first.Code, "a poll is always answered at 200")
	assert.Contains(t, first.Body.String(), `hx-trigger="every 3s"`, "the fixture really is polling")
	assert.Equal(t, first.Body.String(), second.Body.String(),
		"an unchanged claimed tab must render the same bytes twice, a moving clock or not")
}

// TestClaimedTabRefreshStaysAvailableWhilePolling is the FR's "per-region
// Refresh stays on every region and this poll is the only poll on the
// page": the automatic poll is not a replacement for the manual control,
// and the page carries no second timer.
func TestClaimedTabRefreshStaysAvailableWhilePolling(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := claimedTabRow()
	row.LeaseExpiresAt = needsAttentionNow.Add(store.DefaultLeaseDuration / 3)
	f.tasks.claimed = []store.ClaimedTaskRow{row}

	body := claimedTabPage(t, f)

	assert.Contains(t, body, ">Refresh<", "the manual Refresh control is still offered")
	assert.Equal(t, 1, strings.Count(body, `hx-trigger="every 3s"`),
		"the claimed fragment is the page's only poll")
}
