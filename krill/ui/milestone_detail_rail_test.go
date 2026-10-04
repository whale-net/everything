// Coverage for the Milestone detail page's properties rail (FR c208b777):
// the five label/value rows beside the content, the layout that puts them
// beside it at lg and below it under, and the status-history count the
// sibling status-history view has to agree with. No database.
//
// The two specReadClient methods these cases need are declared HERE rather
// than edited into the shared fakes' own files, so this section's addition to
// the seam stays one file nobody else is editing.
//
// The count is asserted as an exact figure and against every neighbouring
// one, not as "contains a number": a rail that printed len(history)-1, or
// that always printed the count from the wrong container, would satisfy a
// looser assertion while telling an operator the wrong thing about how far a
// milestone's status has moved.

package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// specReadClient additions the rail's reads need
// ---------------------------------------------------------------------------

// StatusHistory answers with no transitions and no error, so a fake that a
// case does not give history to still satisfies the seam. It is on the
// delivery tests' fakeSpecReader because that fake implements the whole
// interface method by method rather than embedding it.
func (f *fakeSpecReader) StatusHistory(context.Context, uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	if f.historyErr != nil {
		return nil, f.historyErr
	}
	return f.history[uuid.Nil], nil
}

// StatusHistory satisfies the seam for the per-product listing fake the
// Milestone detail's 404 cases use. It is deliberately empty: those cases
// never reach a rendered page, so a container's history is not what they are
// about.
func (perProductListing) StatusHistory(context.Context, uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	return nil, nil
}

// railSpecReader is a specReadClient whose two rail reads are both
// steerable: Delivery answers the fixture listing, and StatusHistory answers
// per container -- so a case can give one container a history and another
// none, which is the only way to prove the count is this container's count.
type railSpecReader struct {
	specReadClient
	listing    slice.DeliveryListing
	history    map[uuid.UUID][]store.MilestoneStatusEvent
	historyErr error
	askedFor   []uuid.UUID
}

func (r *railSpecReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return r.listing, nil
}

func (r *railSpecReader) StatusHistory(_ context.Context, id uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	r.askedFor = append(r.askedFor, id)
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	return r.history[id], nil
}

// DeliveryBreakdown answers empty, so the detail page's Delivery card --
// which every container's page reads, gated on nothing -- renders beside the
// rail without the fixture having to carry a delivered-scope document. The
// rail cases say nothing about that card; the ones that do have their own
// fixture and their own file.
func (r *railSpecReader) DeliveryBreakdown(context.Context, uuid.UUID) (slice.Document, slice.Document, error) {
	return slice.Document{}, slice.Document{}, nil
}

var _ specReadClient = (*railSpecReader)(nil)

// railTasks is the progress read the rail makes, steerable per container
// and able to fail outright.
//
// It records the scope it was asked for, because WHICH scope the rail asked
// with is half the claim under test: the Milestones table reads the whole
// roadmap under ProductTaskScopeAll, and a rail that copied that would show
// this milestone's page a figure that depends on its siblings.
type railTasks struct {
	store.TaskStore
	containers []store.ContainerTaskProgress
	err        error
	askedScope []store.ProductTaskScope
}

func (t *railTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	t.askedScope = append(t.askedScope, params.Scope)
	if t.err != nil {
		return store.ProductTaskProgress{}, t.err
	}
	return store.ProductTaskProgress{ProductID: params.ProductID, Containers: t.containers}, nil
}

func (t *railTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}

