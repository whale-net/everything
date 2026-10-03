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
	"fmt"
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

// errClaimRead is a claim read failing, which the rail must survive; it
// stands for both GetClaimByID and LatestClaimForTask, whose failures cost
// the row one clause each and never the page.
var errClaimRead = errors.New("claim-read-boom")

// errDepsRead is the dependencies read failing, which is the one read whose
// failure changes the rail's SHAPE rather than one row's content: the
// Depends-on card has to render, because it cannot otherwise distinguish
// "no dependencies" from "could not tell".
var errDepsRead = errors.New("deps-read-boom")

// errEscalationRead is the escalation-event read failing. It is separated
// from errClaimRead because the two cost different rows different clauses,
// and a sweep that conflated them would pass against a page that got one of
// them right.
var errEscalationRead = errors.New("escalation-read-boom")

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
//
// The card renders through the same list the Dependencies tab does, which
// is why it carries the tab's item hook rather than one of its own: two
// components would be two lists that could drift.
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
	// The product-scoped detail, the same address the Tasks table's rows
	// link to -- not the retired per-container form, which would name a
	// container the dependency need not live in.
	assert.Contains(t, card, `href="`+htmlEscapedURL(productTaskDetailPath(f.pid, first.ID))+`"`)
	assert.Contains(t, card, `href="`+htmlEscapedURL(productTaskDetailPath(f.pid, second.ID))+`"`)
	assert.Contains(t, card, `data-krill="task-lane">Done</span>`)
	assert.Contains(t, card, `data-krill="task-lane">Testing</span>`)
	assert.Equal(t, 2, strings.Count(card, `data-krill="task-dep"`),
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

// TestTaskDetailRailClaimHolderReadFailureNamesNobody is the degraded half
// of the live-claim shape, and the one the Implementation pass did not cover:
// GetClaimByID is a read like any other, and when it fails the task still
// holds a claim -- the task row says so, and so does the region's
// data-krill-claim-id, which is what a later claim-guarded write is checked
// against.
//
// What the rail must not do is print "Claimed by" followed by an empty span.
// That reads as "claimed by nobody", which is a different and untrue claim
// from "we could not read who holds it", and it is the exact hole the
// Escalated row's unreadable-instant branch exists to avoid.
func TestTaskDetailRailClaimHolderReadFailureNamesNobody(t *testing.T) {
	f := newDetailFixture(t)
	claim := uuid.New()
	lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	task := f.add(store.Task{
		Title: "unreadable-holder", CurrentLane: store.LaneTesting,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease,
	})
	f.store.claimErr = errClaimRead

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	claimRow := regionBetween(t, html, `data-krill="task-properties-claim"`, "</dd>")
	assert.NotContains(t, claimRow, "claim-read-boom",
		"the store's error is not the operator's to read")
	// The claim is still the task's -- the region attribute is the
	// claim-guarded write's input and must not be blanked by this read.
	assert.Contains(t, html, `data-krill-claim-id="`+claim.String()+`"`,
		"a claim read that failed does not unclaim the task")
	// And whatever the row says, it must not name an empty holder. The
	// subject is the third argument and the message the fourth: read the
	// other way round, the regex is matched against the message and the
	// assertion passes on a row that does exactly this.
	assert.NotRegexp(t, `<span class="font-mono text-xs"></span>`, claimRow,
		"an empty holder span renders a value the page could not read as though it were one:\n%s", claimRow)
	assert.NotRegexp(t, `Claimed by\s*</span>`, claimRow,
		"'Claimed by' with nothing after it reads as claimed-by-nobody:\n%s", claimRow)
}

// TestTaskDetailRailEscalatedTimeIsUpgradedNotJustCarried is the JS-on half
// of FR 82add903's "relative time, exact on hover".
//
// The element carrying the instant is only half the clause: the head's
// relativeAgeScript keys on the [data-krill-updated-at] selector, and a
// markup assertion that only checked the attribute would pass against an
// element the script's selector no longer matches -- which renders the
// absolute instant forever, looking correct and never going relative. The
// selector is therefore read off the script itself rather than restated here,
// so a change to one is a change to both.
func TestTaskDetailRailEscalatedTimeIsUpgradedNotJustCarried(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	escalatedAt := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: escalatedAt}
	task := f.add(store.Task{
		Title: "aged-task", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc,
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	// The script the page serves is the one that does the upgrade, so the
	// selector is whatever the shipped script selects on.
	assert.Contains(t, relativeAgeScript, "[data-krill-updated-at]",
		"the head script's selector changed; this test's selector must change with it")
	row := regionBetween(t, html, `data-krill="task-properties-escalated"`, "</dd>")
	assert.Contains(t, row, "data-krill-updated-at",
		"the escalated row's instant is invisible to relativeAgeScript without this attribute")

	// The exact-on-hover half: the absolute instant is on the title, so an
	// operator who wants the precise time gets it whether or not the upgrade
	// ran.
	assert.Contains(t, row, `title="`+escalatedAt.Format(time.RFC3339)+`"`)
	// And with scripting off the element's own text is still the instant, not
	// an empty node the reader has nothing for.
	assert.Contains(t, row, ">"+escalatedAt.Format(time.RFC3339)+"</time>")
}

// TestTaskDetailRailCopyChipIsAnAccessibleControlBoundFromTheHead pins the
// copy chip as an accessible control carrying the id whose behaviour comes
// from the head, never from the markup.
//
// The intent is the one this test always had: the chip must not claim an
// affordance the page does not honour. It used to assert the behaviour was
// entirely absent; it now asserts the same thing a year-scheme-correct way --
// the clipboard write exists, but only as the head script that
// copyTaskIdScript installs, and nothing here does it inline. An onclick, an
// htmx verb, or a script inside the rail would each be a second copy of the
// behaviour that dies with the next swap while the head's survives.
func TestTaskDetailRailCopyChipIsAnAccessibleControlBoundFromTheHead(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "chip-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	// The row rather than the hook: the hook sits INSIDE the opening tag, so
	// a region cut at it would not contain the tag and every assertion about
	// the element's own attributes would be about markup outside the slice.
	row := regionBetween(t, html, `data-krill="task-properties-id"`, "</dd>")
	// A real control: a button, explicitly type=button so it can never
	// submit an enclosing form, with an accessible name and the id in both
	// the attribute the behaviour reads and the text the operator reads.
	assert.Contains(t, row, "<button type=\"button\"")
	assert.Contains(t, row, `aria-label="Copy task id"`)
	assert.Contains(t, row, `data-krill="copy-task-id"`)
	assert.Contains(t, row, `data-task-id="`+task.ID.String()+`"`)
	assert.Contains(t, row, ">"+task.ID.String()+"</button>",
		"the chip's own text is the id, so the control reads as what it copies")

	// No behaviour IN the markup. It all lives in the head, so the chip
	// survives a swap as dead markup the head re-wires, rather than carrying
	// an inline handler the swap strips.
	assert.NotContains(t, row, "onclick", "the clipboard write is the head script's, never inline")
	assert.NotContains(t, row, "navigator.clipboard",
		"the chip must not carry its own clipboard call alongside the head script's")
	for _, verb := range []string{"hx-post", "hx-put", "hx-delete", "hx-trigger", "hx-get"} {
		assert.NotContains(t, row, verb,
			"copying is a client-side clipboard write; a %s here would make this page a writer", verb)
	}
	// And the rail ships no script of its own, so the behaviour that binds
	// this chip has to arrive in the head rather than inline here.
	rail := regionBetween(t, html, `data-krill="task-properties-rail"`, "</aside>")
	assert.NotContains(t, rail, "<script",
		"a script inside the rail would be dropped by every htmx swap that re-renders it")

	// Whole-fragment, not just the row: the test fetches the HX fragment
	// (hx=true), which never renders buildHead, so the fragment carries no
	// clipboard call of any kind and this holds without exception. A second
	// inline clipboard handler on a DIFFERENT row -- an onclick, say, on the
	// claim -- is exactly the defect the row-scoped check above cannot see
	// and that no other test in this file catches.
	assert.NotContains(t, html, "navigator.clipboard",
		"the fragment carries no clipboard call at all: the behaviour is the head's, and the head is not in this fragment")

	// The head really does carry it, so the assertions above are not
	// satisfied by a page that simply has a dead chip. buildHead is the
	// seam the shipped shell renders, and the fragment served above has no
	// head of its own to check.
	//
	// The whole script, not a word from it: "navigator.clipboard" alone is
	// satisfied by any head carrying ANY clipboard call, so a build that
	// dropped copyTaskIdScript and grew an unrelated one would pass. The
	// negative half above is only defensible because this half is exact.
	head := buildHead()
	assert.Contains(t, head, copyTaskIdScript,
		"the head must carry copyTaskIdScript itself, or this chip never copies anything")
}

// TestTaskDetailRailCopyChipIsLiveOnlyBecauseTheHeadSaysSo is the audit of
// the sweep's narrowing, stated as its own test rather than left to be
// inferred from two neighbours.
//
// The rail sweep used to assert the whole rail contains no
// "navigator.clipboard". That was correct while the chip had no behaviour,
// and it became wrong the moment the head script arrived: the page has to
// contain a clipboard call somewhere, and narrowing the sweep to "no inline
// clipboard" is only honest if something else makes a DEAD chip fail. This
// is that something else, and it is two claims joined:
//
//  1. the head carries the script, whole; and
//  2. the script is what removes the chip's disabled.
//
// Together they mean the chip cannot be both disabled on arrival and without
// an enabler: the state is changed by one specific, asserted piece of code.
// Neither half alone is enough -- a head carrying the script while the chip
// ships enabled-but-unwired is a live-looking dead control, and a chip whose
// disabled is removed by markup with no script behind it is dead with the
// disabled attribute gone.
func TestTaskDetailRailCopyChipIsLiveOnlyBecauseTheHeadSaysSo(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "chip-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	// 1. the enabler is in the head, whole.
	head := buildHead()
	require.Contains(t, head, copyTaskIdScript,
		"without the head's copy script the chip below can never be enabled")

	// 2. the enabler is what flips it, and the flip is inside the bind that
	// every fresh chip from a swap goes through.
	require.Contains(t, copyTaskIdScript, "btn.removeAttribute('disabled')",
		"the script has to be what removes disabled; nothing else does")

	// And the chip really is disabled on arrival -- otherwise the pair above
	// describes a control that is live regardless, and the whole
	// enable-the-chip contract is decorative.
	row := regionBetween(t, html, `data-krill="task-properties-id"`, "</dd>")
	require.Contains(t, row, `" disabled title="`,
		"the chip must ship disabled for the head's enable to mean anything")

	// The swap path, joined up: a chip swapped in after page load arrives in
	// exactly the server-rendered state asserted above, so the after:swap
	// listener is the only thing that revives it, and it has to reach the
	// swapped subtree rather than only the initial document.
	assert.Contains(t, copyTaskIdScript, "document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});",
		"a swapped-in chip arrives disabled and unbound; only an after:swap upgrade over the "+
			"swapped subtree can revive it")
}

// TestTaskDetailRailCopyChipIsDisabledUntilTheHeadEnablesIt is the
// no-JavaScript half, and the reason the chip ships disabled.
//
// With scripting off the clipboard API is never reachable, so an enabled
// button would be a control that looks live and cannot work: an operator
// clicks it, nothing happens, and the absence of a confirmation reads as
// "the id is gone" rather than "this page has no clipboard". Disabled with
// the reason in the title states it, and the head's script is what removes
// disabled -- so the assertion that matters is both halves together: the
// server renders it dead, and the script revives it.
func TestTaskDetailRailCopyChipIsDisabledUntilTheHeadEnablesIt(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "chip-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-id"`, "</dd>")
	// Anchored on the attribute's position in the tag, not on the bare word:
	// the chip also carries a `disabled:opacity-50` utility class, and a
	// looser containment check would match that and pass against a chip that
	// is very much still live.
	assert.Contains(t, row, `" disabled title="Copying the task id needs JavaScript"`,
		"the chip ships disabled: without the head script it is guaranteed to do nothing, and a "+
			"control that cannot work must not look live")
	// The id survives the degradation as text -- which is the whole point of
	// keeping it on the button rather than behind the clipboard.
	assert.Contains(t, row, ">"+task.ID.String()+"</button>",
		"with scripting off the id must still be readable on the page")

	// And the head is what makes it work again. Without this half the chip
	// is permanently dead and the disabled title becomes a lie.
	assert.Contains(t, copyTaskIdScript, "btn.removeAttribute('disabled')",
		"the head script must be what enables the chip, or the server-rendered disabled state "+
			"is permanent: %s", copyTaskIdScript)
}

// TestTaskDetailRailCopyConfirmationIsAPoliteLiveRegion guards the
// confirmation's accessibility contract: announced to a screen reader
// without interrupting whatever the operator was doing, positioned beside
// the chip rather than over the id it confirms, and blank until there is
// something to say.
//
// The last part is the one that looks like a defect and is not. A live region
// has to exist before its content changes or the change is not announced, so
// this span is the single deliberately-empty element on the page. It is not a
// property row and its blankness is not the "value the page could not read"
// that rule is about.
func TestTaskDetailRailCopyConfirmationIsAPoliteLiveRegion(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "chip-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-id"`, "</dd>")

	// The whole span, opening tag included: regionBetween cuts at its start
	// marker, and the marker here sits inside the tag.
	status := regexp.MustCompile(`<span[^>]*data-krill="copy-task-id-status"[^>]*>(?s).*?</span>`)
	span := status.FindString(row)
	require.NotEmpty(t, span,
		"the copy chip has no live region to confirm into, so a click would copy silently:\n%s", row)

	assert.Contains(t, span, `role="status"`,
		"the confirmation needs a live region role or it is announced to nobody")
	assert.Contains(t, span, `aria-live="polite"`,
		"polite, not assertive: confirming a copy must not interrupt the operator mid-task")
	assert.NotContains(t, span, `aria-live="assertive"`,
		"assertive interrupts whatever the operator was doing")
	assert.Equal(t, "></span>", span[len(span)-len("></span>"):],
		"the confirmation is blank on arrival -- a live region has to exist before its content "+
			"changes, so this one span starts empty by design. span: %s", span)

	// Not focusable, so no reader lands on the confirmation instead of where
	// they were: a span cannot take focus, and it carries nothing that can.
	assert.NotContains(t, span, "tabindex")
	for _, focusable := range []string{"<button", "<a ", "<input"} {
		assert.NotContains(t, span, focusable,
			"the confirmation must not be focusable, or the copy steals the operator's place")
	}

	// Beside the chip, not over it: the id stays on screen while the
	// confirmation is read, and the confirmation sits in the same row.
	assert.Contains(t, row, ">"+task.ID.String()+"</button>",
		"the chip's id must still be there while the confirmation reads")
	// Only the id row carries the status hook: a confirmation region that
	// wandered onto another row would announce beside the wrong value.
	assert.Equal(t, 1, strings.Count(html, `data-krill="copy-task-id-status"`),
		"the copy confirmation region belongs to the Task id row alone")
}

// TestTaskDetailRailDependsOnCardRendersOnADepsReadFailure is the degraded
// half of the Depends-on card, and the reason the card's absence is
// conditional on the READ rather than on the RESULT.
//
// "No dependencies" and "could not read the dependencies" are different
// answers to the same question, and the difference is the operator's whole
// reason for looking: a task with no dependencies is claimable now, and a
// task whose dependencies could not be read may be blocked by something this
// page is not showing. So the failure renders the card, with the alert
// inside it, in place of the list -- never an empty card, which would read
// as the first answer.
func TestTaskDetailRailDependsOnCardRendersOnADepsReadFailure(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "unreadable-deps", CurrentLane: store.LaneTesting})
	f.store.depsErr = errDepsRead

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := regionBetween(t, html, `data-krill="task-depends-on"`, "</aside>")
	assert.Contains(t, card, "The dependencies could not be read")
	assert.NotContains(t, card, `data-krill="task-dep"`,
		"a failed read lists nothing; an empty list would read as 'no dependencies'")
	assert.NotContains(t, html, "deps-read-boom", "the store's error is not the operator's to read")
	// The rest of the rail is unaffected: one failed read is one clause of
	// one card.
	props := propertiesOf(t, html)
	assert.Contains(t, props, `data-krill="task-properties-lane"`)
	assert.Contains(t, props, `data-krill="task-properties-attempts"`)
}

// TestTaskDetailRailDependsOnCardIsAbsentWhenThereAreNone is the other side
// of the same condition, stated as a test of its own because it is the case
// the operator sees most: a task nobody declared a dependency on.
//
// The card is omitted entirely. An empty box beside the content says
// something is missing, where the answer is "nothing" -- and the region the
// FR names the card by is simply not on the page.
func TestTaskDetailRailDependsOnCardIsAbsentWhenThereAreNone(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "unblocked-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	assert.NotContains(t, html, `data-krill="task-depends-on"`,
		"no dependencies renders no card, not an empty one")
	assert.NotContains(t, html, ">Depends on</h2>")
	// The properties card beside where it would have been still renders, so
	// this is the card's absence and not the rail's.
	assert.Contains(t, html, `data-krill="task-properties"`)
}

// TestTaskDetailRailAttemptsLabelCountsLapsedAndAbandonedAttempts is FR
// 82add903's "n of cap, noting lapsed leases count".
//
// The number itself is the store's -- attempt_count, which ReclaimExpired,
// AbandonClaim and ReleaseLease each increment, so a task that claimed and
// abandoned comes back with a higher count and not a lower one. The rail's
// half is to render that count against the cap and to SAY that a lapse or an
// abandon counts, because an operator watching a task's count climb with no
// worker on it would otherwise read the climb as a bug.
//
// Both halves are pinned: the label is the store's own count against the
// store's own cap, and the note is on the page.
func TestTaskDetailRailAttemptsLabelCountsLapsedAndAbandonedAttempts(t *testing.T) {
	f := newDetailFixture(t)
	// A task that was claimed, abandoned, and claimed again: three lapsed
	// or abandoned attempts, none of which left a live claim behind.
	task := f.add(store.Task{
		Title: "thrashy-task", CurrentLane: store.LaneTesting, AttemptCount: 3,
	})
	abandoned := time.Now().Add(-2 * time.Hour).UTC()
	abandoner := store.SessionID(uuid.New())
	f.store.lastClaim = store.Claim{
		ID: uuid.New(), TaskID: task.ID, SessionID: abandoner,
		ClaimedAt: abandoned, ReleasedAt: &abandoned, ReleaseReason: strPtr("abandon"),
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-attempts"`, "</dd>")
	assert.Contains(t, row, taskAttemptsLabel(task.AttemptCount),
		"the label is the store's count against the store's cap")
	assert.Contains(t, row, fmt.Sprintf("%d of %d", task.AttemptCount, store.DefaultAttemptCap),
		"and it names the cap explicitly, so '3 of 3' reads as capped rather than as a bare 3")
	assert.Contains(t, row, "lease-lapse and abandon attempts count toward the cap",
		"without this note, a count climbing while no worker holds the task reads as a bug")

	// And the same page names who last held it, so "3 of 3" and "None. Last
	// held by X" are visible together -- the two facts an operator is
	// reconciling when they ask why a task is capped and idle.
	assert.Contains(t, html, "None. Last held by")
	assert.Contains(t, html, abandoner.String())
}

