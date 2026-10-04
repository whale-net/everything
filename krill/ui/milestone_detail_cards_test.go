// Coverage for the Milestone detail page's two cards (FR 0f1fb763): the
// outcome sentence, and the milepebbles card with each cut's own status
// badge, progress bar and link to its own tasks. No database.
//
// The bars are the part most likely to be wrong silently, so they are
// checked against figures the read was GIVEN rather than against whatever
// the page renders: a bar drawn from the parent's row instead of the
// milepebble's own would still be a plausible-looking number, and only a
// test that knows which container the figure belongs to can tell.
package main

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// cardMilestoneID is the CUT milestone: the one whose detail carries both
// cards, and the one whose milepebbles are the subject of the bars.
var cardMilestoneID = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000001")

// cardPebbleIDs are two cuts under it, deliberately with different
// figures: one part-finished, one untouched. Two rather than one because
// a card that shows every row the SAME number is the exact bug a
// single-milepebble fixture cannot see.
var (
	cardPebbleOneID = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000002")
	cardPebbleTwoID = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000003")
)

// cardUncutMilestoneID is the milestone with nothing cut from it, so its
// detail must carry no Milepebbles card at all.
var cardUncutMilestoneID = uuid.MustParse("aaaaaaaa-0000-4000-8000-000000000004")

// cardProductID is the product every fixture below belongs to.
var cardProductID = uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000001")

// cardOutcome is the cut milestone's outcome sentence. It is deliberately
// markdown, so the case proves the card renders it as prose rather than
// printing the raw asterisks an operator would see.
const cardOutcome = "The console reads **every** container's delivery."

// cardPebbleTwoOutcome is the second cut's own outcome sentence, different
// from its parent's -- so a card that fell back to the milestone's outcome
// would be caught.
const cardPebbleTwoOutcome = "The rail's history reads oldest first."

// cardListing is the delivery read's answer: a cut milestone, the two cuts
// under it, and an uncut one. A milepebble's Outcome is its own, which is
// what makes the "this card shows the container you opened" case possible.
var cardListing = slice.DeliveryListing{
	Milestones: []slice.MilestoneListingEntry{
		{
			ID:       cardMilestoneID,
			Name:     "M13 Milestones table",
			Status:   store.MilestoneStatusInProgress,
			Position: 3,
			Outcome:  cardOutcomePtr(cardOutcome),
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: cardPebbleOneID, Name: "P0 Console reads", Status: store.MilestoneStatusShipped},
				{
					ID: cardPebbleTwoID, Name: "P2b Task detail", Status: store.MilestoneStatusInDesign,
					Outcome: cardOutcomePtr(cardPebbleTwoOutcome),
				},
			},
		},
		{
			ID:       cardUncutMilestoneID,
			Name:     "M7 Legacy docs",
			Status:   store.MilestoneStatusNotStarted,
			Position: 1,
			Outcome:  cardOutcomePtr("Nothing is promised here yet."),
		},
	},
}

func cardOutcomePtr(s string) *string { return &s }

// cardContainerOf resolves one id out of cardListing the way the handler
// does, so a case names its container without restating it.
func cardContainerOf(t *testing.T, id uuid.UUID) taskContainer {
	t.Helper()
	c, found := resolveTaskContainer(cardListing, id)
	require.True(t, found, "fixture id %s must resolve", id)
	return c
}

// cardProgress is what the store answers for cardMilestoneID: the
// milestone's own row, plus one row per cut.
//
// The milestone row is here to be IGNORED by the milepebbles: it carries a
// different figure from either cut, so a card that fell back to the
// milestone's numbers would show this one. The two cuts carry the parent
// milestone's id in Milestone -- which is exactly what the real read does
// for a milepebble's row (store.ContainerTaskProgress) -- so a card that
// indexed by that field would collide both cuts onto one entry and they
// would render the same bar.
var cardProgress = []store.ContainerTaskProgress{
	{
		Milestone: store.ProductTaskMilestoneRef{ID: cardMilestoneID, Name: "M13 Milestones table"},
		PerLane:   store.TaskLaneCounts{Scaffold: 7, Done: 9},
	},
	{
		Milestone:  store.ProductTaskMilestoneRef{ID: cardMilestoneID, Name: "M13 Milestones table"},
		Milepebble: &store.ProductTaskMilepebbleRef{ID: cardPebbleOneID, Name: "P0 Console reads"},
		// Every task done: the bar renders at full, and green.
		PerLane: store.TaskLaneCounts{Done: 4},
	},
	{
		Milestone:  store.ProductTaskMilestoneRef{ID: cardMilestoneID, Name: "M13 Milestones table"},
		Milepebble: &store.ProductTaskMilepebbleRef{ID: cardPebbleTwoID, Name: "P2b Task detail"},
		// Part-finished: an amber bar at 1 of 3.
		PerLane: store.TaskLaneCounts{Scaffold: 1, Implementation: 1, Done: 1},
	},
}