func (t *railTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

func (t *railTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

func (t *railTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

func (t *railTasks) ListProductTasks(context.Context, store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	return store.Page[store.ProductTaskRow]{}, nil
}

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// railContainerID is the milestone every rail case below renders. It is a
// member of railListing, so the handler's own container resolution finds it.
var railContainerID = uuid.MustParse("77777777-7777-7777-7777-777777777777")

// railPebbleID is a milepebble under railMilestoneID. A milepebble answers at
// the same URL with a different scope kind, and the rail's Tasks read is
// where that difference shows.
var railPebbleID = uuid.MustParse("88888888-8888-8888-8888-888888888888")

// railMilestoneID is the cut parent the milepebble hangs under. It exists so
// the milepebble cases have a parent the progress read could plausibly have
// answered with the WRONG figures.
var railMilestoneID = uuid.MustParse("99999999-9999-9999-9999-999999999999")

// railProductID is the product the rail's URLs hang off, spelled so a
// mismatch in any href names a wrong id in the failure.
var railProductID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

// railProduct is that product.
var railProduct = store.Product{ID: railProductID, Name: "krill"}

// railBudget is the FR budget the fixture milestone declares; the
// milepebble declares none, which is the FR's own distinction between the two
// kinds.
var railBudget = 12

// railListing is the delivery read's answer: one cut milestone carrying a
// FR budget, and the milepebble cut from it.
var railListing = slice.DeliveryListing{
	Milestones: []slice.MilestoneListingEntry{
		{
			ID:       railMilestoneID,
			Name:     "M7 Console reads",
			Status:   store.MilestoneStatusInDesign,
			FRBudget: &railBudget,
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: railPebbleID, Name: "P1 Workspace shell", Status: store.MilestoneStatusShipped},
			},
		},
		{
			ID:       railContainerID,
			Name:     "M6 UI facelift",
			Status:   store.MilestoneStatusInProgress,
			FRBudget: &railBudget,
		},
	},
}

// railContainer resolves railContainerID the way the handler does.
func railContainer(t *testing.T) taskContainer {
	t.Helper()
	c, found := resolveTaskContainer(railListing, railContainerID)
	require.True(t, found, "the fixture milestone must resolve")
	return c
}

// railPebble resolves railPebbleID the way the handler does.
func railPebble(t *testing.T) taskContainer {
	t.Helper()
	c, found := resolveTaskContainer(railListing, railPebbleID)
	require.True(t, found, "the fixture milepebble must resolve")
	return c
}

// milestoneProgressRow is one container's progress row, as the read returns
// it: Done is the Done lane's own count and Total the whole partition.
//
// The milestone ref is filled because the read carries one on every row --
// including a milepebble's, where it is the PARENT's id. That is the trap
// progressAccountsFor exists for, so the fixture reproduces it rather than
// avoiding it.
func milestoneProgressRow(milestoneID uuid.UUID, done, total int) store.ContainerTaskProgress {
	return store.ContainerTaskProgress{
		Milestone: store.ProductTaskMilestoneRef{
			ID:     milestoneID,
			Name:   "M7 Console reads",
			Status: store.MilestoneStatusInDesign,
		},
		PerLane: store.TaskLaneCounts{Done: done, Implementation: total - done},
	}
}

// milestonePebbleProgressRow is a milepebble's own row: Milepebble names the
// container, and Milestone carries the parent's id.
func milestonePebbleProgressRow(done, total int) store.ContainerTaskProgress {
	return store.ContainerTaskProgress{
		Milestone: store.ProductTaskMilestoneRef{
			ID:     railMilestoneID,
			Name:   "M7 Console reads",
			Status: store.MilestoneStatusInDesign,
		},
		Milepebble: &store.ProductTaskMilepebbleRef{
			ID:     railPebbleID,
			Name:   "P1 Workspace shell",
			Status: store.MilestoneStatusShipped,
		},
		PerLane: store.TaskLaneCounts{Done: done, Implementation: total - done},
	}
}

// railFixture wires an App against the reader and task fake above, so a case
// drives the REAL handler and the REAL registered route.
type railFixture struct {
	reader *railSpecReader
	tasks  *railTasks
	mux    *http.ServeMux
}

func newRailFixture(t *testing.T, history map[uuid.UUID][]store.MilestoneStatusEvent, containers []store.ContainerTaskProgress) *railFixture {
	t.Helper()
	reader := &railSpecReader{listing: railListing, history: history}
	tasks := &railTasks{containers: containers}
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: reader, products: []store.Product{railProduct}}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = tasks
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return &railFixture{reader: reader, tasks: tasks, mux: mux}
}

