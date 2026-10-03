// Coverage for FR 82add903: the task detail's properties rail -- the
// Lane/Attempts/Claim/Escalated/Milepebble/id rows, and the Depends-on
// card beside them.
//
// Each case is driven through the real handler, so what it asserts is the
// markup an operator receives rather than the view model's shape. The
// shape has its own cases below, for the values the markup deliberately
// does not render.

package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// errClaimRead is the last-claim read failing, which the rail must survive.
var errClaimRead = errors.New("claim-read-boom")

// propertiesOf is the properties card's own markup, so an assertion about
// one row is about that row and not about whatever else on the page
// happens to carry the same words.
func propertiesOf(t *testing.T, html string) string {
	t.Helper()
	return regionBetween(t, html, `data-krill="task-properties"`, "</dl>")
}

// TestTaskDetailRailShowsEveryPropertyItHas is FR 82add903's positive
// case: a task carrying a claim, an escalation, a milepebble and a task id
// shows all six rows, each with the value the read observed.
//
// The lane and the dependency's lane come through TaskLaneBadge rather
// than a hand-rolled badge, so the rail cannot drift from the Tasks table
// and the Board on either the word or the colour.
func TestTaskDetailRailShowsEveryPropertyItHas(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	escalatedAt := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	f.store.escalation = store.EscalationEvent{ID: esc, Reason: store.EscalationReasonThrashCap, CreatedAt: escalatedAt}

	claim := uuid.New()
	lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	sess := store.SessionID(uuid.New())
	task := f.addOnMilepebble(store.Task{
		Title: "railed-task", CurrentLane: store.LaneTesting, AttemptCount: 2,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: sess, ClaimedAt: time.Now().UTC()}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	props := propertiesOf(t, html)
	for _, want := range []string{
		// Lane, through the shared component.
		`data-krill="task-properties-lane"`,
		`data-krill="task-lane">Testing</span>`,
		// Attempts, "n of cap", with the note about what counts toward it.
		`data-krill="task-properties-attempts"`,
		"2 of 3",
		"lease-lapse and abandon attempts count toward the cap",
		// Claim: the claimant and the lease expiry as a <time>.
		`data-krill="task-properties-claim"`,
		"Claimed by",
		sess.String(),
		`<time data-krill="task-lease" datetime="` + lease.Format(time.RFC3339) + `"`,
		// Escalated: relative on the page, exact on hover.
		`data-krill="task-properties-escalated"`,
		`<time data-krill="task-escalated-at" data-krill-updated-at="` + escalatedAt.Format(time.RFC3339) + `"`,
		`title="` + escalatedAt.Format(time.RFC3339) + `"`,
		// Milepebble, linking at its own task list.
		`data-krill="task-properties-milepebble"`,
		">Cut milepebble</a>",
		`href="` + htmlEscapedURL(productTaskContainerHref(f.pid, tasksSuffix,
			taskContainer{ID: f.mp, Kind: string(store.MilestoneKindMilepebble)})) + `"`,
		// Task id, as a copy chip that carries the id.
		`data-krill="task-properties-id"`,
		`aria-label="Copy task id"`,
		`data-krill="copy-task-id" data-task-id="` + task.ID.String() + `"`,
	} {
		assert.Contains(t, props, want)
	}

	// The escalation's relative form is the head script's to derive: the
	// region carries the absolute instant, never a "3 minutes ago" that is
	// as old as the response and has nothing inside the swap to age it.
	assert.NotContains(t, props, "ago",
		"the server never emits a now-derived relative string inside a fragment htmx may re-swap")
}

// TestTaskDetailRailEscalatedRowIsAbsentWhenNotEscalated is FR
// 82add903's "only when escalated": a task with no escalation renders no
// row, rather than a blank one that reads as an escalation whose instant
// could not be read.
func TestTaskDetailRailEscalatedRowIsAbsentWhenNotEscalated(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "quiet-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	props := propertiesOf(t, html)
	assert.NotContains(t, props, "task-properties-escalated")
	assert.NotContains(t, props, "task-escalated-at")
	assert.NotContains(t, props, ">Escalated</dt>")
}

// TestTaskDetailRailMilepebbleRowIsAbsentOnAnUncutMilestone is the other
// conditional row: a task on a milestone that was never cut has no
// milepebble, and a row naming its own milestone again would say what the
// breadcrumb already says.
func TestTaskDetailRailMilepebbleRowIsAbsentOnAnUncutMilestone(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "uncut-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	props := propertiesOf(t, html)
	assert.NotContains(t, props, "task-properties-milepebble")
	assert.NotContains(t, props, ">Milepebble</dt>")
}

// TestTaskDetailRailClaimNamesTheLastHolderWhenUnclaimed is the clause
// GetClaimByID could not answer: once a claim is released, task.current_claim_id
// is NULL and the only read that still names a holder is the newest claim
// row for the task.
//
// This is the shape the rail meets most often -- a task sitting claimable,
// the moment an operator most wants to know whether anyone touched it.
func TestTaskDetailRailClaimNamesTheLastHolderWhenUnclaimed(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "released-task", CurrentLane: store.LaneTesting})
	released := time.Now().Add(-time.Hour).UTC()
	lastSession := store.SessionID(uuid.New())
	reason := "complete"
	f.store.lastClaim = store.Claim{
		ID: uuid.New(), TaskID: task.ID, SessionID: lastSession,
		ClaimedAt: released, ReleasedAt: &released, ReleaseReason: &reason,
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	props := propertiesOf(t, html)
	claim := regionBetween(t, props, `data-krill="task-properties-claim"`, "</dd>")
	assert.Contains(t, claim, "None. Last held by")
	assert.Contains(t, claim, lastSession.String())
	assert.NotContains(t, claim, "Claimed by",
		"a released claim must not read as a live one")
	// And no lease: the task holds none, so there is no instant to state.
	assert.NotContains(t, html, `data-krill="task-lease"`,
		"an unclaimed task renders no lease element at all")
}

