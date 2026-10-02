// Acceptance coverage for the product-wide Board's swimlanes (FR
// cf000440): one lane per milestone that has tasks in milestone position
// descending order, headers carrying the name, the status badge and the
// progress read's own "N of M done", five counted columns per lane, the
// horizontal scroller, the empty state, and the absence of anything that
// moves a card.
//
// Every case drives the real route through the real registrations, so
// "which view a URL serves" and "what the shell renders around it" are
// part of what is pinned -- a builder-only case could pass while the Board
// path served the Tasks view.
package main

import (
	"context"
	"errors"
	"net/http"
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

// boardFixtureTasks answers both of the reads a board makes: the task rows
// and the per-container progress aggregate. It embeds recordingProductTasks
// so the row read keeps recording its parameters, and the progress read
// records its own alongside them.
//
// A method the store grew and neither records nil-panics instead of
// passing unnoticed, which is what keeps the fixture honest as the reads
// change.
type boardFixtureTasks struct {
	*recordingProductTasks

	progressed  []store.ProductTaskProgressParams
	containers  []store.ContainerTaskProgress
	progressErr error
}

func (s *boardFixtureTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	s.progressed = append(s.progressed, params)
	if s.progressErr != nil {
		return store.ProductTaskProgress{}, s.progressErr
	}
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: s.containers}, nil
}

// boardMux mounts the shell's real routes against a board fixture.
func boardMux(t *testing.T, rows []store.ProductTaskRow, total int, containers []store.ContainerTaskProgress) (*http.ServeMux, *boardFixtureTasks) {
	t.Helper()

	tasks := &boardFixtureTasks{
		recordingProductTasks: &recordingProductTasks{rows: rows, total: total},
		containers:            containers,
	}

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{{ID: productTaskProduct, Name: "krill"}},
		listing:  productTaskListing(),
	}
	app.tasks = tasks
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux, tasks
}

// boardURL is the Board URL for one product with the given raw query,
// written out rather than built from the constants so the cases do not
// depend on the names they exercise.
func boardURL(query string) string {
	url := "/products/" + productTaskProduct.String() + "/board"
	if query != "" {
		url += "?" + query
	}
	return url
}

// boardBody fetches the Board and returns its markup.
func boardBody(t *testing.T, mux *http.ServeMux, query string) string {
	t.Helper()
	rec := fetch(t, mux, boardURL(query))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// swimlaneIDs reads the swimlanes' container ids out of the markup, in
// rendered order -- which is the order the requirement is about.
func swimlaneIDs(body string) []string {
	matches := regexp.MustCompile(`data-krill="swimlane" data-krill-container-id="([^"]+)"`).FindAllStringSubmatch(body, -1)
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		ids = append(ids, m[1])
	}
	return ids
}

// swimlaneOf returns one swimlane's own markup -- from its opening tag to
// its close -- so a case can assert about one lane's header and columns
// without another lane's values matching by accident. A swimlane nests no
// section of its own, so its own close is the end of it.
//
// The search is anchored on the swimlane element rather than on the bare
// container id, because the scope control's own options carry that id too
// and a search that loose would hand back the select.
func swimlaneOf(t *testing.T, body, containerID string) string {
	t.Helper()
	open := `<section data-krill="swimlane" data-krill-container-id="` + containerID + `"`
	start := strings.Index(body, open)
	require.NotEqual(t, -1, start, "no swimlane for %s in:\n%s", containerID, body)
	rest := body[start:]
	end := strings.Index(rest, "</section>")
	require.NotEqual(t, -1, end, "the swimlane never closes: %s", rest)
	return rest[:end]
}

// columnCountsOf reads one swimlane's five columns as "Lane=N" in rendered
// order, which is the order store.CanonicalLaneOrder names.
func columnCountsOf(lane string) []string {
	matches := regexp.MustCompile(`data-krill="board-column" data-krill-lane="([A-Za-z]+)"[^>]*>.*?data-krill="column-count"[^>]*>(\d+)</span>`).FindAllStringSubmatch(lane, -1)
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1]+"="+m[2])
	}
	return out
}

// boardRow is one task row as the read would answer for a milestone.
func boardRow(milestoneID uuid.UUID, milestoneName string, title string, lane store.Lane) store.ProductTaskRow {
	return store.ProductTaskRow{
		TaskID:      uuid.New(),
		Title:       title,
		Milestone:   store.ProductTaskMilestoneRef{ID: milestoneID, Name: milestoneName, Status: store.MilestoneStatusInProgress},
		CurrentLane: lane,
	}
}