// get renders one container's detail through the registered route.
func (f *railFixture) get(t *testing.T, id uuid.UUID) string {
	t.Helper()
	rec := fetch(t, f.mux, "/products/"+railProductID.String()+"/milestones/"+id.String())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// railRegion slices the rail's own card out of a rendered page, so an
// assertion about one row is about that row and cannot be satisfied by a
// word that appears somewhere else on the page.
//
// This is the deliberately non-vacuous form of the check: it REQUIRES the
// marker and fails the test when it is absent, rather than returning "" and
// letting every later assertion pass against nothing.
func railRegion(t *testing.T, html string) string {
	t.Helper()
	region, ok := findRailRegion(html)
	require.True(t, ok,
		"the properties rail did not render, so every assertion against it would be vacuous")
	require.NotEmpty(t, region, "the rail rendered its marker but no rows at all:\n%s", html)
	return region
}

// findRailRegion is railRegion's fallible core, split out so the
// non-vacuity case can assert what the extractor does when the rail is
// ABSENT: ok=false. A single helper that both found and asserted could only
// ever report success, which is precisely the helper this milestone has
// already been bitten by -- one that probes for a marker, finds none, and
// hands every later assertion an empty string to pass against.
func findRailRegion(html string) (string, bool) {
	start := strings.Index(html, `data-krill="milestone-properties"`)
	if start < 0 {
		return "", false
	}
	end := strings.Index(html[start:], "</dl>")
	if end < 0 {
		return html[start:], true
	}
	return html[start : start+end], true
}

// historyEvents builds n transitions for one container, so the count a rail
// prints is checked against a number the test itself chose.
func historyEvents(n int) []store.MilestoneStatusEvent {
	events := make([]store.MilestoneStatusEvent, 0, n)
	for range n {
		events = append(events, store.MilestoneStatusEvent{
			ID:     uuid.New(),
			Status: store.MilestoneStatusInProgress,
		})
	}
	return events
}

// ---------------------------------------------------------------------------
// 1. the view model
// ---------------------------------------------------------------------------

// TestMilestoneRailCarriesTheFiveFieldsTheFRNames is FR c208b777's positive
// case on the view model: every one of the five rows has a value, and each
// is the read's own answer rather than something re-derived.
func TestMilestoneRailCarriesTheFiveFieldsTheFRNames(t *testing.T) {
	c := railContainer(t)
	rail := buildMilestoneDetailRail(railProductID,
		milestoneRailEntry(railListing, c.ID), c,
		milestoneRailReads{
			Progress: milestoneProgressRow(c.ID, 6, 9),
			History:  historyEvents(4),
		})

	assert.Equal(t, string(store.MilestoneStatusInProgress), rail.Status)
	assert.Equal(t, "12", rail.FRBudget)
	assert.Equal(t, 6, rail.Tasks.Done)
	assert.Equal(t, 9, rail.Tasks.Total)
	assert.Equal(t, 4, rail.StatusChanges)
	assert.Equal(t, c.ID.String(), rail.ID)
	assert.Equal(t,
		"/products/"+railProductID.String()+"/milestones/"+c.ID.String()+"/status-history",
		rail.StatusHistoryPath, "the Status history link targets the sibling view's own URL")
	assert.Empty(t, rail.HistoryError)
}

// TestMilestoneRailLabelsAreTheFRsOwnWords: the two sentences the FR spells
// out -- "N of M done" and "No tasks yet" -- rather than the Milestones
// table's bar caption, which is a different surface's wording.
func TestMilestoneRailLabelsAreTheFRsOwnWords(t *testing.T) {
	with := pages.MilestoneRail{Tasks: pages.ProgressCell{Done: 6, Total: 9}}
	assert.Equal(t, "6 of 9 done", with.TasksLabel())

	empty := pages.MilestoneRail{Tasks: pages.ProgressCell{}}
	assert.Equal(t, "No tasks yet", empty.TasksLabel(),
		"a container holding nothing says so, rather than reading as a stalled one")

	assert.Equal(t, "4 changes", (pages.MilestoneRail{StatusChanges: 4}).HistoryLabel())
	assert.Equal(t, "1 change", (pages.MilestoneRail{StatusChanges: 1}).HistoryLabel())
	assert.Equal(t, "0 changes", (pages.MilestoneRail{}).HistoryLabel())
}

// TestMilestoneRailBudgetIsAbsentForAMilepebble: only a milestone declares
// an FR budget. A milepebble's row must say so explicitly rather than
// rendering an empty cell, which would read as a value the page failed to
// read rather than as a budget that does not exist.
func TestMilestoneRailBudgetIsAbsentForAMilepebble(t *testing.T) {
	c := railPebble(t)
	rail := buildMilestoneDetailRail(railProductID,
		milestoneRailEntry(railListing, c.ID), c, milestoneRailReads{})

	assert.Empty(t, rail.FRBudget,
		"a milepebble inherits no budget from its parent -- the parent's is its own row")
	assert.Equal(t, c.ID.String(), rail.ID)
	assert.Equal(t, string(store.MilestoneStatusShipped), rail.Status,
		"the rail badges the CONTAINER's status, not its parent's")
}

// TestMilestoneRailNeverPrintsZeroChangesOverAFailedRead: an absent count
// and a count of zero are different facts. A failed history read that
// rendered "0 changes" would tell an operator a milestone's status was never
// touched, which is a claim about the container the page never learned.
func TestMilestoneRailNeverPrintsZeroChangesOverAFailedRead(t *testing.T) {
	c := railContainer(t)
	rail := buildMilestoneDetailRail(railProductID,
		milestoneRailEntry(railListing, c.ID), c,
		milestoneRailReads{HistoryErr: assert.AnError})

	assert.NotEmpty(t, rail.HistoryError)
	assert.Equal(t, rail.HistoryError, rail.HistoryLabel())
	assert.NotContains(t, rail.HistoryLabel(), "0 changes")
	assert.NotContains(t, rail.HistoryLabel(), "change",
		"the failure sentence must not read as a count at all")
}

// TestMilestoneRailSurvivesAFailedProgressRead: the Tasks row loses its
// figures and says so; the Status history row and the id are unaffected,
// because they come from different reads.
func TestMilestoneRailSurvivesAFailedProgressRead(t *testing.T) {
	c := railContainer(t)
	rail := buildMilestoneDetailRail(railProductID,
		milestoneRailEntry(railListing, c.ID), c,
		milestoneRailReads{
			ProgressErr: assert.AnError,
			History:     historyEvents(2),
		})

	assert.True(t, rail.TasksErrored())
	assert.NotEmpty(t, rail.Tasks.ProgressError)
	assert.Equal(t, 2, rail.StatusChanges,
		"a progress failure must not cost the status-history row its count")
	assert.Equal(t, "12", rail.FRBudget)
	assert.Equal(t, c.ID.String(), rail.ID)
}

// TestMilestoneRailReportsAReadThatSkippedThisContainer: a progress read
// that answered without a row for this container is not the same as one that
// answered with a zero row. The first says "we did not check"; the second
// says "it holds nothing", and rendering them the same is how an operator
// comes to trust a figure that was never read.
func TestMilestoneRailReportsAReadThatSkippedThisContainer(t *testing.T) {
	c := railContainer(t)

	// A row for a DIFFERENT container: present in the answer, not about
	// this one.
	other := milestonePebbleProgressRow(1, 3)
	reads := readRailProgressFor(t, c, []store.ContainerTaskProgress{other})

	assert.True(t, reads.ProgressErrored,
		"a progress answer holding only another container's row does not account for this one")

	rail := buildMilestoneDetailRail(railProductID,
		milestoneRailEntry(railListing, c.ID), c, reads.milestoneRailReads)
	assert.True(t, rail.TasksErrored())
}

// TestMilestoneRailReadsProgressUnderTheSingleContainerScope: the rail asks
// with ProductTaskScopeMilestone for a milestone and ProductTaskScopeMilepebble
// for a milepebble -- never the all-containers scope the Milestones table
// uses, which would make one container's figure depend on its siblings.
func TestMilestoneRailReadsProgressUnderTheSingleContainerScope(t *testing.T) {
	for _, tc := range []struct {
		name      string
		container func(*testing.T) taskContainer
		want      store.ProductTaskScopeKind
	}{
		{"milestone", railContainer, store.ProductTaskScopeMilestone},
		{"milepebble", railPebble, store.ProductTaskScopeMilepebble},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.container(t)
			reads := readRailProgressFor(t, c, []store.ContainerTaskProgress{
				milestoneProgressRow(c.ID, 1, 2),
			})

			require.Len(t, reads.scopes, 1, "one progress read, one scope")
			assert.Equal(t, tc.want, reads.scopes[0].Kind)
			assert.Equal(t, c.ID, reads.scopes[0].ContainerID,
				"the scope names THIS container, not the product or the parent")
		})
	}
}