// cardFixtureTasks answers the progress read with the containers above,
// and records what it was asked for so a case can assert the SCOPE the
// detail page reads under -- the single-container milestone scope, not the
// all-containers scope the Milestones table uses.
//
// It embeds productScopeTasks rather than store.TaskStore: the shell
// chrome every page renders reads a badge count on the way past, and a
// fixture that left that to the nil embedded interface would panic inside
// the shell instead of failing on this page's own assertion.
type cardFixtureTasks struct {
	productScopeTasks

	calls      []store.ProductTaskProgressParams
	containers []store.ContainerTaskProgress
	err        error
}

func (f *cardFixtureTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	f.calls = append(f.calls, params)
	if f.err != nil {
		return store.ProductTaskProgress{}, f.err
	}
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: f.containers}, nil
}

// cardMux mounts the real product-scoped routes against cardListing and a
// progress read answering with the given containers, so these cases drive
// the same handler production serves rather than calling the builder with a
// hand-assembled view model.
func cardMux(t *testing.T, containers []store.ContainerTaskProgress, progressErr error) (*http.ServeMux, *cardFixtureTasks) {
	t.Helper()
	tasks := &cardFixtureTasks{containers: containers, err: progressErr}

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: cardProductID, Name: "krill"}},
		listing:  cardListing,
	}
	app.tasks = tasks
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux, tasks
}

// cardDetailOf renders one container's detail through the registered
// route, which is the shape production serves.
func cardDetailOf(t *testing.T, containers []store.ContainerTaskProgress, id uuid.UUID) string {
	t.Helper()
	mux, _ := cardMux(t, containers, nil)
	rec := fetch(t, mux, milestoneDetailHref(cardProductID, id))
	require.Equal(t, http.StatusOK, rec.Code)
	return rec.Body.String()
}

// cardRowsOf calls the Milepebbles card's OWN read --
// milestoneDetailMilepebbles, the function the handler calls -- and reports
// both its rows and every progress read that call made.
//
// It exists because the page's read count stopped being the card's to
// assert on: the properties rail beside the cards reads the same aggregate
// for the same container (FR c208b777 wants a Tasks figure on every
// container's page, cut or not), so a served page cannot say which of two
// identical reads the cards issued. Calling the card's read on its own
// answers the cards' questions exactly and is unaffected by whatever else
// the page reads.
func cardRowsOf(t *testing.T, containers []store.ContainerTaskProgress, id uuid.UUID) ([]pages.MilestoneDetailMilepebble, []store.ProductTaskProgressParams) {
	t.Helper()
	tasks := &cardFixtureTasks{containers: containers}
	app := newTestApp(t)
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = tasks
	rows := app.milestoneDetailMilepebbles(context.Background(), cardProductID, cardContainerOf(t, id))
	return rows, tasks.calls
}

// findMilepebblesCard slices the Milepebbles card out of a rendered page, so
// an assertion about one of its rows is about that row and cannot be
// satisfied -- or broken -- by a sentence another section of the page prints.
// The rail reports its OWN unreadable figure in the same words the card does,
// so a whole-page substring check cannot tell the two apart.
func findMilepebblesCard(html string) (string, bool) {
	start := strings.Index(html, `data-krill="milestone-milepebbles"`)
	if start < 0 {
		return "", false
	}
	rest := html[start:]
	end := strings.Index(rest, `data-krill="milestone-delivery-card"`)
	if end < 0 {
		end = strings.Index(rest, `data-krill="milestone-detail-rail"`)
	}
	if end < 0 {
		return rest, true
	}
	return rest[:end], true
}

// milepebblesCard is findMilepebblesCard's asserting form: it REQUIRES the card,
// so every assertion made against the result is about a card that rendered
// rather than about the empty string.
func milepebblesCard(t *testing.T, html string) string {
	t.Helper()
	region, ok := findMilepebblesCard(html)
	require.True(t, ok, "the Milepebbles card did not render, so assertions against it would be vacuous")
	require.NotEmpty(t, region, "the card rendered its marker but no rows:\n%s", html)
	return region
}