// TestTaskDetailRailAttemptsCountSurvivesTheLapsedLeaseIsCloser is the
// FR's "n of cap, NOTING lapsed leases count" as the case the counting
// note is actually about.
//
// The case above pins the label and the note together, but its fixture
// still carries a last claim, so it cannot tell a count that came from
// task.attempt_count off one the page derived from the claim rows it can
// see. This one cannot: after a lease lapses and ReclaimExpired sweeps
// it, the task holds no claim and no lease, and the only thing on the
// page that remembers the burn is the task row's own counter.
//
// A rail that recomputed attempts from visible claims -- the shape a
// "count the claims for this task" implementation would take -- would
// render "0 of 3" here and read as a task nobody has ever touched.
func TestTaskDetailRailAttemptsCountSurvivesTheLapsedLeaseIsCloser(t *testing.T) {
	f := newDetailFixture(t)
	// Swept: no current_claim_id, no lease_expires_at, and one attempt
	// burned by the lapse. The store's ReclaimExpired clears both claim
	// fields and increments attempt_count in the same statement.
	task := f.add(store.Task{
		Title: "swept-task", CurrentLane: store.LaneTesting, AttemptCount: 1,
	})
	sweeper := store.SessionID(uuid.New())
	f.store.lastClaim = store.Claim{
		ID: uuid.New(), TaskID: task.ID, SessionID: sweeper,
		ClaimedAt:     time.Now().Add(-3 * time.Hour).UTC(),
		ReleasedAt:    func() *time.Time { t := time.Now().Add(-2 * time.Hour).UTC(); return &t }(),
		ReleaseReason: strPtr("reclaim"),
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-attempts"`, "</dd>")
	assert.Contains(t, row, taskAttemptsLabel(1),
		"a lapsed lease burned an attempt, and the sweep cleared every claim field the page can see")
	assert.NotContains(t, row, taskAttemptsLabel(0),
		"an attempts count derived from the visible claims would render 0 of 3 for a task that has been worked on")

	// The lease itself is genuinely gone, so the row must not render one --
	// the count surviving the sweep must not carry a lease back with it.
	assert.NotContains(t, html, `data-krill="task-lease"`,
		"the sweep cleared lease_expires_at; rendering a lease here would be inventing one")
	assert.Contains(t, html, "None. Last held by",
		"and the sweep still leaves the session that held it last nameable")
}

// TestTaskDetailRailMilepebbleRowLinksToThatMilepebblesTasks is FR
// 82add903's "Milepebble (link to its tasks)" as its own case, because the
// href is the clause: a row naming the milepebble without linking it, or
// linking somewhere other than its task list, is a dead end in the one place
// on the page that offers a way back to the queue.
//
// It also pins that the link is the milepebble's OWN list and not the
// milestone's, which a href built from the parent would get subtly wrong --
// the operator lands on a list that does not contain their task.
func TestTaskDetailRailMilepebbleRowLinksToThatMilepebblesTasks(t *testing.T) {
	f := newDetailFixture(t)
	task := f.addOnMilepebble(store.Task{Title: "pebbled-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-milepebble"`, "</dd>")
	want := htmlEscapedURL(productTaskContainerHref(f.pid, tasksSuffix,
		taskContainer{ID: f.mp, Kind: string(store.MilestoneKindMilepebble)}))
	assert.Contains(t, row, `href="`+want+`"`, "the row links at its own milepebble's task list")
	assert.Contains(t, row, ">Cut milepebble</a>")
	// Scoped to the milepebble, not the milestone it was cut from: the
	// list the operator lands on must be the one that holds this task.
	assert.Contains(t, row, "scope=milepebble")
	assert.NotContains(t, row, f.mid.String(),
		"the parent milestone's id has no business in a link to the milepebble's tasks")
}

// TestTaskDetailRailRendersOnBothDetailRoutes pins the rail to the route the
// FR names. The rail is composed in the one serveTaskDetail both routes
// reach, so a rail wired from the request context alone -- from the
// pre-redesign route's path, say -- would render on one URL and not the
// other, and only the URL the FR specifies would be covered by any other
// test here.
func TestTaskDetailRailRendersOnBothDetailRoutes(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	escalatedAt := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: escalatedAt}
	task := f.addOnMilepebble(store.Task{
		Title: "both-ways", CurrentLane: store.LaneTesting,
		AttemptCount: 1, CurrentEscalationID: &esc,
	})

	for name, get := range map[string]func(string, bool) (int, string){
		"product-scoped": func(tid string, hx bool) (int, string) { return f.getProductScoped(tid, hx) },
		// The pre-redesign URL names the container, so it has to name the
		// milepebble this task actually sits on -- the fixture's other
		// container would refuse it as a task belonging to another one.
		"pre-redesign": func(tid string, hx bool) (int, string) {
			return f.getAt(taskDetailPath(f.pid, f.mp, uuid.MustParse(tid)), hx)
		},
	} {
		t.Run(name, func(t *testing.T) {
			code, html := get(task.ID.String(), true)
			require.Equal(t, 200, code, "body: %s", html)

			props := propertiesOf(t, html)
			for _, want := range []string{
				`data-krill="task-properties"`,
				`data-krill="task-properties-lane"`,
				`data-krill="task-properties-attempts"`,
				`data-krill="task-properties-claim"`,
				`data-krill="task-properties-escalated"`,
				`data-krill="task-properties-milepebble"`,
				`data-krill="task-properties-id"`,
				"1 of 3",
			} {
				assert.Contains(t, props, want)
			}
		})
	}
}

func strPtr(s string) *string { return &s }

// TestTaskDetailRailDoesNotNameTheEscalationId is a judgement the rail made
// and a reviewer will pull on, stated as a test so it cannot drift back.
//
// The task row's escalation is a UUID. A rail row reading "Escalation:
// escalation 3f9c-..." states an identifier the operator cannot act on and
// does not need -- what they need is WHEN, and the row already says that,
// exactly, on hover. The id stays on the view model as page.Escalation for
// the Overview callout to present; it is simply not printed here.
func TestTaskDetailRailDoesNotNameTheEscalationId(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	escalatedAt := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	f.store.escalation = store.EscalationEvent{ID: esc, Reason: store.EscalationReasonThrashCap, CreatedAt: escalatedAt}
	task := f.add(store.Task{
		Title: "escalated-task", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc,
	})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-escalated"`, "</dd>")
	// The FR's clause -- relative time, exact on hover, only when escalated
	// -- is satisfied by the element alone.
	assert.Contains(t, row, `data-krill-updated-at="`+escalatedAt.Format(time.RFC3339)+`"`,
		"the row's instant must be one relativeAgeScript can upgrade")
	assert.Contains(t, row, `title="`+escalatedAt.Format(time.RFC3339)+`"`,
		"and the exact instant must be on hover")
	// And the id is not in it.
	assert.NotContains(t, row, esc.String(),
		"a bare UUID in a rail row is an identifier, not an answer; the row states WHEN")
	assert.NotContains(t, html, "escalation "+esc.String(),
		"the raw 'escalation <uuid>' string is not the operator's to read")

	// The id is still on the view model for the Overview callout, which
	// explains the escalation. Dropping it from the markup is not dropping it
	// from the page's data.
	page := detailPageOfFixture(t, taskDetailInputs{
		Task:       task,
		Escalation: &store.EscalationEvent{Reason: store.EscalationReasonThrashCap, CreatedAt: escalatedAt},
	})
	assert.Equal(t, "escalation "+esc.String(), page.Escalation)
	assert.Equal(t, "thrash-cap", page.EscalationReason)
	assert.Equal(t, escalatedAt.Format(time.RFC3339), page.EscalatedAt)
}

// TestTaskDetailPageStillCarriesWhatTheOverviewPanelReads is the safety
// check on moving Lane/Attempts/Claim/Escalated out of the content column:
// the Overview tab panel (a sibling task) is specified to show the
// explanation callout, the description and the latest notes, and to explain
// an escalation from the event's reason and counter.
//
// Every one of those inputs is a view-model field or a content-column
// region, so the move is safe only while they survive it. This asserts they
// do, at the level the Overview panel will read them -- which is the only
// place a dedup can quietly cost a sibling task its input.
func TestTaskDetailPageStillCarriesWhatTheOverviewPanelReads(t *testing.T) {
	f := newDetailFixture(t)
	esc, claim := uuid.New(), uuid.New()
	escalatedAt := time.Date(2026, 9, 30, 13, 50, 0, 0, time.UTC)
	lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	body := "the description"
	task := f.addOnMilepebble(store.Task{
		Title: "overview-inputs", CurrentLane: store.LaneTesting, Body: &body,
		AttemptCount:   store.DefaultAttemptCap,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})
	cap := store.DefaultAttemptCap
	f.store.escalation = store.EscalationEvent{
		ID: esc, Reason: store.EscalationReasonThrashCap, CreatedAt: escalatedAt,
		CounterValue: &cap, CapValue: &cap,
	}
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
	f.store.notes = []store.Note{
		{ID: uuid.New(), Kind: store.NoteKindScopeNote, Body: "newest", CurrentStatus: "noted"},
		{ID: uuid.New(), Kind: store.NoteKindComment, Body: "older", CurrentStatus: "carried-over"},
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	// The Overview panel's three subjects are still rendered in the content
	// column it will slot into. The description is on the Overview tab
	// itself; the latest notes are on the Notes tab, which is where the
	// facet strip (FR 7e463e31) put them -- each still in the content
	// column, at the tab that owns it.
	main := regionBetween(t, html, `data-krill="task-detail-main"`, "</section>")
	assert.Contains(t, main, `data-krill="task-body"`, "the description card's input")
	// The notes hook is the panel's own now -- the facet strip gave the
	// notes list a panel host rather than a bare content-column region.
	_, notes := f.getTabAt(task.ID.String(), "?tab=notes")
	assert.Contains(t, notes, `data-krill="task-panel-notes"`, "the latest-notes card's input")
	for _, want := range []string{"newest", "older"} {
		assert.Contains(t, notes, want, "the notes card's input, in the store's order")
	}

	// And the callout's facts are on the view model, which is what the
	// callout will be built from -- reason, counter and cap, all of which
	// the rail's Escalated row does not print.
	page := detailPageOfFixture(t, taskDetailInputs{
		Task:       task,
		Claim:      &store.Claim{SessionID: store.SessionID(uuid.New())},
		Escalation: &f.store.escalation,
		Notes:      f.store.notes,
	})
	assert.Equal(t, "thrash-cap", page.EscalationReason)
	assert.Equal(t, escalatedAt.Format(time.RFC3339), page.EscalatedAt)
	assert.Equal(t, body, page.Body)
	assert.Len(t, page.Notes, 2, "the notes are still on the view model in the store's order")

	// The three state badges the callout keys off come off the task row and
	// are untouched by the move.
	header := regionBetween(t, html, `data-krill="task-detail-header"`, `data-krill="loaded-at"`)
	for _, key := range []string{"claimed", "capped", "escalated"} {
		assert.Contains(t, header, `data-krill="task-badge-`+key+`"`,
			"the Overview callout keys off this badge, which the dedup must not have moved")
	}
}

// railValueRE captures each properties-card row's hook and its value markup.
// The label is a <dt> beside the <dd>, so what lands in the second group is
// the value the operator reads and nothing else.
var railValueRE = regexp.MustCompile(`(?s)<dd data-krill="([a-z-]+)">(.*?)</dd>`)

// railMarkupRE strips tags, so a value that is present in the markup but
// empty once rendered is visible as the empty string rather than as markup
// that merely looks full.
var railMarkupRE = regexp.MustCompile(`<[^>]*>`)

// railEmptySpanRE is the shape the read-failure defect actually took: an
// element that renders, is styled as the row's value, and has nothing in it.
// "Claimed by <span class="font-mono text-xs"></span>" is this shape.
var railEmptySpanRE = regexp.MustCompile(`<span[^>]*>\s*</span>`)

// railCopyStatusSpanRE is the one element on the card that is empty on
// purpose: the copy chip's live region. A live region has to be in the
// document before its content changes or the change is never announced, so
// it necessarily renders empty and is only filled by the click that copies.
//
// It is the exemption, not a weakening: this span carries no value and makes
// no claim about the task, so railEmptySpanRE's rule -- an empty element
// where a value belongs reads as a value the page could not read -- has
// nothing to say about it. The carve-out is anchored on the exact hook
// rather than on "any empty span with a role", so the chip's own span, or an
// empty span in any other row, still trips the sweep.
var railCopyStatusSpanRE = regexp.MustCompile(`<span[^>]*data-krill="copy-task-id-status"[^>]*>\s*</span>`)

// withoutCopyStatusLiveRegion strips that one span so the sweep can run
// over the card with its teeth intact.
func withoutCopyStatusLiveRegion(card string) string {
	return railCopyStatusSpanRE.ReplaceAllString(card, "")
}

// TestWithoutCopyStatusLiveRegion_StripsOnlyThatOneSpan keeps the exemption
// narrow. A carve-out phrased as "any empty span" or "any empty element with
// a role" would quietly disarm the sweep it was added to exempt, and the
// defect the sweep exists for -- a value-bearing span with nothing in it --
// would walk straight back in through the new exemption.
func TestWithoutCopyStatusLiveRegion_StripsOnlyThatOneSpan(t *testing.T) {
	const liveRegion = `<span class="ml-2 text-xs" data-krill="copy-task-id-status" role="status" aria-live="polite"></span>`

	t.Run("strips the empty live region", func(t *testing.T) {
		assert.Empty(t, withoutCopyStatusLiveRegion(liveRegion),
			"the one legitimately empty element on the card should be the only thing removed")
	})

	t.Run("leaves a populated live region alone", func(t *testing.T) {
		// The carve-out is for the empty state only. A span that has
		// something to say is content, and the sweep's question does not
		// apply to it.
		populated := `<span data-krill="copy-task-id-status" role="status">Copied</span>`
		assert.Equal(t, populated, withoutCopyStatusLiveRegion(populated))
	})

	// The defect the sweep exists for, verbatim: an empty span carrying a
	// row's value. If this survives the carve-out, the exemption has grown
	// beyond its one live region and the sweep is no longer sweeping.
	for _, defect := range []string{
		`Claimed by <span class="font-mono text-xs"></span>`,
		`<span class="text-base-content/70" role="status"></span>`,
		`<span data-krill="copy-task-id-status-other"></span>`,
		`<span><span class="font-mono"></span></span>`,
	} {
		t.Run("still catches "+defect, func(t *testing.T) {
			assert.Regexp(t, railEmptySpanRE, withoutCopyStatusLiveRegion(defect),
				"the carve-out must not exempt an empty span that carries a value")
		})
	}
}

// railValueTexts is every properties-card row's visible text, keyed by hook.
//
// Stripping the markup is what makes this useful: a row whose value element
// rendered empty is indistinguishable from a row with a value once you are
// only looking at the hook, and the whole failure the Testing lane exists to
// catch is a value-bearing element whose content is empty because a read
// failed.
func railValueTexts(t *testing.T, html string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range railValueRE.FindAllStringSubmatch(propertiesOf(t, html), -1) {
		out[m[1]] = strings.TrimSpace(railMarkupRE.ReplaceAllString(m[2], ""))
	}
	return out
}

// TestTaskDetailRailNoRowRendersAnEmptyValue is the sweep.
//
// The Claim row shipped the defect once already: GetClaimByID failed, the row
// still rendered, and the value-bearing element inside it was empty -- so the
// operator read "Claimed by " with nothing after it, which is a value the
// page never learned rather than a value it learned. Fixing that one branch
// only rules out that one branch; it does not rule out the same shape
// appearing somewhere else on the rail.
//
// So this states the rail's invariant once, over every shape every rail read
// can be in: a row that renders always renders text, and the properties card
// never contains an empty span. Each case below is a different combination
// of reads succeeding and failing -- the four claim shapes, an escalation
// whose event could not be read, a dependency read that failed -- because the
// defect only appears in the degraded ones and a suite that only ever
// exercises the happy path would never see it.
func TestTaskDetailRailNoRowRendersAnEmptyValue(t *testing.T) {
	esc := uuid.New()
	escalatedAt := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second)

	// Each case gets its own fixture so the knobs under test are the only
	// ones that differ.
	cases := map[string]func(*detailFixture) store.Task{
		// A live, readable claim: the happy path, where every row has a value.
		"live claim": func(f *detailFixture) store.Task {
			claim := uuid.New()
			lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			task := f.addOnMilepebble(store.Task{
				Title: "live-claim", CurrentLane: store.LaneTesting, AttemptCount: 1,
				CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
			})
			f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: escalatedAt}
			f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
			return task
		},
		// The defect's own case: the claim is real, the holder is unreadable.
		"claim holder read failed": func(f *detailFixture) store.Task {
			claim := uuid.New()
			lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
			task := f.addOnMilepebble(store.Task{
				Title: "unreadable-holder", CurrentLane: store.LaneTesting,
				CurrentClaimID: &claim, LeaseExpiresAt: &lease,
			})
			f.store.claimErr = errClaimRead
			return task
		},
		// A claim whose lease lapsed: the holder is readable, the lease is gone.
		"expired lease": func(f *detailFixture) store.Task {
			claim := uuid.New()
			past := time.Now().Add(-time.Hour).UTC()
			task := f.add(store.Task{
				Title: "lapsed", CurrentLane: store.LaneTesting,
				CurrentClaimID: &claim, LeaseExpiresAt: &past,
			})
			f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
			return task
		},
		// Unclaimed but previously held: the last-holder read succeeded.
		"released claim": func(f *detailFixture) store.Task {
			task := f.addOnMilepebble(store.Task{Title: "released", CurrentLane: store.LaneTesting, AttemptCount: 2})
			held := time.Now().Add(-time.Hour).UTC()
			reason := "complete"
			f.store.lastClaim = store.Claim{
				ID: uuid.New(), TaskID: task.ID, SessionID: store.SessionID(uuid.New()),
				ClaimedAt: held, ReleasedAt: &held, ReleaseReason: &reason,
			}
			return task
		},
		// Unclaimed and the last-holder read FAILED: the row must not fall
		// through to a shape that reads as "nobody ever touched this".
		"last claim read failed": func(f *detailFixture) store.Task {
			task := f.add(store.Task{Title: "unreadable-last", CurrentLane: store.LaneTesting})
			f.store.lastClaimErr = errClaimRead
			return task
		},
		// Unclaimed and never claimed: the plainest empty case.
		"never claimed": func(f *detailFixture) store.Task {
			return f.add(store.Task{Title: "untouched", CurrentLane: store.LaneTesting})
		},
		// Escalated, but the event behind the escalation id could not be read.
		// This is the branch that renders a row with no <time> in it.
		"escalation read failed": func(f *detailFixture) store.Task {
			f.store.escalationErr = errEscalationRead
			return f.add(store.Task{Title: "unreadable-escalation", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc})
		},
		// Escalated, but the event resolved between the two reads: no instant,
		// and the store's ErrNotFound is not an operator-facing failure.
		"escalation resolved underneath": func(f *detailFixture) store.Task {
			f.store.escalationErr = store.ErrNotFound
			return f.add(store.Task{Title: "resolved-escalation", CurrentLane: store.LaneTesting, CurrentEscalationID: &esc})
		},
		// The dependency read failed: the card renders, with the gap named.
		"deps read failed": func(f *detailFixture) store.Task {
			task := f.addOnMilepebble(store.Task{Title: "unreadable-deps", CurrentLane: store.LaneTesting})
			f.store.depsErr = errDepsRead
			return task
		},
	}

	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := setup(f)

			code, html := f.getProductScoped(task.ID.String(), true)
			require.Equal(t, 200, code, "body: %s", html)

			values := railValueTexts(t, html)
			require.NotEmpty(t, values, "the properties card rendered no rows at all:\n%s", html)
			for hook, text := range values {
				assert.NotEmpty(t, text,
					"the %s row rendered with nothing in it. An empty value element is not a blank -- it reads as a value the page knows and does not:\n%s",
					hook, regionBetween(t, html, `data-krill="`+hook+`"`, "</dd>"))
			}

			// And no empty span anywhere in the card, whatever row it sits in.
			// The regex is anchored on the card rather than the page because
			// the page's chrome legitimately renders empty spacer elements.
			// The copy chip's live region is the single carve-out, stripped by
			// its exact hook: it is empty by design, carries no value, and says
			// nothing about the task. The carve-out is that one span, not empty
			// spans generally, so every other row still runs under the sweep.
			card := withoutCopyStatusLiveRegion(propertiesOf(t, html))
			assert.NotRegexp(t, railEmptySpanRE, card,
				"the properties card rendered an element with no content in it:\n%s", card)
		})
	}
}