// railProgressOutcome is readRailProgressFor's answer: the rail's reads plus
// the scope they were asked with.
type railProgressOutcome struct {
	milestoneRailReads
	scopes []store.ProductTaskScope
}

// readRailProgressFor drives the rail's OWN progress read against a fixed
// answer, and reports both what it produced and which scope it asked with.
//
// It goes through App rather than through the builder so the scope choice is
// observed where it is made -- a test that only checked the rendered figures
// could not tell a rail that asked the right question from one that got
// lucky with a fake.
func readRailProgressFor(t *testing.T, c taskContainer, containers []store.ContainerTaskProgress) railProgressOutcome {
	t.Helper()
	app := &App{scopes: productScopeScopes{scope: store.Scope{ID: chromeScopeID}}, tasks: &railTasks{containers: containers}}
	reads := app.readMilestoneRailProgress(context.Background(), railProductID, c)
	return railProgressOutcome{reads, app.tasks.(*railTasks).askedScope}
}

// ---------------------------------------------------------------------------
// 2. rendered markup
// ---------------------------------------------------------------------------

// TestMilestoneDetailRendersTheRailTheFRNames: all five rows render, each
// with its own marker, and the values are the ones the view model carries.
func TestMilestoneDetailRendersTheRailTheFRNames(t *testing.T) {
	f := newRailFixture(t,
		map[uuid.UUID][]store.MilestoneStatusEvent{railContainerID: historyEvents(4)},
		[]store.ContainerTaskProgress{milestoneProgressRow(railContainerID, 6, 9)})

	rail := railRegion(t, f.get(t, railContainerID))

	for _, marker := range []string{
		`data-krill="milestone-properties"`,
		`data-krill="milestone-properties-status"`,
		`data-krill="milestone-properties-fr-budget"`,
		`data-krill="milestone-properties-tasks"`,
		`data-krill="milestone-properties-status-history"`,
		`data-krill="milestone-properties-id"`,
	} {
		assert.Contains(t, rail, marker, "the rail must carry its own marker %s", marker)
	}

	// Status, through the SHARED badge -- so this status is this colour
	// here and on the header and the Milestones table alike.
	statusCell := regionBetween(t, rail, `data-krill="milestone-properties-status"`, "</dd>")
	assert.Contains(t, statusCell, "badge")
	assert.Contains(t, statusCell, "in progress")

	// The other three values.
	assert.Contains(t, rail, ">12<")
	assert.Contains(t, rail, "6 of 9 done")
	assert.Contains(t, rail, ">4 changes<")
	assert.Contains(t, rail, railContainerID.String())
}