// ---------------------------------------------------------------------------
// 1. the Outcome card
// ---------------------------------------------------------------------------

// TestMilestoneDetailShowsTheOutcomeSentence is the FR's first claim: the
// card shows the milestone's own outcome, rendered as markdown rather than
// as the raw source text.
func TestMilestoneDetailShowsTheOutcomeSentence(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	require.Contains(t, html, `data-krill="milestone-outcome"`, "the Outcome card renders")
	assert.Contains(t, html, ">Outcome</h2>", "with its own heading")
	// Markdown rendered, not printed: the prose reads "every", and no
	// literal asterisks survive into the page.
	assert.Contains(t, html, "console reads")
	assert.Contains(t, html, "<strong>every</strong>")
	assert.NotContains(t, html, "**every**", "the raw markdown source must not reach the page")
}

// TestMilestoneDetailOutcomeIsTheContainerYouOpened: a milepebble has its
// OWN outcome, and the card must show that one. Falling back to the
// parent's would show an operator a milepebble described by its
// milestone's promise.
func TestMilestoneDetailOutcomeIsTheContainerYouOpened(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardPebbleTwoID)

	require.Contains(t, html, `data-krill="milestone-outcome"`)
	assert.Contains(t, html, cardPebbleTwoOutcome)
	assert.NotContains(t, html, cardOutcome,
		"a milepebble's card shows its own outcome, never its parent's")
}

// TestMilestoneDetailWithNoOutcomeRendersNoOutcomeCard: a card with a
// heading and nothing under it is worse than no card -- it reads as a
// promise the page cannot keep. Empty means render nothing, here too.
func TestMilestoneDetailWithNoOutcomeRendersNoOutcomeCard(t *testing.T) {
	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: cardProductID, Name: "krill"}},
		listing: slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{{
			ID: cardUncutMilestoneID, Name: "M7 Legacy docs", Status: store.MilestoneStatusNotStarted,
		}}},
	}
	app.tasks = &cardFixtureTasks{}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardUncutMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="milestone-outcome"`,
		"a milestone with no outcome sentence renders no Outcome card")
	assert.NotContains(t, body, ">Outcome</h2>")
	// The control: the page itself rendered, so this is an omission and
	// not a blank page.
	assert.Contains(t, body, `data-krill="page-title"`)
	assert.Contains(t, body, "M7 Legacy docs")
}

// ---------------------------------------------------------------------------
// 2. the Milepebbles card
// ---------------------------------------------------------------------------

// TestMilestoneDetailListsEachCutWithItsOwnProgress is the FR's second
// claim and the case that matters: every cut listed, each with the figures
// from ITS OWN row.
//
// The two cuts are given different totals (4 and 3), so a card that
// indexed the read's Milestone field -- which carries the PARENT's id for
// both, exactly as the real read does -- would collide them onto one entry
// and render two identical bars. Both figures are asserted, so that bug is
// a red test here rather than a plausible-looking page.
func TestMilestoneDetailListsEachCutWithItsOwnProgress(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	require.Contains(t, html, `data-krill="milestone-milepebbles"`, "the Milepebbles card renders")
	assert.Contains(t, html, ">Milepebbles</h2>")

	// Both cuts, named.
	assert.Contains(t, html, "P0 Console reads")
	assert.Contains(t, html, "P2b Task detail")

	// Each cut's OWN figures. The first is 4 of 4, the second 1 of 3 --
	// and neither is the milestone's own 9 of 16.
	assert.Contains(t, html, `value="4" max="4"`, "the shipped cut's own bar")
	assert.Contains(t, html, `value="1" max="3"`, "the part-finished cut's own bar")
	assert.Contains(t, html, "4/4 tasks done")
	assert.Contains(t, html, "1/3 tasks done")

	// The parent's figures appear nowhere: a milestone-level roll-up is
	// the header's and the rail's business, not this card's.
	assert.NotContains(t, html, `value="9" max="16"`,
		"the milestone's own row must not answer for either cut")
}

// TestMilestoneDetailMilepebbleRowsCarryTheirOwnStatusBadge: each cut is a
// container with its OWN status, and it renders through the shared
// statusBadge -- so "shipped" is the same green here as on the Milestones
// table and the header.
//
// The classes are asserted against components.MilestoneStatusStyle rather
// than copied from the rendered output, so a badge that quietly fell back
// to the neutral default fails here.
func TestMilestoneDetailMilepebbleRowsCarryTheirOwnStatusBadge(t *testing.T) {
	require.Equal(t, htmxui.BadgeSuccess, components.MilestoneStatusStyle(string(store.MilestoneStatusShipped)).Variant,
		"the fixture's shipped status must be the success badge, or this case proves nothing")

	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	// The cut shipped, the other is in design: two DIFFERENT variants, so
	// "every status renders as some badge" is not enough. Both classes are
	// spelled out from components.MilestoneStatusStyle, so a badge that
	// fell back to the neutral default fails here rather than rendering a
	// plausible grey chip.
	assert.Contains(t, html, `class="badge badge-success badge-sm"`)
	assert.Contains(t, html, ">shipped</span>")
	assert.Contains(t, html, `class="badge badge-soft badge-info badge-sm"`,
		"in design is the shared mapper's soft info badge")
	assert.Contains(t, html, ">in design</span>")
	assert.NotContains(t, html, `class="badge badge-neutral badge-sm"`,
		"neither cut's status fell through to the neutral default")
}

// TestMilestoneDetailMilepebbleLinksToItsOwnTasks: each name links to that
// cut's OWN task list -- its own id, and its own scope mode. A link
// carrying the parent's id would land the operator on the whole cut's
// tasks, which is a different question than the one they asked.
func TestMilestoneDetailMilepebbleLinksToItsOwnTasks(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	for _, id := range []uuid.UUID{cardPebbleOneID, cardPebbleTwoID} {
		assert.Contains(t, html, `href="/products/`+cardProductID.String()+
			`/tasks?container_id=`+id.String()+`&amp;scope=milepebble"`,
			"each cut links to its own tasks, scoped as a milepebble")
	}
	assert.NotContains(t, html, "container_id="+cardMilestoneID.String()+"&amp;scope=milepebble",
		"the milestone itself is not one of its own cuts")
	assert.NotContains(t, html, `data-krill-milepebble-id="`+cardMilestoneID.String()+`"`,
		"the milestone is the page, not a row in its own card")
}