// boardMilepebbleRow is one task row scoped to a milepebble, which the
// board aggregates under its milestone unless the milestone is out of
// scope itself.
func boardMilepebbleRow(milestoneID uuid.UUID, milestoneName string, milepebbleID uuid.UUID, milepebbleName, title string, lane store.Lane) store.ProductTaskRow {
	row := boardRow(milestoneID, milestoneName, title, lane)
	row.Milepebble = &store.ProductTaskMilepebbleRef{
		ID:     milepebbleID,
		Name:   milepebbleName,
		Status: store.MilestoneStatusInDesign,
	}
	return row
}

// boardProgressRow is one milestone's progress container.
func boardProgressRow(id uuid.UUID, name string, status store.MilestoneStatus, lanes store.TaskLaneCounts) store.ContainerTaskProgress {
	return store.ContainerTaskProgress{
		Milestone: store.ProductTaskMilestoneRef{ID: id, Name: name, Status: status},
		PerLane:   lanes,
	}
}

// TestBoardSwimlanesAreOnePerMilestoneInPositionDescendingOrder is the
// board's own ordering rule (FR cf000440): the rows arrive in the task
// read's milestone position DESCENDING order, and the swimlanes come out
// in exactly that order -- one per milestone, the highest position first.
//
// The fixture deliberately puts the newest milestone's rows FIRST, so a
// builder that sorted the lanes by name, by id or by the listing's own
// ascending order would render them the other way round and fail here.
func TestBoardSwimlanesAreOnePerMilestoneInPositionDescendingOrder(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "newest scaffolding", store.LaneScaffold),
		boardRow(productTaskNewestMilestone, "Newest milestone", "newest done", store.LaneDone),
		boardMilepebbleRow(productTaskMilestone, "Middle milestone", productTaskNewestMilepebble, "Middle milepebble", "middle testing", store.LaneTesting),
		boardRow(productTaskOldestMilestone, "Oldest milestone", "oldest validation", store.LaneValidation),
	}
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskOldestMilestone, "Oldest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Validation: 1}),
		boardProgressRow(productTaskMilestone, "Middle milestone", store.MilestoneStatusPlanned,
			store.TaskLaneCounts{Testing: 1}),
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInDesign,
			store.TaskLaneCounts{Scaffold: 1, Done: 1}),
	}
	mux, tasks := boardMux(t, rows, 4, containers)

	body := boardBody(t, mux, "")

	assert.Equal(t, []string{
		productTaskNewestMilestone.String(),
		productTaskMilestone.String(),
		productTaskOldestMilestone.String(),
	}, swimlaneIDs(body), "one swimlane per milestone, highest position first")
	// A milepebble's task aggregates under its milestone while the
	// milestone is itself in scope -- that is what the milestone's own
	// PerLane covers, and a lane of its own here would double-count it.
	assert.NotContains(t, body, productTaskNewestMilepebble.String()+`" data-krill-container-id`)

	require.Len(t, tasks.progressed, 1, "exactly one progress read per request")
	require.Len(t, tasks.listed, 1)
	assert.Equal(t, tasks.listed[0].Scope, tasks.progressed[0].Scope,
		"the board's rows and its headers must be read for the same scope")
}

// TestBoardSwimlaneHeaderCarriesNameStatusAndProgress pins the header's
// three parts: the name linking to the container's own detail page, that
// container's OWN status badge, and the progress read's own figures.
//
// The figures are the read's Done() and Total(), which is what keeps a
// cancelled task counted once rather than by a second rule spelled out in
// the view. The fixture's counts sum to more than the cards on the board
// precisely because the read answers for the whole container.
func TestBoardSwimlaneHeaderCarriesNameStatusAndProgress(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneImplementation),
	}
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 2, Implementation: 1, Done: 3}),
	}
	mux, _ := boardMux(t, rows, 1, containers)

	lane := swimlaneOf(t, boardBody(t, mux, ""), productTaskNewestMilestone.String())

	assert.Contains(t, lane, `data-krill="swimlane-name"`)
	assert.Contains(t, lane, "Newest milestone")
	assert.Contains(t, lane, "/milestones/"+productTaskNewestMilestone.String()+`"`,
		"the name links to the container's own detail page")
	assert.Contains(t, lane, "in progress", "the container's own status badge")
	// The read's own Done() and Total() handed over whole -- six tasks in
	// the container, three of them in the Done lane -- rather than a
	// figure summed from the one card this board is showing.
	assert.Contains(t, lane, `data-krill="swimlane-progress">3 of 6 done`)
}