// TestMilestoneDetailRailStatusBadgeMatchesTheHeaderExactly: the header
// already shows this status, and the rail repeats it -- through the same
// statusBadge. A second badge definition would compile and pass every other
// test here while silently downgrading the colour on one of the two, so the
// two cells are compared rather than each checked for "contains badge".
func TestMilestoneDetailRailStatusBadgeMatchesTheHeaderExactly(t *testing.T) {
	f := newRailFixture(t, nil, []store.ContainerTaskProgress{
		milestoneProgressRow(railContainerID, 0, 0),
	})
	html := f.get(t, railContainerID)

	header := regionBetween(t, html, `data-krill="milestone-detail-header"`, "</div>")
	railStatus := regionBetween(t, railRegion(t, html), `data-krill="milestone-properties-status"`, "</dd>")

	// The badge's own rendered classes, which is what the operator sees.
	badgeClasses := func(region string) string {
		from := strings.Index(region, `class="badge`)
		require.GreaterOrEqual(t, from, 0, "no badge in %s", region)
		rest := region[from+len(`class="`):]
		to := strings.Index(rest, `"`)
		return rest[:to]
	}

	// The classes components.MilestoneStatusStyle says this status gets --
	// assembled here the way htmxui's own badgeClasses does, so the
	// expectation is the shared mapper's answer rather than whatever the
	// page happened to render.
	style := components.MilestoneStatusStyle(string(store.MilestoneStatusInProgress))
	want := "badge"
	if style.Soft {
		want += " badge-soft"
	}
	want += " " + string(style.Variant) + " " + string(style.Size)

	assert.Equal(t, want, badgeClasses(header),
		"the header's badge must be the shared mapper's, not a local one")
	assert.Equal(t, want, badgeClasses(railStatus),
		"the rail's badge must be the shared mapper's, not a local one")
	assert.Equal(t, badgeClasses(header), badgeClasses(railStatus),
		"and the rail's status badge and the header's must resolve to the same variant")
}