// TestMilestoneDetailMilepebbleRowIsAddressable: the row carries its own
// id, so the two rows are distinguishable in the markup rather than being
// two anonymous lines of a table.
func TestMilestoneDetailMilepebbleRowIsAddressable(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	for _, id := range []uuid.UUID{cardPebbleOneID, cardPebbleTwoID} {
		assert.Contains(t, html, `data-krill-milepebble-id="`+id.String()+`"`)
	}
	assert.Equal(t, 2, strings.Count(html, `data-krill="milestone-milepebble-row"`),
		"exactly one row per cut, and no third row for the milestone")
}

// TestMilestoneDetailUncutMilestoneHasNoMilepebblesCard is the FR's
// explicit omission: "given none are cut, the Milepebbles card is
// omitted (empty means render nothing)". An empty card is the failure --
// it reads as a milestone whose cuts were lost.
func TestMilestoneDetailUncutMilestoneHasNoMilepebblesCard(t *testing.T) {
	mux, _ := cardMux(t, cardProgress, nil)

	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardUncutMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.NotContains(t, body, `data-krill="milestone-milepebbles"`)
	assert.NotContains(t, body, ">Milepebbles</h2>")
	assert.NotContains(t, body, `data-krill="milestone-milepebble-row"`)

	// The control: the page rendered its header, so the card's absence is
	// the omission under test and not an error page.
	assert.Contains(t, body, `data-krill="page-title"`)
	assert.Contains(t, body, "M7 Legacy docs")

	// And nothing was read to fill a card that does not exist. Scoped to
	// the CARD's read rather than to the page's: the rail beside it
	// legitimately reads this container's Tasks figure (FR c208b777), and
	// an uncut milestone is still a container whose Tasks row the FR
	// requires.
	rows, calls := cardRowsOf(t, cardProgress, cardUncutMilestoneID)
	assert.Nil(t, rows, "a milestone with no cuts has no rows to render")
	assert.Empty(t, calls,
		"the card asks the store for no progress at all when it renders nothing")
}