// TestTaskDetailRailClaimRowRendersFourDistinctShapes states the property
// the Claim row has to have as one test, because it is the property that
// failed: the row has four shapes and no two of them may render alike.
//
// Confusing them is not a cosmetic problem. "None. Last held by X" and "None.
// This task has never been claimed." are different operational facts --
// someone touched this task and let go, or nobody ever has -- and an operator
// deciding whether to pick the task up is deciding on exactly that
// difference. So does the read-failed shape against either of them: "could
// not read" is not "nobody", and a page that cannot read the holder must not
// say it is empty.
//
// Each case below asserts its own shape is present AND that the other three
// are absent. The negative half is the load-bearing half: asserting only that
// the expected phrase appears would pass just as happily against a row that
// printed all four phrases at once, which is the confusion this guards.
func TestTaskDetailRailClaimRowRendersFourDistinctShapes(t *testing.T) {
	// The four phrases as the rail renders them, kept in one place so the
	// positive and negative halves below cannot drift apart.
	const (
		live      = "Claimed by"
		released  = "None. Last held by"
		neverHeld = "None. This task has never been claimed."
		unread    = "could not be read"
	)

	cases := map[string]struct {
		setup   func(*detailFixture) store.Task
		want    string
		notWant []string
	}{
		// A live claim: the holder is named, so none of the three "nobody is
		// on this" shapes may appear.
		"live claim": {
			setup: func(f *detailFixture) store.Task {
				claim := uuid.New()
				lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
				task := f.add(store.Task{
					Title: "held", CurrentLane: store.LaneTesting,
					CurrentClaimID: &claim, LeaseExpiresAt: &lease,
				})
				f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}
				return task
			},
			want: live,
			// "could not be read" must not fire here even though the lease
			// row also renders: the holder DID read, and the phrase appearing
			// anyway would be the operator being told a working read failed.
			notWant: []string{released, neverHeld, unread},
		},
		// Released: the claim is over, but somebody held it, so the row names
		// them and refuses to say the task was never touched.
		"released claim": {
			setup: func(f *detailFixture) store.Task {
				task := f.add(store.Task{Title: "let-go", CurrentLane: store.LaneTesting})
				held := time.Now().Add(-time.Hour).UTC()
				reason := "complete"
				f.store.lastClaim = store.Claim{
					ID: uuid.New(), TaskID: task.ID, SessionID: store.SessionID(uuid.New()),
					ClaimedAt: held, ReleasedAt: &held, ReleaseReason: &reason,
				}
				return task
			},
			want:    released,
			notWant: []string{live, neverHeld, unread},
		},
		// Never claimed: nobody has ever held it, so there is no last holder
		// to name and the row says exactly that.
		"never claimed": {
			setup: func(f *detailFixture) store.Task {
				return f.add(store.Task{Title: "untouched", CurrentLane: store.LaneTesting})
			},
			want:    neverHeld,
			notWant: []string{live, released, unread},
		},
		// The claim read FAILED on a task that genuinely holds one. This is
		// the shape the defect collapsed into "claimed by nobody": it must
		// name the gap, and it must NOT claim the task is unheld -- which is
		// what it would say if it fell through to either "None." branch.
		"claim read failed": {
			setup: func(f *detailFixture) store.Task {
				claim := uuid.New()
				lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
				task := f.add(store.Task{
					Title: "unreadable", CurrentLane: store.LaneTesting,
					CurrentClaimID: &claim, LeaseExpiresAt: &lease,
				})
				f.store.claimErr = errClaimRead
				return task
			},
			want: unread,
			// The important negative: an unreadable holder is not an absent
			// one. Falling through to either "None." branch is precisely the
			// confusion this test exists to catch.
			notWant: []string{released, neverHeld},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDetailFixture(t)
			task := tc.setup(f)

			code, html := f.getProductScoped(task.ID.String(), true)
			require.Equal(t, 200, code, "body: %s", html)

			row := regionBetween(t, html, `data-krill="task-properties-claim"`, "</dd>")
			assert.Contains(t, row, tc.want,
				"the claim row did not render its own shape:\n%s", row)
			for _, absent := range tc.notWant {
				assert.NotContains(t, row, absent,
					"the claim row rendered two shapes at once, which is the confusion this guards against:\n%s", row)
			}
			// And it is text, not an element with nothing in it.
			assert.NotEmpty(t, strings.TrimSpace(railMarkupRE.ReplaceAllString(row, "")))
			assert.NotRegexp(t, railEmptySpanRE, row,
				"the claim row rendered an element with no content in it:\n%s", row)
		})
	}
}