// TestTaskDetailRailClaimOnANeverClaimedTask is the other unclaimed shape:
// there is no claim row at all, so there is nobody to have held it last
// and the row says so rather than naming an empty holder.
func TestTaskDetailRailClaimOnANeverClaimedTask(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "untouched-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	claim := regionBetween(t, html, `data-krill="task-properties-claim"`, "</dd>")
	assert.Contains(t, claim, "never been claimed")
	assert.NotContains(t, claim, "Last held by")
}

// TestTaskDetailRailClaimReadFailureKeepsTheRow: the last-claim read is one
// clause of one row, so failing it must cost the operator that clause and
// not the page -- and must not be shown the store's error.
func TestTaskDetailRailClaimReadFailureKeepsTheRow(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "half-readable-claim", CurrentLane: store.LaneTesting})
	f.store.lastClaimErr = errClaimRead

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	claim := regionBetween(t, html, `data-krill="task-properties-claim"`, "</dd>")
	assert.Contains(t, claim, "None.",
		"the task holds no claim, whatever the last-holder read could not say")
	assert.NotContains(t, html, "claim-read-boom")
}

// TestTaskDetailRailLeaseIsATimeElement pins the lease row's timestamp
// shape on this view specifically: datetime is the instant, title repeats
// it for hover, and the element's own text is that same absolute instant,
// so an operator with scripting off still reads when the lease runs out.
func TestTaskDetailRailLeaseIsATimeElement(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	task := f.add(store.Task{
		Title: "leased-task", CurrentLane: store.LaneTesting,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease,
	})
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	elems := leaseTimeElements(html)
	require.Len(t, elems, 1, "the rail states the lease once, or not at all:\n%s", html)
	instant := lease.Format(time.RFC3339)
	assert.Equal(t, instant, elems[0].attr("datetime"))
	assert.Equal(t, instant, elems[0].attr("title"))
	assert.Equal(t, instant, elems[0].text)
}