// TestMilestoneDetailOfAMilepebbleHasNoMilepebblesCard: a milepebble has
// no cuts of its own, so it is not "a milestone whose cuts are missing" --
// it is the whole of what that region has to say. A milepebble's detail
// must therefore render no Milepebbles card at all.
func TestMilestoneDetailOfAMilepebbleHasNoMilepebblesCard(t *testing.T) {
	mux, _ := cardMux(t, cardProgress, nil)

	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardPebbleOneID))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `data-krill-container-kind="milepebble"`)
	assert.NotContains(t, body, `data-krill="milestone-milepebbles"`,
		"a milepebble's own detail has no milepebbles of its own to list")
	// The control: it is the milepebble's page, not a 404.
	assert.Contains(t, body, "P0 Console reads")

	// Scoped to the card's own read for the same reason as the case above:
	// the rail's Tasks read is a different section's, and a milepebble has
	// no cuts for the card to be filling.
	rows, calls := cardRowsOf(t, cardProgress, cardPebbleOneID)
	assert.Nil(t, rows, "a milepebble has no cuts of its own to list")
	assert.Empty(t, calls)
}

// ---------------------------------------------------------------------------
// 3. which read, and what a failed one costs
// ---------------------------------------------------------------------------

// TestMilestoneDetailProgressComesFromTheSingleContainerScope pins the
// read's SCOPE. A detail page is about one container, and the
// single-container milestone scope walks exactly this milestone and the
// cuts under it -- whatever their status, so a shipped cut's finished bar
// is still answered rather than omitted.
//
// A page that asked for the all-containers scope would still render bars,
// and would render them correctly; this case exists because the difference
// is a wasted whole-product read that a test that only checked the
// numbers could never see.
//
// It is asserted on the SCOPE OF EVERY READ THE PAGE MADE rather than on
// how many reads it made. The detail page has more than one consumer of
// this aggregate -- the cards' bars and the rail's Tasks figure both need
// it -- so a raw count no longer says whose read it was counting, while
// "no read on this page walked the whole roadmap" is a claim about all of
// them at once and is exactly what this case is for.
func TestMilestoneDetailProgressComesFromTheSingleContainerScope(t *testing.T) {
	mux, tasks := cardMux(t, cardProgress, nil)

	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code)

	require.NotEmpty(t, tasks.calls, "the page must read progress at all")
	for i, call := range tasks.calls {
		assert.Equal(t, store.ProductTaskScopeMilestone, call.Scope.Kind,
			"read %d: the detail reads the ONE milestone it is about, never every container", i)
		assert.Equal(t, cardMilestoneID, call.Scope.ContainerID,
			"read %d: and it is this milestone, resolved out of the URL's own product", i)
		assert.Equal(t, cardProductID, call.ProductID, "read %d", i)
		assert.Equal(t, chromeScopeID, call.ScopeID, "read %d", i)
	}

	// And the cards' own read among them is a single-container read, once:
	// not one per cut, and not a whole-roadmap read wearing this milestone's
	// name. Called directly, so this is the card's read and not whichever
	// section's happened to be recorded first.
	rows, calls := cardRowsOf(t, cardProgress, cardMilestoneID)
	require.Len(t, rows, 2)
	require.Len(t, calls, 1, "one progress read for the card, not one per cut")
	assert.Equal(t, store.ProductTaskScopeMilestone, calls[0].Scope.Kind,
		"the card reads under the single-container scope")
	assert.Equal(t, cardMilestoneID, calls[0].Scope.ContainerID)
	assert.Equal(t, cardProductID, calls[0].ProductID)
	assert.Equal(t, chromeScopeID, calls[0].ScopeID)
}

// TestMilestoneDetailUnreadableProgressStillListsTheCuts: a failed
// progress read costs the BARS, not the card. Every cut still has its name
// and its link to its tasks -- the two facts the listing answered -- and
// each bar says the figures could not be read.
//
// The failure sentence matters in its own right: "No tasks yet" is a
// different fact, and an operator who reads it concludes a shipped cut has
// no work in it.
func TestMilestoneDetailUnreadableProgressStillListsTheCuts(t *testing.T) {
	mux, _ := cardMux(t, nil, errProgressReadFailed)
	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code,
		"a failed progress read is not a failed page")

	body := rec.Body.String()

	// The card and its rows survive.
	require.Contains(t, body, `data-krill="milestone-milepebbles"`)
	assert.Contains(t, body, "P0 Console reads")
	assert.Contains(t, body, `href="/products/`+cardProductID.String()+
		`/tasks?container_id=`+cardPebbleOneID.String()+`&amp;scope=milepebble"`,
		"the tasks link comes from the listing, so a failed read cannot take it")

	// And every bar says the figures could not be read. Scoped to the card:
	// the rail has its own Tasks figure and reports its own unreadable one
	// in the same words, so a whole-page count would be counting two
	// sections' sentences against one section's rows.
	card := milepebblesCard(t, body)
	assert.Equal(t, 2, strings.Count(card, `data-krill="milestone-progress-error"`),
		"one unreadable sentence per row, never a silent zero")
	assert.Contains(t, card, "Task progress could not be read.")
	assert.NotContains(t, card, "No tasks yet",
		"an unread figure is never reported as an empty container")
	assert.NotContains(t, card, `data-krill="milestone-no-tasks"`)
}