// TestMilestoneDetailRailIdUsesTheCopyChipTheHeadScriptBinds: the chip
// carries the task detail's own data attributes, so the copyTaskIdScript
// already in every page's head enables it. A chip that named its own
// attributes would render as a live control that silently does nothing --
// which is exactly what the script's disabled-by-default chip exists to
// prevent -- so the markers are asserted, not just the presence of a button.
func TestMilestoneDetailRailIdUsesTheCopyChipTheHeadScriptBinds(t *testing.T) {
	f := newRailFixture(t, nil, []store.ContainerTaskProgress{
		milestoneProgressRow(railContainerID, 0, 0),
	})
	html := f.get(t, railContainerID)

	// The script binds this selector and reads this attribute; the
	// confirmation lands in this sibling span.
	assert.Contains(t, html, `querySelectorAll('[data-krill="copy-task-id"]')`,
		"the head script that gives the chip its behaviour must still be on the page")
	assert.Contains(t, html, `data-krill="copy-task-id"`)

	rail := railRegion(t, html)
	assert.Contains(t, rail, `data-task-id="`+railContainerID.String()+`"`,
		"the chip carries the container's id for the script to copy")
	assert.Contains(t, rail, `data-krill="copy-task-id-status"`,
		"the chip needs the script's own live region beside it to announce into")
	assert.Contains(t, rail, "disabled",
		"the chip ships disabled, so a control that cannot work never looks live")

	// And no inline clipboard call anywhere on the page: the behaviour is
	// the head script's, so a second implementation cannot appear here.
	assert.NotContains(t, rail, "navigator.clipboard")
	assert.NotContains(t, rail, "onclick=")
}

// TestMilestoneDetailRailSitsBesideTheContentAtLgAndStacksUnderIt is the
// layout half of the FR. The grid frame was declared by the header task; the
// rail's claim is that it sits in that frame's SECOND cell, in the <aside> --
// so the lg: two-column rule and the rail's placement are checked together,
// because a rail rendered outside the frame would satisfy neither.
func TestMilestoneDetailRailSitsBesideTheContentAtLgAndStacksUnderIt(t *testing.T) {
	f := newRailFixture(t, nil, []store.ContainerTaskProgress{
		milestoneProgressRow(railContainerID, 0, 0),
	})
	html := f.get(t, railContainerID)

	// The frame is sliced from its own opening <div> to the region's
	// </section>. Both ends matter: templ writes class BEFORE data-krill, so
	// starting at the marker alone would drop the very grid classes under
	// test; and the main cell is a currently-empty <div>, so a "</div>"
	// end would stop before either cell's contents.
	markerAt := strings.Index(html, `data-krill="milestone-detail-frame"`)
	require.GreaterOrEqual(t, markerAt, 0, "the page must declare the detail frame")
	openAt := strings.LastIndex(html[:markerAt], "<div")
	require.GreaterOrEqual(t, openAt, 0, "the frame's marker must sit inside its own <div>")
	sectionEnd := strings.Index(html[markerAt:], "</section>")
	require.Greater(t, sectionEnd, 0, "the detail region must close")
	frame := html[openAt : markerAt+sectionEnd]

	// lg: and only lg: two columns, main column then the fixed-width rail.
	// Without the lg: prefix the grid is one column at EVERY width, which
	// would stack the rail on a desktop -- the opposite of what the FR asks.
	assert.Contains(t, frame, "grid")
	assert.Contains(t, frame, "lg:grid-cols-[minmax(0,1fr)_18rem]")
	assert.NotContains(t, strings.Split(frame, "lg:grid-cols")[0], "grid-cols-[",
		"the two-column split must be lg-gated, so narrow viewports stack")

	// The rail is in the frame's second cell: after the main column, inside
	// the frame.
	mainAt := strings.Index(frame, `data-krill="milestone-detail-main"`)
	railAt := strings.Index(frame, `data-krill="milestone-detail-rail"`)
	require.GreaterOrEqual(t, mainAt, 0, "the frame must still have its main cell:\n%s", frame)
	require.GreaterOrEqual(t, railAt, 0, "the rail must be inside the frame:\n%s", frame)
	assert.Less(t, mainAt, railAt, "the rail is the SECOND cell -- the wide one is the content")

	// And it is an <aside>, the landmark that says this column is the
	// page's properties rather than more of its content.
	aside := regionBetween(t, frame, "<aside data-krill=\"milestone-detail-rail\"", "</aside>")
	assert.Contains(t, aside, `data-krill="milestone-properties"`,
		"the declared rail cell must actually hold the rail")
}