// TestTaskDetailRailMarksAnExpiredClaimNotLive: a claim whose lease has
// lapsed is still the task's current claim id, so the rail names the
// holder -- and says the lease is gone, because a task whose worker has
// left is not being worked on.
func TestTaskDetailRailMarksAnExpiredClaimNotLive(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	past := time.Now().Add(-time.Hour).UTC()
	task := f.add(store.Task{
		Title: "stale-task", CurrentLane: store.LaneTesting,
		CurrentClaimID: &claim, LeaseExpiresAt: &past,
	})
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	props := propertiesOf(t, html)
	assert.Contains(t, props, "lease expired, not live")
	assert.Contains(t, html, `data-krill="task-badge-lease-expired"`)
}

// TestTaskDetailDependsOnCardListsEachDependency is the Depends-on card:
// one entry per dependency, each a link to its own detail carrying its
// lane as a badge, in the order ListDependencies returned (declaration
// order -- the order an operator reading a blocked task needs).
func TestTaskDetailDependsOnCardListsEachDependency(t *testing.T) {
	f := newDetailFixture(t)
	first := f.add(store.Task{Title: "first-dep", CurrentLane: store.Lane("Done")})
	second := f.add(store.Task{Title: "second-dep", CurrentLane: store.LaneTesting})
	task := f.add(store.Task{Title: "blocked-task", CurrentLane: store.LaneTesting})
	f.store.deps = []store.TaskDependency{
		{DependsOnTaskID: first.ID},
		{DependsOnTaskID: second.ID},
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := regionBetween(t, html, `data-krill="task-depends-on"`, "</ul>")
	assert.Less(t, strings.Index(card, ">first-dep</a>"), strings.Index(card, ">second-dep</a>"),
		"declaration order is the order the store returned")
	assert.Contains(t, card, `href="`+htmlEscapedURL(taskDetailPath(f.pid, first.MilestoneID, first.ID))+`"`)
	assert.Contains(t, card, `href="`+htmlEscapedURL(taskDetailPath(f.pid, second.MilestoneID, second.ID))+`"`)
	assert.Contains(t, card, `data-krill="task-lane">Done</span>`)
	assert.Contains(t, card, `data-krill="task-lane">Testing</span>`)
	assert.Equal(t, 2, strings.Count(card, `data-krill="task-depends-on-item"`),
		"one entry per dependency, never one per link plus one for the list")
}

// TestTaskDetailDependsOnCardDoesNotRequery: the card reads the titles and
// lanes the composed DepTasks map already carries. A dependency the map
// missed still renders as a link -- to its own id, since that is all the
// page knows -- rather than vanishing.
func TestTaskDetailDependsOnCardSurvivesAnUnreadDependency(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "half-readable-deps", CurrentLane: store.LaneTesting})
	orphan := uuid.New()
	f.store.deps = []store.TaskDependency{{DependsOnTaskID: orphan}}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := regionBetween(t, html, `data-krill="task-depends-on"`, "</ul>")
	assert.Contains(t, card, orphan.String(),
		"a dependency the page could not resolve is still named and still linked")
}

// TestTaskDetailRailKeepsTheObservedClaimIdentity: the region carries the
// claim id and lease the read observed, so a later claim-guarded write can
// refuse if the claim moved underneath it. The rail is read-only, so
// nothing here may carry an hx-post or sit inside a form.
func TestTaskDetailRailKeepsTheObservedClaimIdentity(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	lease := time.Now().Add(10 * time.Minute).UTC().Truncate(time.Second)
	task := f.add(store.Task{
		Title: "observed-task", CurrentLane: store.LaneTesting,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease,
	})
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	code, frag := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", frag)

	assert.Contains(t, frag, `data-krill-claim-id="`+claim.String()+`"`)
	assert.Contains(t, frag, `data-krill-lease-expires-at="`+lease.Format(time.RFC3339)+`"`)

	rail := regionBetween(t, frag, `data-krill="task-properties-rail"`, "</aside>")
	assert.NotContains(t, rail, "<form")
	for _, verb := range []string{"hx-post", "hx-put", "hx-delete"} {
		assert.NotContains(t, rail, verb, "the rail is read-only; a %s here is a write", verb)
	}
}