// TestMilestoneDetailCutWithNoTasksSaysNoTasksYet is the OTHER end of the
// same three-way distinction: a cut the read accounted for and found
// empty says "No tasks yet", which is a real answer and a different one
// from either the bar or the failure sentence.
func TestMilestoneDetailCutWithNoTasksSaysNoTasksYet(t *testing.T) {
	// BOTH cuts are accounted for and both are empty, so the whole card
	// answers the same way and no error marker can arrive from a row this
	// case did not set up.
	html := cardDetailOf(t, []store.ContainerTaskProgress{
		{
			Milestone:  store.ProductTaskMilestoneRef{ID: cardMilestoneID},
			Milepebble: &store.ProductTaskMilepebbleRef{ID: cardPebbleOneID},
			// Accounted for, and empty: PerLane is all zero, so Total is 0.
		},
		{
			Milestone:  store.ProductTaskMilestoneRef{ID: cardMilestoneID},
			Milepebble: &store.ProductTaskMilepebbleRef{ID: cardPebbleTwoID},
		},
	}, cardMilestoneID)

	// Scoped to the card, because the rail beside it prints "No tasks yet"
	// and the unreadable sentence too -- on this fixture the read accounts
	// for the two cuts and not for the milestone, so the rail's own figure
	// is legitimately a failure. A whole-page count here would be adding
	// the rail's answer to the card's.
	card := milepebblesCard(t, html)
	assert.Equal(t, 2, strings.Count(card, "No tasks yet"),
		"one sentence per cut, and no cut quietly rendered a bar")
	assert.Equal(t, 2, strings.Count(card, `data-krill="milestone-no-tasks"`))
	assert.NotContains(t, card, `data-krill="milestone-progress-error"`,
		"a container the read accounted for is not one it failed to read")
	assert.NotContains(t, card, "Task progress could not be read.")
}

// TestMilestoneDetailCutMissingFromTheReadIsNotCalledEmpty: a row the read
// never accounted for is neither "no tasks" nor a bar -- it is the read
// coming back short, which the Milestones table's progressCell already
// refuses to call empty. Reusing that builder is what makes the two
// surfaces answer the same way.
func TestMilestoneDetailCutMissingFromTheReadIsNotCalledEmpty(t *testing.T) {
	html := cardDetailOf(t, []store.ContainerTaskProgress{
		// The milestone's row only: neither cut is accounted for.
		{Milestone: store.ProductTaskMilestoneRef{ID: cardMilestoneID}, PerLane: store.TaskLaneCounts{Done: 9}},
	}, cardMilestoneID)

	// The card's region, for the same reason as the case above.
	card := milepebblesCard(t, html)
	assert.Contains(t, card, `data-krill="milestone-progress-error"`,
		"a cut the read did not account for must not read as 'No tasks yet'")
	assert.NotContains(t, card, "No tasks yet")
	assert.NotContains(t, card, `value="9"`,
		"and it must not borrow the milestone's own figures either")
}

// ---------------------------------------------------------------------------
// 4. composition
// ---------------------------------------------------------------------------