// sweepTrips runs the sweep's OWN predicate -- the exact expression
// TestTaskDetailRailNoRowRendersAnEmptyValue asserts with -- over a card, so
// this file's carve-out cases are testing the shipped sweep rather than a
// restatement of it.
//
// That distinction is the point. The carve-out is a change to the sweep's
// INPUT, so testing it against a hand-written copy of the rule proves only
// that the copy agrees with itself. Driving it through the real card, with
// the real regex, is what makes "the carve-out does not disarm the sweep" a
// statement about the page rather than about this file.
func sweepTrips(card string) bool {
	return railEmptySpanRE.MatchString(withoutCopyStatusLiveRegion(card))
}

// TestTheCopyChipCarveOutStillCatchesTheEmptyValueDefect is the audit of the
// empty-element sweep's carve-out, run against the real card.
//
// The rail failed validation once for exactly this class of bug: a row
// rendering an empty value that read as a real one. The sweep was written to
// make that impossible, and this task then carved one element out of it --
// the copy chip's live region, which is empty on arrival by design, because
// a live region must exist before its content changes to be announced.
//
// A carve-out is only safe if it is narrow enough that the class of bug it
// was protecting against still cannot walk in through it. Each case below is
// that class, injected into the REAL rendered card: the sweep must still
// trip on it. The live region itself is in that card throughout, so every
// case also proves the carve-out is not simply eating the whole card.
//
// This is deliberately not "the regex still matches a string I typed". It is
// "the shipped page, plus this defect, still fails the shipped sweep".
func TestTheCopyChipCarveOutStillCatchesTheEmptyValueDefect(t *testing.T) {
	// The claim row's holder is the value the original defect emptied, so
	// the carrier here is the real markup rather than a hand-written span.
	f := newDetailFixture(t)
	held := time.Now().Add(-time.Hour).UTC()
	lastSession := store.SessionID(uuid.New())
	task := f.add(store.Task{Title: "carve-out-task", CurrentLane: store.LaneTesting})
	f.store.lastClaim = store.Claim{
		ID: uuid.New(), TaskID: task.ID, SessionID: lastSession,
		ClaimedAt: held, ReleasedAt: &held, ReleaseReason: strPtr("complete"),
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := propertiesOf(t, html)
	// The live region really is on the card -- otherwise every case below
	// passes for the wrong reason, with no carve-out in play at all.
	require.NotEmpty(t, railCopyStatusSpanRE.FindString(card),
		"the card under audit has no live region to carve out:\n%s", card)
	// And the carve-out is what clears it, so the baseline is clean.
	require.False(t, sweepTrips(card),
		"the real card trips the sweep even after the carve-out, so this audit has no clean "+
			"baseline to inject into:\n%s", card)

	// Each case replaces the claim row's value with a shape the sweep must
	// reject. The claim row's real content is what gets replaced, so nothing
	// here is a shape the template could not produce.
	replacements := map[string]struct{ from, to string }{
		// The original defect, verbatim: an empty span styled as the row's
		// value. "Claimed by <span class="font-mono text-xs"></span>" reads
		// as claimed-by-nobody.
		"the original defect: an empty holder span": {
			from: `<span class="font-mono text-xs">` + lastSession.String() + `</span>`,
			to:   `<span class="font-mono text-xs"></span>`,
		},
		// The same defect wearing the live region's role: an exemption keyed
		// on "empty span with a role" instead of on the exact hook would let
		// this through.
		"an empty value span that also has role=status": {
			from: `<span class="font-mono text-xs">` + lastSession.String() + `</span>`,
			to:   `<span class="font-mono text-xs" role="status" aria-live="polite"></span>`,
		},
		// The near-miss hook: one character away from the exempt one. An
		// exemption keyed on a prefix rather than on the exact attribute
		// value would swallow it.
		"a near-miss hook": {
			from: `<span class="font-mono text-xs">` + lastSession.String() + `</span>`,
			to:   `<span class="font-mono text-xs" data-krill="copy-task-id-status-2"></span>`,
		},
		// Nested: an empty span inside another span. The outer has content
		// (an element), so a sweep that only checked the outermost tag's text
		// would read it as populated.
		"a nested empty span": {
			from: `<span class="font-mono text-xs">` + lastSession.String() + `</span>`,
			to:   `<span class="font-mono text-xs"><span class="font-mono"></span></span>`,
		},
		// Whitespace-only: an element whose content is a newline and a tab,
		// which is what a template's own indentation produces.
		"a whitespace-only span": {
			from: `<span class="font-mono text-xs">` + lastSession.String() + `</span>`,
			to:   "<span class=\"font-mono text-xs\">\n\t\t\t</span>",
		},
	}

	for name, r := range replacements {
		t.Run(name, func(t *testing.T) {
			require.Contains(t, card, r.from,
				"the card no longer has the carrier this defect replaces:\n%s", card)
			defective := strings.Replace(card, r.from, r.to, 1)

			// The defect is really in, and the live region is still there --
			// so the sweep is failing because of the defect, not because the
			// carve-out ate the card.
			require.NotEqual(t, card, defective, "the injection did not change the card")
			require.NotEmpty(t, railCopyStatusSpanRE.FindString(defective),
				"the live region vanished from the card under test, so this case is not testing "+
					"the carve-out:\n%s", defective)

			assert.True(t, sweepTrips(defective),
				"the carve-out swallowed a value-bearing empty span; this is the defect class the "+
					"sweep exists for and it must still fail:\n%s", defective)
		})
	}
}

// TestTheCopyChipCarveOutIsOneElementNotAPattern is the carve-out's other
// half: it must remove exactly the one span, and leave the rest of the card
// byte-for-byte alone.
//
// The cases above ask whether the sweep still BITES. This asks whether the
// carve-out still removes only what it is supposed to -- because an
// over-broad replacement that quietly deleted real content would make the
// sweep pass by hiding markup rather than by the markup being correct, and
// that failure is invisible from the sweep's side.
func TestTheCopyChipCarveOutIsOneElementNotAPattern(t *testing.T) {
	f := newDetailFixture(t)
	esc := uuid.New()
	escalatedAt := time.Now().Add(-90 * time.Minute).UTC().Truncate(time.Second)
	claim := uuid.New()
	lease := time.Now().Add(time.Hour).UTC().Truncate(time.Second)
	task := f.add(store.Task{
		Title: "carve-out-scope", CurrentLane: store.LaneTesting, AttemptCount: 1,
		CurrentClaimID: &claim, LeaseExpiresAt: &lease, CurrentEscalationID: &esc,
	})
	f.store.escalation = store.EscalationEvent{ID: esc, CreatedAt: escalatedAt}
	f.store.claim = store.Claim{ID: claim, TaskID: task.ID, SessionID: store.SessionID(uuid.New())}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := propertiesOf(t, html)
	carved := withoutCopyStatusLiveRegion(card)

	// Exactly one element went. The card is six rows; a carve-out that ate
	// more than one would show up as a row that has vanished.
	assert.Equal(t, 1, strings.Count(card, railCopyStatusSpanRE.FindString(card)),
		"the carve-out matched %d spans, but the chip's live region is one",
		strings.Count(card, railCopyStatusSpanRE.FindString(card)))

	// Every row is still there, with its value.
	for hook, want := range map[string]string{
		"task-properties-lane":      "Testing",
		"task-properties-attempts":  "1 of 3",
		"task-properties-claim":     "Claimed by",
		"task-properties-escalated": escalatedAt.Format(time.RFC3339),
		"task-properties-id":        task.ID.String(),
	} {
		row := regionBetween(t, carved, `data-krill="`+hook+`"`, "</dd>")
		assert.Contains(t, row, want,
			"the carve-out removed content from the %s row; it must strip one span, not a pattern", hook)
	}

	// And the whole removal is accounted for by that one span's own text --
	// nothing else changed.
	span := railCopyStatusSpanRE.FindString(card)
	require.NotEmpty(t, span)
	assert.Equal(t, strings.Replace(card, span, "", 1), carved,
		"the carve-out changed more than the live region's own markup")
}

// TestTheCopyChipCarveOutWouldBeUnnecessaryIfTheSweepRanOnTheRow is the
// structural reason the carve-out has to exist at all, and the reason it can
// stay this narrow.
//
// The sweep's regex matches an empty span ANYWHERE in the card. The live
// region is an empty span in the card. So the carve-out is not optional
// politeness -- without it the sweep would fail on every correct page, and
// the tempting fix for that failure is to weaken the regex, which would
// weaken the sweep for every row. This pins that the exemption is a
// pre-filter on one element rather than a loosening of the rule.
func TestTheCopyChipCarveOutWouldBeUnnecessaryIfTheSweepRanOnTheRow(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "baseline-task", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := propertiesOf(t, html)

	// The live region IS an empty span, matched by the sweep's own regex --
	// so the carve-out is load-bearing, not decorative.
	assert.Regexp(t, railEmptySpanRE, card,
		"the sweep's regex does not match the live region, so the carve-out is dead code and the "+
			"rule it protects may have been loosened instead:\n%s", card)

	// But it is the ONLY match: a card where the sweep finds two empty spans
	// has a second defect the carve-out is not covering, and the sweep would
	// be relying on the exemption for more than the one live region.
	assert.Equal(t, 1, len(railEmptySpanRE.FindAllString(card, -1)),
		"the card holds more than the one exempt empty span, so the carve-out is hiding a real "+
			"defect rather than only the live region:\n%s", card)

	// The exemption is scoped to the hook, and the hook appears once. A
	// second chip's live region would be a second exemption.
	assert.Equal(t, 1, strings.Count(html, `data-krill="copy-task-id-status"`),
		"one chip, one live region: a second exempt element would mean the carve-out covers more "+
			"than it was written for")
}

// TestTheCopyChipCarveOutIsAnAttributeMatchNotAPositionMatch states the
// carve-out's residual reach, exactly, rather than leaving it for the next
// reader to discover.
//
// railCopyStatusSpanRE matches on the hook ATTRIBUTE wherever it appears in
// the card. It is not scoped to the Task id row. So an empty span that
// happens to carry data-krill="copy-task-id-status" in some OTHER row would
// be exempted too, and the sweep would not see it.
//
// That is a real hole, and this test is written to make it visible rather
// than to argue it away. It does NOT defend the hole -- it pins what the
// hole costs and what keeps it bounded:
//
//   - The sweep does not trip on such a span. Asserted, so nobody later
//     "fixes" a red test by loosening the sweep further.
//   - The hole is bounded by the hook being unique. Asserted, and the count
//     is one on the shipped page, so a second element reusing the hook is a
//     change the page-level count test in
//     TestTaskDetailRailCopyConfirmationIsAPoliteLiveRegion already fails on.
//
// The honest summary for the Validation lane: the carve-out is one attribute
// wide, not one row wide. Today that is exactly one element on one page. If a
// second empty element ever carries the hook, the fix is to anchor the regex
// on the Task id row, NOT to widen the exemption.
func TestTheCopyChipCarveOutIsAnAttributeMatchNotAPositionMatch(t *testing.T) {
	f := newDetailFixture(t)
	held := time.Now().Add(-time.Hour).UTC()
	lastSession := store.SessionID(uuid.New())
	task := f.add(store.Task{Title: "carve-out-reach", CurrentLane: store.LaneTesting})
	f.store.lastClaim = store.Claim{
		ID: uuid.New(), TaskID: task.ID, SessionID: lastSession,
		ClaimedAt: held, ReleasedAt: &held, ReleaseReason: strPtr("complete"),
	}

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	card := propertiesOf(t, html)
	carrier := `<span class="font-mono text-xs">` + lastSession.String() + `</span>`
	require.Contains(t, card, carrier, "the claim row's holder span moved:\n%s", card)

	// The stated limit: an empty span carrying the hook, in a row that has
	// nothing to do with the copy chip, is exempted along with the live
	// region. Stated as a fact about the shipped regex, not as a wish.
	reused := strings.Replace(card, carrier, `<span data-krill="copy-task-id-status"></span>`, 1)
	require.NotEqual(t, card, reused)
	assert.False(t, sweepTrips(reused),
		"the carve-out's exemption is one ATTRIBUTE wide, not one ROW wide: an empty span "+
			"carrying the live region's hook is exempted wherever it sits. If this has become true, "+
			"the exemption was widened; if it should stop being true, anchor railCopyStatusSpanRE "+
			"on the Task id row rather than loosening the sweep:\n%s", reused)

	// What bounds it: the hook is unique to the one live region, and the
	// count is asserted at page level. This is the assertion that would fail
	// first if a second element ever reused the hook.
	assert.Equal(t, 1, strings.Count(html, `data-krill="copy-task-id-status"`),
		"the carve-out's exemption is as wide as the number of elements carrying the hook; "+
			"that number is one, and this is what keeps the limit above harmless")
}

// TestTaskDetailRailCopyChipLivesOutsideEverySwapTarget is the swap half of
// the placement claim, stated as a property of the markup rather than of the
// script.
//
// The task body requires the behaviour to survive every tab swap without
// being re-bound, and the head placement is what delivers that. But the chip
// itself is in the rail, and the rail is inside the region the Refresh button
// replaces -- so "outside every swap target" is not literally true and must
// not be asserted as if it were. What IS true, and what makes the head
// placement load-bearing, is:
//
//	the chip's BEHAVIOUR is never inside a swapped region, so it is never
//	re-requested and never re-parsed; and the script re-binds on every swap
//	anyway, so a chip that IS re-requested comes back working.
//
// This pins the first half from the markup side: the chip's row is inside
// the Refresh target (which is why the rebind path matters), and no script
// rides along with it (which is why the behaviour cannot die with the swap).
func TestTaskDetailRailCopyChipLivesOutsideEverySwapTarget(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "swap-survival", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	// The Refresh button replaces the whole detail section by outerHTML --
	// the section the rail is in. So a Refresh DOES re-request the chip, as
	// fresh disabled markup, and the after:swap rebind is the only thing that
	// revives it. This is the load the head placement is carrying.
	refresh := regionBetween(t, html, `data-krill="refresh"`, "</button>")
	require.Contains(t, refresh, `hx-target="#`+pages.TaskDetailAnchor+`"`)
	require.Contains(t, refresh, `hx-swap="outerHTML"`)

	// The chip is inside that replaced region. The HX fragment served IS the
	// one element the Refresh replaces, so the chip being in the fragment is
	// the claim. Read with the sound top-level parser rather than a naive
	// `</section>` cut, which would close on the content column's inner
	// section before the rail is ever reached.
	require.Len(t, fragmentTopLevelElements(html), 1,
		"the HX fragment must be exactly the one element the Refresh replaces:\n%s", html)
	assert.Contains(t, html, `data-krill="copy-task-id"`,
		"the chip must be inside the swapped region; if it ever moves out, this test's premise "+
			"changes and the rebind path would need re-justifying")

	// And the behaviour does NOT ride along with it. A chip re-requested by
	// every Refresh would come back with whatever inline handler it shipped
	// with; the head's is unaffected by the swap.
	assert.NotContains(t, html, "<script",
		"no script may live inside the swapped detail region, or it dies with every Refresh")

	// Both halves of the head's answer, asserted together: it survives the
	// swap (it is in the head) and it revives a swapped-in chip (it rebinds).
	head := buildHead()
	assert.Contains(t, head, copyTaskIdScript, "the behaviour lives in the head, outside every swap")
	assert.Contains(t, copyTaskIdScript,
		"document.addEventListener('htmx:after:swap',function(e){upgrade(e.target);});",
		"a Refresh re-requests the chip disabled; only an after:swap upgrade over the swapped "+
			"subtree revives it")
}

// TestTaskDetailRailCopyChipAccessibilityContract is the confirmation's
// accessibility contract read off the MARKUP and the SCRIPT together, as
// distinct claims.
//
// The script-level test already asserts the script never calls .focus() and
// never uses alert/confirm. What it cannot see is the rendered page: whether
// the chip itself is reachable by keyboard, whether the live region is
// actually a live region, and whether the outcome is carried by a word a
// screen reader reads out rather than by a colour class.
//
// The keyboard clause is the one worth stating: a copy chip that cannot be
// reached by Tab is not an accessible control at all, however well its
// live region is written, and the script's removeAttribute('disabled') is
// exactly what makes it reachable again after the head runs.
func TestTaskDetailRailCopyChipAccessibilityContract(t *testing.T) {
	f := newDetailFixture(t)
	task := f.add(store.Task{Title: "a11y-chip", CurrentLane: store.LaneTesting})

	code, html := f.getProductScoped(task.ID.String(), true)
	require.Equal(t, 200, code, "body: %s", html)

	row := regionBetween(t, html, `data-krill="task-properties-id"`, "</dd>")

	// Reachable by keyboard once the head has run. It is NOT reachable on
	// arrival -- that is the deliberate disabled state -- and the script's
	// removal of disabled is what makes it reachable, so both halves are
	// asserted rather than the convenient one.
	assert.Contains(t, row, `" disabled`, "the chip ships disabled, before the head runs")
	assert.NotContains(t, row, "tabindex",
		"a tabindex on the chip would either remove it from the tab order or reorder it, and "+
			"neither is wanted: the script's removeAttribute('disabled') is what restores it")

	// The live region is a live region, is polite rather than assertive, and
	// is announced without moving focus: no tabindex, no autofocus, and
	// nothing focusable inside it.
	status := regexp.MustCompile(`<span[^>]*data-krill="copy-task-id-status"[^>]*>(?s).*?</span>`)
	span := status.FindString(row)
	require.NotEmpty(t, span, "no live region beside the chip:\n%s", row)
	assert.Contains(t, span, `role="status"`)
	assert.Contains(t, span, `aria-live="polite"`)
	for _, forbidden := range []string{"tabindex", "autofocus", "<button", "<a ", "<input"} {
		assert.NotContains(t, strings.ToLower(span), forbidden,
			"the confirmation must not be focusable; %q in it would move the operator's place", forbidden)
	}

	// And the outcome is a word in both states. The success word is asserted
	// on the script (it only exists there); the failure message too. Neither
	// may be conveyed by text-success / text-error alone, which a monochrome
	// display and a screen reader both render as nothing.
	assert.Contains(t, copyTaskIdScript, "'Copied'",
		"the success confirmation must be a word, not the success colour")
	assert.Contains(t, copyTaskIdScript, "Could not copy. The id is selected - press Ctrl+C.",
		"the failure confirmation must name what happened and what to do, not only turn red")
	assert.NotContains(t, copyTaskIdScript, ".focus(",
		"the copy must not move focus; the operator is mid-task")
	assert.NotContains(t, copyTaskIdScript, "alert(",
		"an alert() blocks the operator and is announced by every screen reader at once")
	assert.NotContains(t, copyTaskIdScript, "confirm(",
		"a confirm() dialog for a copy is a modal interruption, not an in-place confirmation")
}