// TestBoardSwimlaneHasFiveCountedColumnsIncludingZeros is FR cf000440's
// per-lane rule: exactly the five canonical lanes, in that order, each
// headed by its count -- a zero included, so an empty lane is visibly
// empty rather than absent.
func TestBoardSwimlaneHasFiveCountedColumnsIncludingZeros(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "scaffolding one", store.LaneScaffold),
		boardRow(productTaskNewestMilestone, "Newest milestone", "scaffolding two", store.LaneScaffold),
		boardRow(productTaskNewestMilestone, "Newest milestone", "testing one", store.LaneTesting),
	}
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 2, Testing: 1}),
	}
	mux, _ := boardMux(t, rows, 3, containers)

	body := boardBody(t, mux, "")
	lane := swimlaneOf(t, body, productTaskNewestMilestone.String())

	assert.Equal(t, []string{
		"Scaffold=2", "Implementation=0", "Testing=1", "Validation=0", "Done=0",
	}, columnCountsOf(lane), "store.CanonicalLaneOrder's five lanes, zeros included")
	// Each card sits in the column of its own lane and in no other.
	assert.Equal(t, 1, strings.Count(lane, "scaffolding one"))
	assert.Less(t,
		strings.Index(lane, "scaffolding one"),
		strings.Index(lane, `data-krill-lane="Implementation"`),
		"a Scaffold card renders before the Implementation column")
	assert.Less(t,
		strings.Index(lane, "testing one"),
		strings.Index(lane, `data-krill-lane="Validation"`),
		"a Testing card renders before the Validation column")
}

// TestBoardMilepebbleScopeNamesTheMilepebbleAndItsOwnProgress is the
// milepebble-scope half of the header rule: the one lane names the
// milepebble, names the milestone it hangs under, and reports the
// MILEPEBBLE's own progress rather than its parent's.
func TestBoardMilepebbleScopeNamesTheMilepebbleAndItsOwnProgress(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardMilepebbleRow(productTaskMilestone, "Middle milestone", productTaskMilepebble, "Middle milepebble", "cut work", store.LaneScaffold),
	}
	// The parent milestone is 9 of 10 done and the milepebble 1 of 2. A
	// header that reported the parent's figures would say "9 of 10" here.
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskMilestone, "Middle milestone", store.MilestoneStatusPlanned,
			store.TaskLaneCounts{Done: 9, Scaffold: 1}),
		{
			Milestone:  store.ProductTaskMilestoneRef{ID: productTaskMilestone, Name: "Middle milestone", Status: store.MilestoneStatusPlanned},
			Milepebble: &store.ProductTaskMilepebbleRef{ID: productTaskMilepebble, Name: "Middle milepebble", Status: store.MilestoneStatusInDesign},
			PerLane:    store.TaskLaneCounts{Scaffold: 1, Done: 1},
		},
	}
	mux, tasks := boardMux(t, rows, 1, containers)

	body := boardBody(t, mux, "scope=milepebble&container_id="+productTaskMilepebble.String())

	assert.Equal(t, []string{productTaskMilepebble.String()}, swimlaneIDs(body),
		"the milepebble scope is one lane keyed by the milepebble")
	lane := swimlaneOf(t, body, productTaskMilepebble.String())
	assert.Contains(t, lane, "Middle milepebble")
	assert.Contains(t, lane, "in design", "the milepebble's OWN status, not its parent's")
	assert.Contains(t, lane, `data-krill="swimlane-parent"`)
	assert.Contains(t, lane, "Middle milestone", "the header names the milestone it hangs under")
	assert.Contains(t, lane, "1 of 2 done", "the milepebble's own figures, not its parent's")
	require.Len(t, tasks.progressed, 1)
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: productTaskMilepebble},
		tasks.progressed[0].Scope, "the header's read is scoped to the milepebble itself")
}