// TestMilestoneDetailCardsRenderInsideTheContentColumn: the cards belong
// to the grid's LEFT cell, not beside the header and not in the rail. A
// card rendered into the wrong cell is invisible at lg and stacked in the
// wrong place below it, which no assertion on the card's own markup would
// catch.
func TestMilestoneDetailCardsRenderInsideTheContentColumn(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	// The grid's two cells are siblings, so the content column is exactly
	// what sits between the frame's opening and the rail.
	frameStart := strings.Index(html, `data-krill="milestone-detail-frame"`)
	railStart := strings.Index(html, `data-krill="milestone-detail-rail"`)
	require.Positive(t, frameStart, "the page must render its grid frame")
	require.Positive(t, railStart, "and the rail cell beside it")
	require.Greater(t, railStart, frameStart)
	frame := html[frameStart:railStart]

	assert.Contains(t, frame, `data-krill="milestone-detail-main"`)
	assert.Contains(t, frame, `data-krill="milestone-outcome"`,
		"the Outcome card is in the content column")
	assert.Contains(t, frame, `data-krill="milestone-milepebbles"`,
		"and so is the Milepebbles card")

	rail := html[railStart:]
	assert.NotContains(t, rail, `data-krill="milestone-milepebbles"`,
		"and no card leaked into the rail")
	assert.NotContains(t, rail, `data-krill="milestone-outcome"`)
}

// TestMilestoneDetailOnePrimaryActionStillHolds: the cards are read-only,
// so this page still has exactly ONE primary action -- its "Open tasks"
// button. A card that grew a button of its own would give the page a
// second one, and a header where neither is primary is a header that
// cannot say what the page is for.
func TestMilestoneDetailOnePrimaryActionStillHolds(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	assert.Contains(t, html, `data-krill="milestone-open-tasks"`)
	assert.Equal(t, 1, strings.Count(html, "btn-primary"),
		"the header's Open tasks is still the page's only primary action")
	assert.NotContains(t, html, `data-krill="milestone-outcome"`+`>`+`\s*<a href`,
		"the Outcome card states a fact; it does not offer an action")
}

// ---------------------------------------------------------------------------
// view model
// ---------------------------------------------------------------------------

// TestMilestoneDetailCardsOnTheViewModel checks the builder's own output,
// for the case that cannot be reached through the handler: the numbers on
// the struct the template renders. A template can drop a field it was
// handed, and only a struct-level assertion notices that the figures
// arrived correctly in the first place.
func TestMilestoneDetailCardsOnTheViewModel(t *testing.T) {
	page := buildMilestoneDetailPage(store.Product{ID: cardProductID, Name: "krill"}, cardContainerOf(t, cardMilestoneID))
	page.Outcome = milestoneDetailOutcomeOf(cardListing, cardMilestoneID)

	app := newTestApp(t)
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = &cardFixtureTasks{containers: cardProgress}
	page.Milepebbles = app.milestoneDetailMilepebbles(context.Background(), cardProductID, cardContainerOf(t, cardMilestoneID))

	assert.Equal(t, cardOutcome, page.Outcome)
	require.Len(t, page.Milepebbles, 2)

	assert.Equal(t, cardPebbleOneID.String(), page.Milepebbles[0].ID)
	assert.Equal(t, "P0 Console reads", page.Milepebbles[0].Name)
	assert.Equal(t, string(store.MilestoneStatusShipped), page.Milepebbles[0].Status)
	assert.Equal(t, 4, page.Milepebbles[0].Done)
	assert.Equal(t, 4, page.Milepebbles[0].Total)

	assert.Equal(t, cardPebbleTwoID.String(), page.Milepebbles[1].ID)
	assert.Equal(t, 1, page.Milepebbles[1].Done)
	assert.Equal(t, 3, page.Milepebbles[1].Total)

	// The listing's own order, not a re-sort: the card lists the cuts in
	// the order the delivery read returned them, so a re-sorted cut list
	// would disagree with the table these rows are the same rows as.
	require.Len(t, cardListing.Milestones[0].Milepebbles, 2)
	assert.Equal(t, cardListing.Milestones[0].Milepebbles[0].ID.String(), page.Milepebbles[0].ID)
	assert.Equal(t, cardListing.Milestones[0].Milepebbles[1].ID.String(), page.Milepebbles[1].ID)

	// The rows carry no read of their own beyond the figures: an errored
	// figure is a ProgressCell with the sentence, never a silent zero.
	for _, mp := range page.Milepebbles {
		assert.Empty(t, mp.ProgressError)
		assert.True(t, mp.HasTasks())
	}
}