// TestTaskDetailRailIsOneRenderingOfEachProperty is the deduplication the
// rail exists to force: Lane, Attempts, Claim and Escalated each render
// once on the page, not once in the rail and once in the content column.
// Two renderings of one value are two things that can drift.
func TestTaskDetailRailIsOneRenderingOfEachProperty(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	claim := uuid.New()
	lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	task := f.add(store.Task{
		Title: "one-of-each", CurrentLane: store.LaneTesting,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})
	f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: time.Now().UTC()}
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	main := regionBetween(t, html, `data-krill="task-detail-main"`, `data-krill="task-properties-rail"`)
	for _, moved := range []string{"Current lane", "Attempts", "Escalation", "Claim"} {
		assert.NotContains(t, main, ">"+moved+"</dt>",
			"the rail is the one rendering of %s, not a second one beside it", moved)
	}
	assert.Equal(t, 1, strings.Count(html, `data-krill="task-properties-attempts"`))
	assert.Equal(t, 1, strings.Count(html, `data-krill="task-properties-claim"`))
	assert.Equal(t, 1, strings.Count(html, `data-krill="task-properties-escalated"`))
	assert.Equal(t, 1, strings.Count(html, `data-krill="task-properties-lane"`))
}

// TestTaskDetailRailViewModelSplitsTheClaimRow: the rail needs the holder,
// the lapse and the last holder as three separate facts, because "claimed
// by X" and "None. Last held by X" must be mutually exclusive rather than
// two strings composed from one condition.
func TestTaskDetailRailViewModelSplitsTheClaimRow(t *testing.T) {
	f := newDetailFixture(t)
	claimID := uuid.New()
	claimed := f.add(store.Task{Title: "claimed-task", CurrentLane: store.LaneTesting, CurrentClaimID: &claimID})
	page := detailPageOfFixture(t, taskDetailInputs{
		Task:  claimed,
		Claim: &store.Claim{SessionID: store.SessionID(uuid.New())},
	})
	assert.NotEmpty(t, page.ClaimID)
	assert.NotEmpty(t, page.ClaimHolder)
	assert.Empty(t, page.LastClaimHolder,
		"a task holding a claim has no last holder to name")

	idle := f.add(store.Task{Title: "idle-task", CurrentLane: store.LaneTesting})
	page = detailPageOfFixture(t, taskDetailInputs{
		Task:      idle,
		LastClaim: &store.Claim{SessionID: store.SessionID(uuid.New())},
	})
	assert.Empty(t, page.ClaimID)
	assert.Empty(t, page.ClaimHolder)
	assert.NotEmpty(t, page.LastClaimHolder)
}

// TestTaskDetailRailMilepebbleRowIsCarriedOnlyForAMilepebble: the row's
// value is a view-model fact, so the uncut case is decided once in the
// builder rather than by the template re-deriving the container's kind.
func TestTaskDetailRailMilepebbleRowIsCarriedOnlyForAMilepebble(t *testing.T) {
	pid := uuid.New()

	pebble := taskDetailPageOf(pid, pages.ProductHeader{Name: "P"},
		taskContainer{ID: uuid.New(), Name: "Pebble", Kind: string(store.MilestoneKindMilepebble)},
		taskDetailInputs{Task: store.Task{ID: uuid.New(), CurrentLane: store.LaneTesting}},
		time.Now())
	assert.Equal(t, "Pebble", pebble.MilepebbleName)
	assert.NotEmpty(t, pebble.MilepebblePath)
	assert.Equal(t, pebble.TasksPath, pebble.MilepebblePath,
		"the row links at the same list the breadcrumb does")

	plain := taskDetailPageOf(pid, pages.ProductHeader{Name: "P"},
		taskContainer{ID: uuid.New(), Name: "Milestone", Kind: string(store.MilestoneKindMilestone)},
		taskDetailInputs{Task: store.Task{ID: uuid.New(), CurrentLane: store.LaneTesting}},
		time.Now())
	assert.Empty(t, plain.MilepebbleName)
	assert.Empty(t, plain.MilepebblePath)
}