// TestBoardEmptyScopeRendersTheEmptyState: no incomplete milestone has a
// task, so the board says so. Distinct from the read-failed state, which
// is a sentence about the failure rather than a board with nothing in it.
func TestBoardEmptyScopeRendersTheEmptyState(t *testing.T) {
	mux, tasks := boardMux(t, nil, 0, nil)

	body := boardBody(t, mux, "")

	assert.Contains(t, body, `data-krill="product-board-empty"`)
	assert.NotContains(t, body, `data-krill="swimlane"`)
	assert.NotContains(t, body, `data-krill="board-scroll"`)
	assert.Empty(t, tasks.progressed, "an empty board has nothing for the progress read to describe")
}

// TestBoardFailedProgressReadIsNotAnEmptyBoard: a board that cannot say
// how far along each milestone is must not render as a board with no
// milestones in it. "Nothing to show" and "we could not check" are
// different answers and only one of them is true.
func TestBoardFailedProgressReadIsNotAnEmptyBoard(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneScaffold),
	}
	mux, tasks := boardMux(t, rows, 1, nil)
	tasks.progressErr = errors.New("connection refused")

	body := boardBody(t, mux, "")

	assert.Contains(t, body, "could not be read")
	assert.NotContains(t, body, `data-krill="product-board-empty"`)
	assert.Len(t, tasks.progressed, 1, "the failed read was actually attempted")
}

// TestBoardShowsEveryTaskOrSaysHowManyOfHowMany: the board's own
// completeness statement. When it rendered everything there is nothing to
// say; when the read paged, the line names the shortfall and links to the
// Tasks view of the very same scope and filters.
func TestBoardShowsEveryTaskOrSaysHowManyOfHowMany(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "one", store.LaneScaffold),
	}
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 9}),
	}

	t.Run("a board that rendered everything says nothing about a shortfall", func(t *testing.T) {
		mux, _ := boardMux(t, rows, 1, containers)
		body := boardBody(t, mux, "")
		assert.NotContains(t, body, `data-krill="board-truncated"`)
	})

	t.Run("a paged board says Showing X of Y and links to the same filtered list", func(t *testing.T) {
		mux, _ := boardMux(t, rows, 40, containers)
		body := boardBody(t, mux, "lane=Scaffold&only_stuck=true")
		require.Contains(t, body, `data-krill="board-truncated"`)
		assert.Contains(t, body, "Showing 1 of 40")
		// The link carries the scope and filters through unchanged, so it
		// lands on exactly the rows this board was showing.
		assert.Contains(t, body, "href=\""+productHref(productTaskProduct, tasksSuffix)+
			"?lane=Scaffold&amp;only_stuck=true\"")
	})
}

// TestBoardIsReadOnlyAndCarriesTheClaimIdentity: FR f41a352d's read-only
// half and its claim-identity half. There is no draggable attribute, no
// lane-move control, and no form that writes; the card still carries the
// claim it observed.
func TestBoardIsReadOnlyAndCarriesTheClaimIdentity(t *testing.T) {
	claim := uuid.New()
	lease := time.Now().Add(20 * time.Minute).UTC().Truncate(time.Second)
	row := boardRow(productTaskNewestMilestone, "Newest milestone", "a claimed card", store.LaneImplementation)
	row.ClaimID = &claim
	row.LeaseExpiresAt = &lease
	mux, _ := boardMux(t, []store.ProductTaskRow{row}, 1, []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Implementation: 1}),
	})

	body := boardBody(t, mux, "")

	assert.Contains(t, body, `data-krill-claim-id="`+claim.String()+`"`)
	assert.Contains(t, body, `data-krill-lease-expires-at="`+lease.Format(time.RFC3339)+`"`)
	for _, forbidden := range []string{"draggable", "hx-post", "hx-put", "hx-patch", "hx-delete", "lane-move", "move-to-lane"} {
		assert.NotContains(t, body, forbidden, "the board offers nothing that moves a card")
	}
	// The board region itself carries exactly one form, and it is the
	// scope control's plain GET -- which is what makes "read-only" a
	// property of the markup rather than a claim about the handler.
	region := body[strings.Index(body, `<section id="`+pages.ProductTasksAnchor+`"`):strings.Index(body, "</main>")]
	assert.Equal(t, 1, strings.Count(region, "<form"), "only the scope control's GET form")
	assert.NotContains(t, region, `method="post"`)
}