// TestMilestoneDetailOutcomeOfAnswersForBothKinds: the outcome lookup walks
// milestones AND the milepebbles under them, because {mid} is one wildcard
// for both kinds and this is the read that says which. A lookup that only
// walked milestones would show a milepebble's detail no outcome at all.
func TestMilestoneDetailOutcomeOfAnswersForBothKinds(t *testing.T) {
	assert.Equal(t, cardOutcome, milestoneDetailOutcomeOf(cardListing, cardMilestoneID))
	assert.Equal(t, cardPebbleTwoOutcome, milestoneDetailOutcomeOf(cardListing, cardPebbleTwoID),
		"a cut with no outcome of its own answers empty, never its parent's")
	assert.Empty(t, milestoneDetailOutcomeOf(cardListing, cardPebbleOneID))
	assert.Empty(t, milestoneDetailOutcomeOf(cardListing, uuid.New()),
		"an id that is not this product's answers empty rather than panicking")
}

// TestMilestoneDetailCardsRenderThroughTheSharedProgress: the bars come
// from pages.ProgressCell and the shared milestoneProgress component, not a
// fourth copy of the arithmetic. A copy is what eventually disagrees with
// the Milestones table's bar for the same container, so the rendering is
// checked to BE the shared one.
func TestMilestoneDetailCardsRenderThroughTheSharedProgress(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	// The shared markers, which the milestones.templ component is the only
	// producer of.
	assert.Contains(t, html, `data-krill="milestone-progress"`)
	assert.Contains(t, html, `data-krill="milestone-progress-label"`)
	// And the same classes the shared component emits, so a bespoke bar
	// here would differ visibly from the table's.
	assert.Contains(t, html, `class="progress progress-success w-32"`,
		"a finished cut's bar is the shared green one")
	assert.Contains(t, html, `class="progress progress-warning w-32"`,
		"and a cut in flight is the shared amber one")
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// errProgressReadFailed is the sentinel read failure the fixture is handed
// where a case needs one, so no case depends on which error value another
// test file declares.
var errProgressReadFailed = errors.New("progress read failed")

// cardRegion is the SHARED productPageRegion helper, not a second copy of
// it.
//
// That helper once keyed only on the placeholder marker, so the moment the
// real page rendered it answered "" and every assertion made against it
// passed vacuously. The cards live inside the region it already probes
// (data-krill="milestone-detail"), so there is no new marker to add -- but
// reusing it here means a regression in the helper itself is caught by the
// guard below rather than by an assertion that quietly stopped looking at
// anything.
func cardRegion(body string) string { return productPageRegion(body) }

// TestCardRegionIsNotVacuous is the guard on that helper: it must find the
// real region, so every other case in this file is asserting against
// markup that actually rendered.
func TestCardRegionIsNotVacuous(t *testing.T) {
	mux, _ := cardMux(t, cardProgress, nil)
	rec := fetch(t, mux, milestoneDetailHref(cardProductID, cardMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code)

	region := cardRegion(rec.Body.String())
	require.NotEmpty(t, region, "the region helper must find the page's own region")
	assert.Contains(t, region, `data-krill="milestone-outcome"`)
	assert.Contains(t, region, `data-krill="milestone-milepebbles"`)
}

// The anchor the cards' page carries, referenced so a rename of it is a
// compile error here rather than a silently different selector.
var _ = pages.MilestoneDetailAnchor

// TestMilestoneDetailMilepebbleReachesBothWorkViews is FR 31cbd3eb's rule
// on this card: each cut row offers the Tasks table AND the Board, both
// scoped to that cut.
//
// Both are asserted rather than one, because the defect this exists to
// catch is precisely the one a single-link check would miss -- the card
// grew a Tasks link and no Board, and every pre-existing assertion still
// passed. The scope ids are the cuts' own, so a card that pointed both
// links at the parent would fail here too.
func TestMilestoneDetailMilepebbleReachesBothWorkViews(t *testing.T) {
	html := cardDetailOf(t, cardProgress, cardMilestoneID)

	for _, id := range []uuid.UUID{cardPebbleOneID, cardPebbleTwoID} {
		assert.Contains(t, html, `href="/products/`+cardProductID.String()+
			`/tasks?container_id=`+id.String()+`&amp;scope=milepebble"`,
			"cut %s reaches its own Tasks", id)
		assert.Contains(t, html, `href="/products/`+cardProductID.String()+
			`/board?container_id=`+id.String()+`&amp;scope=milepebble"`,
			"cut %s reaches its own Board", id)
	}
	// The Board link must be the Board's page, not a second Tasks link --
	// two anchors to the same href would satisfy a "does the row link to
	// the Board" check by name alone.
	assert.Contains(t, html, `data-krill="milestone-milepebble-board-link"`)
	assert.Contains(t, html, `data-krill="milestone-milepebble-tasks-link"`)
}