// TestMilestoneDetailRailCountIsThisContainersCount is the FR 9a6e7924
// handoff: the label the operator follows has to equal the number of
// transitions that view will list.
//
// The three containers below carry different histories, and each case asserts
// its own exact figure -- against the reader's own list length, not against a
// literal -- plus that NO other container's count appears. A rail that read
// the parent's history, or read under the all-containers scope, or printed a
// constant, would fail here.
func TestMilestoneDetailRailCountIsThisContainersCount(t *testing.T) {
	history := map[uuid.UUID][]store.MilestoneStatusEvent{
		railContainerID: historyEvents(4),
		railPebbleID:    historyEvents(1),
		// The parent deliberately holds a DIFFERENT count (7) from the
		// milepebble beneath it (1), so a rail that read the parent's
		// register for a milepebble prints 7 instead of 1.
		railMilestoneID: historyEvents(7),
	}

	for _, id := range []uuid.UUID{railContainerID, railPebbleID, railMilestoneID} {
		t.Run(id.String(), func(t *testing.T) {
			f := newRailFixture(t, history, []store.ContainerTaskProgress{
				milestoneProgressRow(id, 1, 2),
				milestonePebbleProgressRow(1, 2),
			})
			html := f.get(t, id)

			want := len(history[id])
			rail := railRegion(t, html)

			assert.Contains(t, rail, `data-krill-change-count="`+strconv.Itoa(want)+`"`,
				"the link must carry its own container's count, %d", want)
			if want == 1 {
				assert.Contains(t, rail, ">1 change<")
			} else {
				assert.Contains(t, rail, ">"+strconv.Itoa(want)+" changes<")
			}

			// No OTHER container's count. This is what catches a read that
			// answered for the wrong container while still printing some
			// plausible number.
			for other, otherEvents := range history {
				if other == id || len(otherEvents) == want {
					continue
				}
				assert.NotContains(t, rail, ">"+strconv.Itoa(len(otherEvents))+" changes<",
					"the rail printed another container's count (%d)", len(otherEvents))
			}
		})
	}
}

// TestMilestoneDetailRailReadsTheHistoryOfTheContainerItIsRendering: the
// read is issued for THIS container's id -- the id the URL named and the
// header badges, not the parent or a default.
func TestMilestoneDetailRailReadsTheHistoryOfTheContainerItIsRendering(t *testing.T) {
	f := newRailFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		railPebbleID: historyEvents(3),
	}, []store.ContainerTaskProgress{milestonePebbleProgressRow(3, 4)})

	f.get(t, railPebbleID)

	require.NotEmpty(t, f.reader.askedFor, "the rail must actually read a history")
	for _, asked := range f.reader.askedFor {
		assert.Equal(t, railPebbleID, asked,
			"the history read must name the container being rendered")
	}
}

// TestMilestoneDetailRailSurvivesBothReadsFailing: neither rail read is the
// page, so both failing still renders the container's name, status and work
// links, and both rows say what could not be read rather than showing a
// figure or a zero.
func TestMilestoneDetailRailSurvivesBothReadsFailing(t *testing.T) {
	reader := &railSpecReader{listing: railListing, historyErr: assert.AnError}
	tasks := &railTasks{err: assert.AnError}
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: reader, products: []store.Product{railProduct}}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = tasks
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	rec := fetch(t, mux, "/products/"+railProductID.String()+"/milestones/"+railContainerID.String())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	html := rec.Body.String()

	// The page itself is intact.
	assert.Contains(t, html, "M6 UI facelift")
	assert.Contains(t, html, ">Open tasks<")
	assert.Contains(t, html, ">Board<")

	rail := railRegion(t, html)
	assert.Contains(t, rail, milestonesProgressError,
		"a failed progress read says so rather than showing a count of zero")
	assert.NotContains(t, rail, " of 0 done")
	assert.NotContains(t, rail, "0 changes",
		"a failed history read is not a milestone whose status was never touched")
	assert.Contains(t, rail, "could not be read")

	// The id is from the listing, not either failing read, so it survives.
	assert.Contains(t, rail, railContainerID.String())
}