// TestBoardScrollsInsideItselfRatherThanAcrossThePage is FR cf000440's
// narrow-viewport rule: the columns are a horizontal scroller inside the
// board, with fixed-minimum-width columns, rather than a five-column grid
// that squeezed or spilled.
func TestBoardScrollsInsideItselfRatherThanAcrossThePage(t *testing.T) {
	mux, _ := boardMux(t, []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneScaffold),
	}, 1, []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 1}),
	})

	body := boardBody(t, mux, "")

	assert.Contains(t, body, `data-krill="board-scroll"`)
	assert.Contains(t, body, "overflow-x-auto", "the board scrolls sideways, not the page")
	assert.NotContains(t, body, "grid-cols-5", "the fixed five-column grid is what spilled")
	assert.Contains(t, body, "min-w-full", "a lane is as wide as its columns and no narrower than the scroller")
	assert.Contains(t, body, "w-72 shrink-0", "each column keeps a fixed minimum width")
}

// TestBoardCarriesTheSingleFreshnessElement: NFR 7b497d92's element, on
// this page too. The absolute instant is in the markup -- so the page
// states a real time with scripting off -- and Refresh re-requests the
// region that carries it.
func TestBoardCarriesTheSingleFreshnessElement(t *testing.T) {
	mux, _ := boardMux(t, []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneScaffold),
	}, 1, []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 1}),
	})

	body := boardBody(t, mux, "")

	assert.Equal(t, 1, strings.Count(body, `data-krill="updated-at"`), "one freshness element per page")
	assert.Regexp(t, `data-krill-updated-at="\d{4}-\d{2}-\d{2}T`, body,
		"the absolute instant is in the markup, for the client script to relativise")
	assert.Contains(t, body, `data-krill="refresh"`)
	// It lives in the region's own chrome, which the Refresh target is, so
	// a refresh updates the element rather than replacing it.
	assert.Contains(t, body, `hx-target="#`+pages.ProductTasksAnchor+`"`)
}

// TestBoardAndTasksAreTwoViewsOfOneScope: the Board URL serves the board
// and the Tasks URL serves the table, and each carries the same resolved
// scope control -- one mode marked, the same selects, the same carried
// filters. What differs is the body, which is the whole of FR ab5f4936's
// half this milestone owns.
func TestBoardAndTasksAreTwoViewsOfOneScope(t *testing.T) {
	rows := []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneScaffold),
	}
	containers := []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 1}),
	}
	mux, tasks := boardMux(t, rows, 1, containers)
	query := "scope=milestone&container_id=" + productTaskNewestMilestone.String()

	board := boardBody(t, mux, query)
	tasksRec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/tasks?"+query)
	require.Equal(t, http.StatusOK, tasksRec.Code, "body: %s", tasksRec.Body.String())
	table := tasksRec.Body.String()

	assert.Contains(t, board, `data-krill="product-board"`)
	assert.NotContains(t, board, "The task rows for this scope are not rendered yet.")
	for _, shared := range []string{`data-krill="scope-control"`, `data-krill-mode="milestone"`, "Newest milestone"} {
		assert.Contains(t, board, shared)
		assert.Contains(t, table, shared, "both views render one scope control")
	}
	assert.Len(t, tasks.listed, 2, "one read per view")
	assert.Equal(t, tasks.listed[0].Scope, tasks.listed[1].Scope,
		"the two views read the same scope")
}

// TestBoardHtmxRequestSwapsTheRegionInPlace: the fragment the scope
// control's hx-get asks for keeps the region's anchor, or htmx's
// outerHTML swap deletes the target the next change looks for.
func TestBoardHtmxRequestSwapsTheRegionInPlace(t *testing.T) {
	mux, _ := boardMux(t, []store.ProductTaskRow{
		boardRow(productTaskNewestMilestone, "Newest milestone", "a card", store.LaneScaffold),
	}, 1, []store.ContainerTaskProgress{
		boardProgressRow(productTaskNewestMilestone, "Newest milestone", store.MilestoneStatusInProgress,
			store.TaskLaneCounts{Scaffold: 1}),
	})

	rec := htmxGet(mux, boardURL(""))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `id="`+pages.ProductTasksAnchor+`"`)
	assert.Contains(t, body, `data-krill="swimlane"`)
	// A fragment, not the whole shell: it is swapped into a page that
	// already has the chrome.
	assert.NotContains(t, body, "<nav")
}