// TestMilestoneDetailRailRendersForAMilepebble: a milepebble answers at the
// same URL, so the rail renders for one too -- with the milepebble's own
// budget (none), status and id, never its parent's.
func TestMilestoneDetailRailRendersForAMilepebble(t *testing.T) {
	f := newRailFixture(t,
		map[uuid.UUID][]store.MilestoneStatusEvent{railPebbleID: historyEvents(1)},
		[]store.ContainerTaskProgress{milestonePebbleProgressRow(3, 4)})

	rail := railRegion(t, f.get(t, railPebbleID))

	assert.Contains(t, rail, "No FR budget declared",
		"a milepebble declares no budget of its own")
	assert.NotContains(t, rail, ">12<",
		"its parent's budget must not appear as its own")
	assert.Contains(t, rail, ">shipped<", "its OWN status")
	assert.Contains(t, rail, "3 of 4 done")
	assert.Contains(t, rail, ">1 change<")
	assert.Contains(t, rail, railPebbleID.String())
	assert.NotContains(t, rail, `data-task-id="`+railMilestoneID.String()+`"`)
}

// TestMilestoneDetailRailIsNotVacuousWhenTheRailIsRemoved is the guard
// against this milestone's own failure mode: a helper that probes a list of
// data-krill markers, finds none of them, returns "" and lets every assertion
// against it pass.
//
// It renders the SAME page the other cases do and asserts the region
// extractor FAILS on a page whose rail is gone -- by checking the extractor's
// precondition directly. railRegion requires the marker; this case proves
// the marker is a real, present thing on the served page rather than
// something the tests assume.
func TestMilestoneDetailRailIsNotVacuousWhenTheRailIsRemoved(t *testing.T) {
	f := newRailFixture(t, nil, []store.ContainerTaskProgress{
		milestoneProgressRow(railContainerID, 6, 9),
	})
	html := f.get(t, railContainerID)

	// The served page really does carry the region, so a case built on it is
	// asserting against real markup...
	assert.Contains(t, html, `data-krill="milestone-detail-rail"`)
	rail := railRegion(t, html)
	assert.NotEmpty(t, rail)
	assert.Contains(t, rail, "6 of 9 done")

	// ...and the extractor refuses on a page WITHOUT the rail, rather than
	// returning "". This is what makes every other case's failure real: it
	// is checked here because a helper that returned "" would let all of
	// them pass while the rail rendered nothing at all.
	withoutRail := strings.Replace(html, rail, "", 1)
	require.NotContains(t, withoutRail, `data-krill="milestone-properties"`,
		"the removal must actually take the marker out for this to mean anything")
	region, ok := findRailRegion(withoutRail)
	assert.False(t, ok,
		"a rail that did not render must be reported absent, not returned as an empty region")
	assert.Empty(t, region)
}

// TestMilestoneDetailRailRegionIsInsideTheProductScopedRegion keeps
// productPageRegion honest for every other test that uses it: the rail
// renders INSIDE data-krill=\"milestone-detail\", so the shared helper --
// which slices the routed page's own region -- still sees the whole page
// rather than stopping short of it.
func TestMilestoneDetailRailRegionIsInsideTheProductScopedRegion(t *testing.T) {
	f := newRailFixture(t,
		map[uuid.UUID][]store.MilestoneStatusEvent{railContainerID: historyEvents(4)},
		[]store.ContainerTaskProgress{milestoneProgressRow(railContainerID, 6, 9)})
	html := f.get(t, railContainerID)

	page := productPageRegion(html)
	require.NotEmpty(t, page,
		"productPageRegion must find the milestone-detail region, or every "+
			"assertion made against it in other files passes vacuously")
	assert.Contains(t, page, `data-krill="milestone-properties"`,
		"and it must encompass the rail, not stop before it")
}
