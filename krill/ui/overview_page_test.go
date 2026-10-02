// Wire-driven coverage for the Overview (overview_page.go) -- the shell's
// home, and the page the cutover made every other page render beside.
//
// The tables below are the FR's, spelled as literals: the eight statuses
// and the classification each must get, the header's three shapes (N>0,
// N=0, nothing in flight), the three URL shapes the chrome reads its
// targets out of. Nothing here derives an expectation from the code under
// test -- the statuses are written as their own wire strings and checked
// for exhaustiveness against the store's exported valid set, the counts
// and paths are written out, and the milestone names are fixture names no
// production table produces. A table that read its own rows could shrink
// to match a shrinking implementation.
package main

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

// attentionRows splits the panel's rendered list into one string per <li>,
// in the order they appear. Asserting against a whole row rather than the
// list as a whole is what makes "this row's badge is this row's reason"
// checkable: a scan of the whole list cannot tell a row carrying its own
// reason from five rows each carrying the first one's.
func attentionRows(body string) []string {
	list := overviewRegion(body, "needs-attention-list")
	if list == "" {
		return nil
	}
	list, end := list, strings.Index(list, "</ul>")
	if end < 0 {
		return nil
	}
	list = list[:end]

	var rows []string
	for rest := list; ; {
		i := strings.Index(rest, "<li")
		if i < 0 {
			return rows
		}
		rest = rest[i:]
		j := strings.Index(rest, "</li>")
		if j < 0 {
			return rows
		}
		rows = append(rows, rest[:j])
		rest = rest[j:]
	}
}

// overviewProduct is the one product the Overview fixtures serve. It is a
// literal rather than uuid.New() so the expected paths in the tables below
// can be written out and read, and so a failure names the same ids every
// run.
var overviewProduct = uuid.MustParse("44444444-4444-4444-4444-444444444444")

// overviewMilestone is the container the milestone-scoped URL shapes in
// TestShellPathTargets name.
var overviewMilestone = uuid.MustParse("55555555-5555-5555-5555-555555555555")

// overviewEscaped is a counter the chrome and the header read: a positive
// figure renders the primary action, zero renders a sentence instead. It
// also stands in for the task-progress read the in-flight panel makes, so
// the two reads a page's Overview depends on come from one fixture.
type overviewCounter struct {
	store.TaskStore
	count     int
	readable  bool
	callCount int

	progress    store.ProductTaskProgress
	progressErr error

	// escalated is what the Needs-attention panel's own read returns,
	// and escalations records the params it was called with so a test can
	// assert the narrowing and the page size the panel asks for.
	escalated    []store.EscalatedTaskRow
	escalatedErr error
	escalations  []store.ListEscalatedTasksParams
}

func (c *overviewCounter) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	c.callCount++
	if !c.readable {
		return 0, store.ErrNotFound
	}
	return c.count, nil
}

// SummarizeProductTaskProgress answers with the fixture's containers only
// for the product the fixture names, so a caller that read the wrong
// product gets an empty answer rather than this product's rows. A fixture
// that answered the same thing whatever it was asked for would let the
// panel's rows pass against a read of the wrong product.
func (c *overviewCounter) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	if c.progressErr != nil {
		return store.ProductTaskProgress{}, c.progressErr
	}
	if c.progress.ProductID != params.ProductID {
		return store.ProductTaskProgress{ProductID: params.ProductID}, nil
	}
	return store.ProductTaskProgress{
		ProductID:  params.ProductID,
		Containers: c.progress.Containers,
	}, nil
}

func (*overviewCounter) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}

// CountConsoleOverview is the stat tiles' read. These tests are about the
// header, so the tiles' figures are a constant zero here -- which renders
// four tiles reading "0" rather than leaving the strip out, and keeps the
// header assertions about the header.
func (*overviewCounter) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}

func (c *overviewCounter) ListEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	c.escalations = append(c.escalations, p)
	if c.escalatedErr != nil {
		return store.Page[store.EscalatedTaskRow]{}, c.escalatedErr
	}
	// The store applies ResolvePageSize and returns only the first page;
	// the fixture truncates the same way so a panel reading a page it did
	// not ask for fails here rather than in production.
	page := store.Page[store.EscalatedTaskRow]{Items: c.escalated}
	if size := store.ResolvePageSize(p.Page.PageSize); len(page.Items) > size {
		page.Items = page.Items[:size]
		page.NextToken = "more"
	}
	return page, nil
}

func (*overviewCounter) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}

func (*overviewCounter) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}

func (*overviewCounter) ListTasksByMilestone(context.Context, uuid.UUID) ([]store.TaskSummary, error) {
	return nil, nil
}

// The product-wide task read, which shellPagePaths' walk now reaches: the
// Tasks and Board nav hrefs lead there. Both answer empty, so the walk
// measures the chrome those pages render rather than the store behind
// them.
func (*overviewCounter) ListProductTasks(context.Context, store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	return store.Page[store.ProductTaskRow]{}, nil
}

func (*overviewCounter) CountProductTasks(context.Context, store.ListProductTasksParams) (int, error) {
	return 0, nil
}

// The task-detail reads, which shellPagePaths' walk reaches through the
// per-container detail URL: that URL now 302s into the product-scoped
// detail (FR 0c03eac1), and the product-scoped handler reads the task
// before it checks the container, so a fixture with no GetTaskByID
// nil-panics on a page the walk has always visited. The task answers under
// a milestone the shared listing carries, so the detail renders rather than
// answering its in-shell 404 -- the walk measures chrome, not data.
func (*overviewCounter) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	return store.Task{
		ID:          id,
		Title:       "Overview walk task",
		MilestoneID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
		CurrentLane: store.LaneImplementation,
	}, nil
}

func (*overviewCounter) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (*overviewCounter) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

func (*overviewCounter) GetClaimByID(context.Context, uuid.UUID) (store.Claim, error) {
	return store.Claim{}, store.ErrNotFound
}

// LatestClaimForTask is the rail's "None. Last held by <session>" read. The
// walk's fixture task holds no claim, so the not-found signal is the answer
// and the walk measures chrome, not data.
func (*overviewCounter) LatestClaimForTask(context.Context, uuid.UUID, uuid.UUID) (store.Claim, bool, error) {
	return store.Claim{}, false, nil
}

func (*overviewCounter) GetEscalationEventByID(context.Context, uuid.UUID) (store.EscalationEvent, error) {
	return store.EscalationEvent{}, store.ErrNotFound
}

// overviewMux mounts the shell's own routes against an app whose two
// Overview reads are fixtures: the escalated counter and the delivery
// listing. Mounting the real registrations (rather than just the Overview
// handler) is what lets the same fixtures drive both the Overview's own
// URL and the un-prefixed home.
func overviewMux(t *testing.T, listing slice.DeliveryListing, listingErr error, counter *overviewCounter) *http.ServeMux {
	t.Helper()

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products:   []store.Product{{ID: overviewProduct, Name: "krill", Vision: "the spec substrate"}},
		listing:    listing,
		listingErr: listingErr,
	}
	app.tasks = counter
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// overviewListing is the delivery listing the header cases share: two
// containers in flight (one in design, one in progress) among containers
// that are not, so a rendering that lists everything fails here.
func overviewListing() slice.DeliveryListing {
	return slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
				Name:   "Build the thing",
				Status: store.MilestoneStatusInProgress,
			},
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"),
				Name:   "Design the thing",
				Status: store.MilestoneStatusInDesign,
			},
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003"),
				Name:   "Ship the thing",
				Status: store.MilestoneStatusShipped,
			},
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000004"),
				Name:   "Abandon the thing",
				Status: store.MilestoneStatusAbandoned,
			},
		},
	}
}

// nothingInFlightListing is the one the header calls empty: three
// containers, none of which is in design or in progress.
func nothingInFlightListing() slice.DeliveryListing {
	return slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000005"),
				Name:   "Planned thing",
				Status: store.MilestoneStatusPlanned,
			},
			{
				ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000006"),
				Name:   "Designed thing",
				Status: store.MilestoneStatusDesigned,
				Milepebbles: []slice.MilepebbleListingEntry{
					{
						ID:     uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001"),
						Name:   "Stalled milepebble",
						Status: store.MilestoneStatusPartiallyComplete,
					},
				},
			},
		},
	}
}

// readableCount is the ordinary counter: a count the read returned.
func readableCount(n int) *overviewCounter {
	return &overviewCounter{count: n, readable: true}
}

// withProgress returns the counter with a task-progress read behind it,
// the shape the in-flight panel consumes.
func withProgress(containers ...store.ContainerTaskProgress) *overviewCounter {
	c := readableCount(1)
	c.progress = store.ProductTaskProgress{ProductID: overviewProduct, Containers: containers}
	return c
}

// milestoneProgress is one milestone row: the container's id and name, its
// own status, and its per-lane counts. A milepebble row is the same shape
// with Milepebble set; neither carries an id the test reads back, because
// the ids are literals the href assertions spell out.
func milestoneProgress(id uuid.UUID, name string, status store.MilestoneStatus, lanes store.TaskLaneCounts) store.ContainerTaskProgress {
	return store.ContainerTaskProgress{
		Milestone: store.ProductTaskMilestoneRef{ID: id, Name: name, Status: status},
		PerLane:   lanes,
	}
}

// milepebbleProgress is the same row for a container that is a cut
// milepebble rather than a milestone. The Milestone ref is the parent,
// which is what the read returns; the classification must read the
// milepebble's own status beside it.
func milepebbleProgress(parentID uuid.UUID, parentName string, parentStatus store.MilestoneStatus, id uuid.UUID, name string, status store.MilestoneStatus, lanes store.TaskLaneCounts) store.ContainerTaskProgress {
	return store.ContainerTaskProgress{
		Milestone:  store.ProductTaskMilestoneRef{ID: parentID, Name: parentName, Status: parentStatus},
		Milepebble: &store.ProductTaskMilepebbleRef{ID: id, Name: name, Status: status},
		PerLane:    lanes,
	}
}

// overviewRegion slices the element carrying hook out of a page, so an
// assertion cannot be satisfied by the same text appearing somewhere else
// on the page.
func overviewRegion(body, hook string) string {
	marker := `data-krill="` + hook + `"`
	start := strings.Index(body, marker)
	if start < 0 {
		return ""
	}
	return body[start:]
}

// collapsed runs a region's HTML down to its text, collapsing the runs of
// whitespace templ's source formatting leaves between elements. templ
// keeps newlines and indentation inside an element's text, so a literal
// sentence like "Review 3 escalated tasks" is not a substring of the raw
// markup; it is a substring of this.
func collapsed(region string) string {
	return strings.Join(strings.Fields(region), " ")
}

// elementTag returns the whole opening tag of the element carrying hook,
// attributes and all. overviewRegion deliberately starts at the hook, which
// is after href -- so an assertion about the href needs the tag, not the
// region.
func elementTag(body, hook string) string {
	i := strings.Index(body, `data-krill="`+hook+`"`)
	if i < 0 {
		return ""
	}
	start := strings.LastIndex(body[:i], "<")
	if start < 0 {
		return ""
	}
	end := strings.Index(body[i:], ">")
	if end < 0 {
		return ""
	}
	return body[start : i+end+1]
}

// ---------------------------------------------------------------------------
// the eight-status classification
// ---------------------------------------------------------------------------

// TestMilestoneInFlightClassifiesEveryStatus is the rule the whole header
// stands on, one row per status, with the verdict written out rather than
// computed. In flight is exactly "in design" and "in progress": the
// designed and planned rungs are up next and partially complete is work
// that stopped, and reading any of the three as in flight is the specific
// wrong answer the FR forbids.
func TestMilestoneInFlightClassifiesEveryStatus(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
		why    string
	}{
		{status: "not started", want: false, why: "no work has begun"},
		{status: "in design", want: true, why: "spec is being written"},
		{status: "designed", want: false, why: "up next, not yet started"},
		{status: "planned", want: false, why: "up next, committed to a plan but not begun"},
		{status: "in progress", want: true, why: "tasks are executing"},
		{status: "shipped", want: false, why: "already done"},
		{status: "partially complete", want: false, why: "stalled -- the status that most looks like progress"},
		{status: "abandoned", want: false, why: "deliberately stopped"},
	} {
		got := milestoneInFlight(store.MilestoneStatus(tc.status))
		if got != tc.want {
			t.Errorf("milestoneInFlight(%q) = %t, want %t (%s)", tc.status, got, tc.want, tc.why)
		}
	}
}

// TestMilestoneInFlightTableCoversEveryValidStatus closes the hole the
// table above cannot see by itself: a ninth status added to the store
// would be classified by the default branch and never appear here, so the
// table would still pass. Comparing it against the store's own exported
// valid set -- not against inFlightStatuses, which is the thing under
// test -- makes a new status fail instead.
func TestMilestoneInFlightTableCoversEveryValidStatus(t *testing.T) {
	cases := map[string]struct{ want bool }{
		"not started":        {false},
		"in design":          {true},
		"designed":           {false},
		"planned":            {false},
		"in progress":        {true},
		"shipped":            {false},
		"partially complete": {false},
		"abandoned":          {false},
	}

	if len(handlers.ValidMilestoneStatuses) != len(cases) {
		t.Fatalf("store has %d valid statuses, this test tables %d; re-table it",
			len(handlers.ValidMilestoneStatuses), len(cases))
	}
	for status := range handlers.ValidMilestoneStatuses {
		tc, ok := cases[string(status)]
		if !ok {
			t.Errorf("status %q has no row in the classification table; add one", status)
			continue
		}
		if got := milestoneInFlight(status); got != tc.want {
			t.Errorf("milestoneInFlight(%q) = %t, want %t", status, got, tc.want)
		}
	}
}

// TestInFlightStatusesMatchesTheClassification pins the pair the delivery
// read is narrowed by to the predicate that re-checks its rows. The two
// disagreeing is the specific failure: the read would filter a container
// out and the predicate would then be deciding rows that never arrived.
func TestInFlightStatusesMatchesTheClassification(t *testing.T) {
	var kept []string
	for status := range handlers.ValidMilestoneStatuses {
		if milestoneInFlight(status) {
			kept = append(kept, string(status))
		}
	}
	// Map iteration is randomized, so compare as sorted sets.
	slices.Sort(kept)
	slices.Sort(inFlightStatusesStrings())
	want := []string{"in design", "in progress"}
	if len(kept) != len(want) {
		t.Fatalf("milestoneInFlight accepts %v, want exactly %v", kept, want)
	}
	for i := range want {
		if kept[i] != want[i] {
			t.Fatalf("milestoneInFlight accepts %v, want exactly %v", kept, want)
		}
	}
}

// inFlightStatusesStrings is inFlightStatuses as plain strings.
func inFlightStatusesStrings() []string {
	out := make([]string, 0, len(inFlightStatuses))
	for _, s := range inFlightStatuses {
		out = append(out, string(s))
	}
	return out
}

// ---------------------------------------------------------------------------
// the in-flight list
// ---------------------------------------------------------------------------

// TestInFlightOfListsEveryInFlightContainer covers both container kinds
// and both in-flight statuses, and the near-misses beside them. A parent
// whose own status is not in flight is not the only thing that matters:
// a designed milestone can hold a milepebble in progress, and hiding that
// reports a quiet product that is in fact being built.
func TestInFlightOfListsEveryInFlightContainer(t *testing.T) {
	listing := slice.DeliveryListing{
		Milestones: []slice.MilestoneListingEntry{
			{
				Name:   "Executing milestone",
				Status: store.MilestoneStatusInProgress,
				Milepebbles: []slice.MilepebbleListingEntry{
					{Name: "Executing milepebble", Status: store.MilestoneStatusInProgress},
					{Name: "Planned milepebble", Status: store.MilestoneStatusPlanned},
				},
			},
			{
				Name:   "Designed milestone",
				Status: store.MilestoneStatusDesigned,
				Milepebbles: []slice.MilepebbleListingEntry{
					{Name: "Writing the milepebble", Status: store.MilestoneStatusInDesign},
					{Name: "Stalled milepebble", Status: store.MilestoneStatusPartiallyComplete},
				},
			},
			{Name: "Shipped milestone", Status: store.MilestoneStatusShipped},
		},
	}

	var got []string
	for _, m := range inFlightOf(listing) {
		got = append(got, m.Name+"|"+m.Status)
	}
	want := []string{
		"Executing milestone|in progress",
		"Executing milepebble|in progress",
		"Writing the milepebble|in design",
	}
	if len(got) != len(want) {
		t.Fatalf("in flight = %v, want exactly %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("in flight[%d] = %q, want %q (got %v)", i, got[i], want[i], got)
		}
	}
}

// ---------------------------------------------------------------------------
// the Overview header
// ---------------------------------------------------------------------------

// overviewPaths are the two URLs that must render the Overview. Both are
// spelled out rather than built from a production table, so a route
// dropped from the registration cannot shrink the walk.
func overviewPaths() []string {
	return []string{
		"/",
		"/products/" + overviewProduct.String() + "/overview",
	}
}

// TestOverviewHeaderShowsThePrimaryActionAboveZero is the N>0 shape: the
// product is the heading, every in-flight container is named in a badge,
// and the one primary action reads the count and links to the Escalated
// tab. The count and the href are literals -- a header that rendered its
// own data would pass a self-derived expectation.
func TestOverviewHeaderShowsThePrimaryActionAboveZero(t *testing.T) {
	mux := overviewMux(t, overviewListing(), nil, readableCount(3))

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		header := overviewRegion(body, "page-title")
		if header == "" {
			t.Errorf("GET %s rendered no Overview heading", path)
			continue
		}
		if !strings.Contains(collapsed(header), "krill") {
			t.Errorf("GET %s: heading is not the product name: %s", path, collapsed(header))
		}

		action := overviewRegion(body, "overview-primary-action")
		if action == "" {
			t.Fatalf("GET %s rendered no primary action at N=3", path)
		}
		text := collapsed(action)
		if want := "Review 3 escalated tasks"; !strings.Contains(text, want) {
			t.Errorf("GET %s: primary action text = %q, want it to contain %q", path, text, want)
		}
		// The Escalated tab of Needs attention, spelled out: /ops/escalated
		// is where the same rows are listed today, and a link to a page
		// that has not shipped is how an operator who believes something is
		// stuck arrives at a page that says nothing is.
		tag := elementTag(body, "overview-primary-action")
		if want := `href="/ops/escalated"`; !strings.Contains(tag, want) {
			t.Errorf("GET %s: primary action tag %q does not carry %s", path, tag, want)
		}

		if strings.Contains(body, `data-krill="overview-nothing-escalated"`) {
			t.Errorf("GET %s: header says nothing is escalated while it reads 3", path)
		}
	}
}

// TestOverviewHeaderHasNoPrimaryActionAtZero is the N=0 shape, and the
// absence is the assertion: an action reading "Review 0 escalated tasks"
// is the specific wrong thing here, so the test checks the action hook is
// gone rather than that its count reads zero.
func TestOverviewHeaderHasNoPrimaryActionAtZero(t *testing.T) {
	mux := overviewMux(t, overviewListing(), nil, readableCount(0))

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		if strings.Contains(body, `data-krill="overview-primary-action"`) {
			t.Errorf("GET %s: the primary action is present at N=0; it must be absent", path)
		}
		if strings.Contains(body, "Review 0") {
			t.Errorf("GET %s: header offers to review zero escalated tasks", path)
		}
		nothing := overviewRegion(body, "overview-nothing-escalated")
		if nothing == "" {
			t.Errorf("GET %s: header neither acts nor says nothing is escalated", path)
			continue
		}
		if want := "Nothing is escalated."; !strings.Contains(collapsed(nothing), want) {
			t.Errorf("GET %s: header says %q, want it to contain %q", path, collapsed(nothing), want)
		}
	}
}

// TestOverviewHeaderSaysNothingAboutEscalationWhenUnreadable covers the
// third badge state. An unreadable count must not be rendered as a zero:
// "nothing is escalated" would be a second unverified claim on a page
// whose whole job is verified claims, and an action reading 0 would send
// an operator looking for something that may well be there.
func TestOverviewHeaderSaysNothingAboutEscalationWhenUnreadable(t *testing.T) {
	mux := overviewMux(t, overviewListing(), nil, &overviewCounter{readable: false})

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		if strings.Contains(body, `data-krill="overview-primary-action"`) {
			t.Errorf("GET %s: primary action rendered from an unreadable count", path)
		}
		if strings.Contains(body, `data-krill="overview-nothing-escalated"`) {
			t.Errorf("GET %s: header claims nothing is escalated from an unreadable count", path)
		}
	}
}

// TestOverviewHeaderSaysSoWhenNoMilestoneIsInFlight is the other half of
// the FR's "when no milestone is in flight the header says so": an empty
// answer is a sentence an operator can read, not a blank panel they have
// to interpret as a rendering failure.
func TestOverviewHeaderSaysSoWhenNoMilestoneIsInFlight(t *testing.T) {
	mux := overviewMux(t, nothingInFlightListing(), nil, readableCount(2))

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		empty := overviewRegion(body, "overview-no-in-flight")
		if empty == "" {
			t.Fatalf("GET %s: no in-flight milestone, but the header has no empty state", path)
		}
		if want := "Nothing is in flight."; !strings.Contains(collapsed(empty), want) {
			t.Errorf("GET %s: empty state says %q, want it to contain %q", path, collapsed(empty), want)
		}
		if strings.Contains(body, `data-krill="overview-in-flight"`) {
			t.Errorf("GET %s: an in-flight badge list rendered with nothing in flight", path)
		}
		// Nothing in flight is not the same as nothing escalated: the
		// action beside it is still the header's other job.
		if !strings.Contains(body, `data-krill="overview-primary-action"`) {
			t.Errorf("GET %s: nothing is in flight but the escalated action is gone at N=2", path)
		}
	}
}

// TestOverviewHeaderNamesEachInFlightContainer is the "each" in the FR:
// one badge per in-flight container, naming it, with the status that put
// it there beside it. The containers that are not in flight must not be
// named, so the two shipped ones below would fail here if the page listed
// the whole roadmap.
func TestOverviewHeaderNamesEachInFlightContainer(t *testing.T) {
	listing := overviewListing()
	listing.Milestones = append(listing.Milestones, slice.MilestoneListingEntry{
		ID:     uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000007"),
		Name:   "Half-built milestone",
		Status: store.MilestoneStatusPartiallyComplete,
		Milepebbles: []slice.MilepebbleListingEntry{
			{Name: "Executing milepebble", Status: store.MilestoneStatusInProgress},
			{Name: "Up-next milepebble", Status: store.MilestoneStatusDesigned},
		},
	})
	mux := overviewMux(t, listing, nil, readableCount(1))

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		badges := overviewRegion(body, "overview-in-flight")
		if badges == "" {
			t.Fatalf("GET %s rendered no in-flight badge list", path)
		}
		for _, want := range []string{
			"Build the thing", "in progress",
			"Design the thing", "in design",
			"Executing milepebble", "in progress",
		} {
			if !strings.Contains(badges, want) {
				t.Errorf("GET %s: in-flight badges do not name %q", path, want)
			}
		}
		for _, unwanted := range []string{
			"Ship the thing", "Abandon the thing", "Half-built milestone", "Up-next milepebble",
		} {
			if strings.Contains(badges, unwanted) {
				t.Errorf("GET %s: in-flight badges name %q, which is not in flight", path, unwanted)
			}
		}
		// One badge per in-flight container, counted rather than assumed.
		if n := strings.Count(badges, `<span class="font-medium">`); n != 3 {
			t.Errorf("GET %s: rendered %d in-flight badges, want 3: %s", path, n, badges)
		}
	}
}

// TestOverviewFailedInFlightReadDoesNotClaimNothingIsInFlight is the
// failure path of the one read the header cannot do without. Answering
// "nothing is in flight" because the read failed would be the page's
// central question answered wrongly and confidently.
func TestOverviewFailedInFlightReadDoesNotClaimNothingIsInFlight(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, store.ErrNotFound, readableCount(0))

	for _, path := range overviewPaths() {
		rec := fetch(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (the header's own failure is rendered inline)", path, rec.Code)
		}
		body := rec.Body.String()

		if !strings.Contains(body, `data-krill="overview-in-flight-error"`) {
			t.Errorf("GET %s: no inline error for the failed in-flight read", path)
		}
		if strings.Contains(body, `data-krill="overview-no-in-flight"`) {
			t.Errorf("GET %s: claims nothing is in flight after a failed read", path)
		}
		if strings.Contains(body, `data-krill="overview-in-flight"`) {
			t.Errorf("GET %s: rendered an in-flight badge list after a failed read", path)
		}
	}
}

// TestOverviewReadsTheEscalatedCountOnce pins the single read the FR's
// "the badge and this page's action are the same number" depends on: the
// Overview puts the figure it read on the request, so the chrome reuses it
// rather than reading the queue again and rendering two numbers.
func TestOverviewReadsTheEscalatedCountOnce(t *testing.T) {
	counter := readableCount(4)
	mux := overviewMux(t, overviewListing(), nil, counter)

	fetch(t, mux, "/products/"+overviewProduct.String()+"/overview")

	if counter.callCount != 1 {
		t.Errorf("CountEscalatedTasks called %d times for one Overview, want 1 (the header and the sidebar badge must be one read)", counter.callCount)
	}
}

// ---------------------------------------------------------------------------
// the chrome's path targets
// ---------------------------------------------------------------------------

// TestShellPathTargets reads the product and container ids off the three
// URL shapes the shell serves, so Tasks and Board link at the right
// container's pages on every milestone-scoped page with no per-page work.
// The paths and the expected ids are literals; a table built from
// shellPathTargets' own output would pass whatever it produced.
func TestShellPathTargets(t *testing.T) {
	const (
		pid = "44444444-4444-4444-4444-444444444444"
		mid = "55555555-5555-5555-5555-555555555555"
	)
	for _, tc := range []struct {
		path          string
		wantProduct   string
		wantMilestone string
		why           string
	}{
		{
			path:        "/products/" + pid + "/overview",
			why:         "the shell's own product prefix, no container",
			wantProduct: pid,
		},
		{
			path:          "/products/" + pid + "/milestones/" + mid + "/tasks",
			why:           "the container hangs off the same prefix",
			wantProduct:   pid,
			wantMilestone: mid,
		},
		{
			path:        "/spec/products/" + pid + "/decisions",
			why:         "the spec prefix carries its own area, then the id",
			wantProduct: pid,
		},
		{
			path:          "/spec/products/" + pid + "/milestones/" + mid + "/board",
			why:           "the spec prefix's milestone subtree",
			wantProduct:   pid,
			wantMilestone: mid,
		},
		{
			path:        "/design/products/" + pid + "/design-sessions",
			why:         "the design prefix carries its own area, then the id",
			wantProduct: pid,
		},
		{
			path:          "/design/products/" + pid + "/milestones/" + mid,
			why:           "the design prefix's milestone subtree",
			wantProduct:   pid,
			wantMilestone: mid,
		},
		{path: "/", why: "the home names no product"},
		{path: "/ops/escalated", why: "the un-prefixed console names no product"},
		{path: "/account/credentials", why: "the credentials page is not product-scoped"},
		{path: "/products/not-a-uuid/overview", why: "an id-shaped segment that is not an id"},
		{path: "/products", why: "the bare prefix names no product"},
		{path: "/spec/products/" + pid + "/milestones", why: "no container id follows", wantProduct: pid},
	} {
		t.Run(fmt.Sprintf("%s", tc.path), func(t *testing.T) {
			product, milestone := shellPathTargets(tc.path)

			wantProduct, wantMilestone := uuid.Nil, uuid.Nil
			if tc.wantProduct != "" {
				wantProduct = uuid.MustParse(tc.wantProduct)
			}
			if tc.wantMilestone != "" {
				wantMilestone = uuid.MustParse(tc.wantMilestone)
			}
			if product != wantProduct {
				t.Errorf("shellPathTargets(%q) product = %s, want %s (%s)", tc.path, product, wantProduct, tc.why)
			}
			if milestone != wantMilestone {
				t.Errorf("shellPathTargets(%q) milestone = %s, want %s (%s)", tc.path, milestone, wantMilestone, tc.why)
			}
		})
	}
}

// TestShellPathTargetsBuildsTheContainerNavLinks pins what the product id
// is read for now that both task views are product-wide: Tasks and Board
// link at the product's own pages from EVERY page, whether or not the URL
// it is on carries a container.
//
// The old contract was the opposite -- a page under a container linked at
// that container's own pages, and a page without one fell back to the
// delivery page -- so the same sidebar showed different Tasks and Board
// links depending on which page it was rendered from. That is the shape
// this case now excludes: the two hrefs must be identical from a
// container-scoped URL and from a bare one, and must never name the
// delivery page.
func TestShellPathTargetsBuildsTheContainerNavLinks(t *testing.T) {
	scoped := "/spec/products/" + overviewProduct.String() + "/milestones/" + overviewMilestone.String() + "/board"
	bare := "/products/" + overviewProduct.String() + "/milestones"

	for _, tc := range []struct {
		name string
		path string
	}{
		{name: "under a container", path: scoped},
		{name: "with no container in the URL", path: bare},
	} {
		t.Run(tc.name, func(t *testing.T) {
			product, _ := shellPathTargets(tc.path)
			hrefs := sidebarHrefsByLabel(workspaceNav(navTargets{Product: product}, tc.path))

			if want := productHref(product, tasksSuffix); hrefs["Tasks"] != want {
				t.Errorf("Tasks href = %q, want %q", hrefs["Tasks"], want)
			}
			if want := productHref(product, boardSuffix); hrefs["Board"] != want {
				t.Errorf("Board href = %q, want %q", hrefs["Board"], want)
			}
			for _, label := range []string{"Tasks", "Board"} {
				if strings.Contains(hrefs[label], uuid.Nil.String()) {
					t.Errorf("%s href %q carries uuid.Nil", label, hrefs[label])
				}
				if strings.HasSuffix(hrefs[label], milestonesSuffix) || strings.Contains(hrefs[label], "/milestones/") {
					t.Errorf("%s href %q is still scoped to a milestone", label, hrefs[label])
				}
				if strings.HasSuffix(hrefs[label], "/delivery") {
					t.Errorf("%s href %q still falls back to the delivery page", label, hrefs[label])
				}
			}
			if hrefs["Tasks"] == hrefs["Board"] {
				t.Errorf("Tasks and Board both link at %q; they are two views, not one", hrefs["Tasks"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// the toast host
// ---------------------------------------------------------------------------

// TestToastHostLandsOutsideMain is the placement the shell cutover
// changed: the live region every mutation's confirmation lands in sits
// outside <main>, so the swap that replaces a page's body cannot destroy
// the region that is about to announce the result. A host inside <main>
// answers every "the host is present" assertion and still fails the FR.
func TestToastHostLandsOutsideMain(t *testing.T) {
	mux := overviewMux(t, overviewListing(), nil, readableCount(1))

	for _, path := range shellPagePaths(overviewProduct) {
		body := fetchPage(t, mux, path).Body.String()

		host := strings.Index(body, `id="`+components.ToastHostID+`"`)
		if host < 0 {
			t.Errorf("GET %s rendered no toast host", path)
			continue
		}
		mainEnd := strings.Index(body, "</main>")
		if mainEnd < 0 {
			t.Errorf("GET %s rendered no <main> to place the toast host against", path)
			continue
		}
		if host < mainEnd {
			t.Errorf("GET %s: the toast host is inside <main>; a swap of the body would destroy it", path)
		}
		if tag := elementTag(body, "toast-host"); !strings.Contains(tag, `aria-live="status"`) {
			t.Errorf("GET %s: toast host tag %q is not an aria-live status region", path, tag)
		}
	}
}

// ---------------------------------------------------------------------------
// the Milestones-in-flight panel
// ---------------------------------------------------------------------------

// The panel's own ids, spelled out so an href assertion reads as the URL
// an operator would click rather than a value the fixture produced.
const (
	panelMilestone  = "cccccccc-0000-0000-0000-000000000001"
	panelDesign     = "cccccccc-0000-0000-0000-000000000002"
	panelStalled    = "cccccccc-0000-0000-0000-000000000003"
	panelEmpty      = "cccccccc-0000-0000-0000-000000000004"
	panelParent     = "cccccccc-0000-0000-0000-000000000005"
	panelMilepebble = "cccccccc-0000-0000-0000-000000000006"
)

// panelContainers is the read's whole answer: one container per status the
// FR's classification distinguishes, plus a milepebble in progress under
// a milestone that is not itself in flight. A panel that renders all of
// them, or renders them from the parent's status rather than the
// container's own, fails the rows below.
func panelContainers() []store.ContainerTaskProgress {
	return []store.ContainerTaskProgress{
		milestoneProgress(uuid.MustParse(panelMilestone), "Executing milestone",
			store.MilestoneStatusInProgress, store.TaskLaneCounts{Implementation: 2, Done: 3}),
		milestoneProgress(uuid.MustParse(panelDesign), "Designing milestone",
			store.MilestoneStatusInDesign, store.TaskLaneCounts{}),
		milestoneProgress(uuid.MustParse(panelStalled), "Stalled milestone",
			store.MilestoneStatusPartiallyComplete, store.TaskLaneCounts{Testing: 1}),
		milestoneProgress(uuid.MustParse(panelEmpty), "Unstarted milestone",
			store.MilestoneStatusNotStarted, store.TaskLaneCounts{}),
		milepebbleProgress(uuid.MustParse(panelParent), "Designed parent",
			store.MilestoneStatusDesigned, uuid.MustParse(panelMilepebble), "Executing milepebble",
			store.MilestoneStatusInProgress, store.TaskLaneCounts{Done: 4}),
	}
}

// panelRegion slices the in-flight card out of a rendered page, so an
// assertion cannot be satisfied by the same text elsewhere on the page.
func panelRegion(t *testing.T, body string) string {
	t.Helper()
	r := overviewRegion(body, "overview-in-flight-panel")
	if r == "" {
		t.Fatalf("page rendered no Milestones-in-flight panel:\n%s", body)
	}
	return r
}

// TestInFlightPanelListsOnlyTheInFlightContainers is the classification
// the panel renders against, driven over every one of the store's eight
// statuses. The expected names are literals: the two in-flight ones must
// appear and the other six must not, so a panel that drifted into
// listing planned or partially-complete containers fails here rather than
// passing against a self-derived expectation.
func TestInFlightPanelListsOnlyTheInFlightContainers(t *testing.T) {
	var containers []store.ContainerTaskProgress
	for _, status := range []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusDesigned,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	} {
		containers = append(containers, milestoneProgress(
			uuid.MustParse(panelMilestone), "Container "+string(status), status,
			store.TaskLaneCounts{Done: 1, Implementation: 1}))
	}
	// A milepebble in progress under a parent that is not in flight is
	// in flight itself -- the same rule the header already applies.
	containers = append(containers, milepebbleProgress(
		uuid.MustParse(panelParent), "Designed parent", store.MilestoneStatusDesigned,
		uuid.MustParse(panelMilepebble), "Child in progress", store.MilestoneStatusInProgress,
		store.TaskLaneCounts{Done: 1}))

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(containers...))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		for _, want := range []string{"Container in design", "Container in progress", "Child in progress"} {
			if !strings.Contains(panel, want) {
				t.Errorf("GET %s: the panel does not list the in-flight %q", path, want)
			}
		}
		for _, unwanted := range []string{
			"Container not started", "Container designed", "Container planned",
			"Container shipped", "Container partially complete", "Container abandoned",
			"Designed parent",
		} {
			if strings.Contains(panel, unwanted) {
				t.Errorf("GET %s: the panel lists %q, which is not in flight", path, unwanted)
			}
		}
	}
}

// TestInFlightPanelShowsProgressFromTheRead is the "N of M tasks done"
// line and its bar, taken from the read's own Done and Total rather than
// anything the view re-derives. The counts below are written out so a view
// that summed the lanes differently from the read would fail here.
func TestInFlightPanelShowsProgressFromTheRead(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(panelContainers()...))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		// 3 of 5: two in Implementation, three in Done.
		if want := "3 of 5 tasks done"; !strings.Contains(collapsed(panel), want) {
			t.Errorf("GET %s: the panel does not read %q: %s", path, want, collapsed(panel))
		}
		tag := elementTag(panel, "overview-in-flight-progress")
		if !strings.Contains(tag, `value="3"`) || !strings.Contains(tag, `max="5"`) {
			t.Errorf("GET %s: progress bar tag %q does not carry value=3 max=5", path, tag)
		}
		// The milepebble's own figures, not the parent's.
		if want := "4 of 4 tasks done"; !strings.Contains(collapsed(panel), want) {
			t.Errorf("GET %s: the panel does not read %q for the milepebble: %s", path, want, collapsed(panel))
		}
	}
}

// TestInFlightPanelCountsACancelledTaskTheWayTheReadDoes pins the
// cancelled rule the read documents: a cancelled task counts in the
// total, stays in the lane it was left in, and is Done only if it had
// already completed. A view that re-derived Total by any other rule would
// disagree with the read it is supposed to be presenting.
func TestInFlightPanelCountsACancelledTaskTheWayTheReadDoes(t *testing.T) {
	row := milestoneProgress(uuid.MustParse(panelMilestone), "Executing milestone",
		store.MilestoneStatusInProgress, store.TaskLaneCounts{Implementation: 1, Testing: 1, Done: 2})
	row.Cancelled = 2

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(row))

	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())
	// The 2 cancelled tasks are among the 4, and only the 2 already-Done
	// ones count as done.
	if want := "2 of 4 tasks done"; !strings.Contains(collapsed(panel), want) {
		t.Errorf("panel reads %q, want %q: %s", collapsed(panel), want, collapsed(panel))
	}
}

// TestInFlightPanelSaysNoTasksYetRatherThanZeroOfZero is the container
// with no tasks at all. "0 of 0 tasks done" beside an empty bar reads as
// a container that has stalled rather than one that has not started.
func TestInFlightPanelSaysNoTasksYetRatherThanZeroOfZero(t *testing.T) {
	row := milestoneProgress(uuid.MustParse(panelEmpty), "Fresh milestone",
		store.MilestoneStatusInProgress, store.TaskLaneCounts{})

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(row))
	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())

	if want := "No tasks yet."; !strings.Contains(collapsed(panel), want) {
		t.Errorf("panel says %q, want it to contain %q", collapsed(panel), want)
	}
	if strings.Contains(collapsed(panel), "0 of 0") {
		t.Errorf("panel renders a 0 of 0 progress figure: %s", collapsed(panel))
	}
	if strings.Contains(panel, `data-krill="overview-in-flight-progress"`) {
		t.Errorf("panel renders a progress bar for a container with no tasks: %s", collapsed(panel))
	}
}

// TestInFlightPanelSaysAnInDesignContainerIsNotSignedOff is the in-design
// row: the spec is still being written, so there is nothing to count and
// the sentence says which of the two it is rather than implying zero.
func TestInFlightPanelSaysAnInDesignContainerIsNotSignedOff(t *testing.T) {
	row := milestoneProgress(uuid.MustParse(panelDesign), "Designing milestone",
		store.MilestoneStatusInDesign, store.TaskLaneCounts{})

	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(row))
	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())

	if want := "Spec not yet signed off."; !strings.Contains(collapsed(panel), want) {
		t.Errorf("panel says %q, want it to contain %q", collapsed(panel), want)
	}
	if strings.Contains(panel, `data-krill="overview-in-flight-progress"`) {
		t.Errorf("an in-design container renders a progress bar: %s", collapsed(panel))
	}
	if strings.Contains(collapsed(panel), "No tasks yet.") {
		t.Errorf("an in-design container says it has no tasks; it has no spec yet: %s", collapsed(panel))
	}
}

// TestInFlightPanelLinksEachRowToItsDetail is the navigation the row
// carries: the name links at the container's own milestone-detail page,
// under the product prefix. Both container kinds link -- a milepebble is
// a milestone_ref row, so it has a detail page on the same path.
func TestInFlightPanelLinksEachRowToItsDetail(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(panelContainers()...))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		for _, id := range []string{panelMilestone, panelDesign, panelMilepebble} {
			want := `href="/products/` + overviewProduct.String() + `/milestones/` + id + `"`
			if !strings.Contains(panel, want) {
				t.Errorf("GET %s: no row links to %s", path, want)
			}
		}
	}
}

// TestInFlightPanelShowsTheEmptyStateWhenNoneIsInFlight is the empty
// answer: a designed empty state, not a blank card and not an empty list
// an operator has to interpret as a rendering failure.
func TestInFlightPanelShowsTheEmptyStateWhenNoneIsInFlight(t *testing.T) {
	// Containers exist, but none is in flight: the case an empty list and
	// a failed read must both be distinguishable from.
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress(
		milestoneProgress(uuid.MustParse(panelStalled), "Stalled milestone",
			store.MilestoneStatusPartiallyComplete, store.TaskLaneCounts{Done: 1}),
	))

	for _, path := range overviewPaths() {
		panel := panelRegion(t, fetch(t, mux, path).Body.String())

		empty := overviewRegion(panel, "overview-in-flight-panel-empty")
		if empty == "" {
			t.Errorf("GET %s: nothing is in flight but the panel has no empty state: %s", path, collapsed(panel))
			continue
		}
		if want := "Nothing is in flight"; !strings.Contains(collapsed(empty), want) {
			t.Errorf("GET %s: the empty state says %q, want it to contain %q", path, collapsed(empty), want)
		}
		// And it names why: which statuses would have put a container here.
		if want := "No milestone is in design or in progress."; !strings.Contains(collapsed(empty), want) {
			t.Errorf("GET %s: the empty state says %q, want it to contain %q", path, collapsed(empty), want)
		}
		if strings.Contains(panel, `data-krill="overview-in-flight-row"`) {
			t.Errorf("GET %s: the panel rendered a row with nothing in flight", path)
		}
	}
}

// TestInFlightPanelShowsTheEmptyStateOnAnEmptyRead is the same answer
// from a read that returned no containers at all -- a product whose every
// container is shipped. It is a real answer, not a failure.
func TestInFlightPanelShowsTheEmptyStateOnAnEmptyRead(t *testing.T) {
	mux := overviewMux(t, slice.DeliveryListing{}, nil, withProgress())

	panel := panelRegion(t, fetch(t, mux, "/products/"+overviewProduct.String()+"/overview").Body.String())
	if !strings.Contains(panel, `data-krill="overview-in-flight-panel-empty"`) {
		t.Errorf("an empty read rendered no empty state: %s", collapsed(panel))
	}
}

// TestInFlightPanelFailedProgressReadIsNotAnEmptyPanel is the failure
// path. Rendering a read failure as an empty panel would answer the
// page's central question wrongly and confidently: an operator reads
// "nothing is in flight" and concludes the product is quiet.
func TestInFlightPanelFailedProgressReadIsNotAnEmptyPanel(t *testing.T) {
	counter := withProgress()
	counter.progressErr = store.ErrNotFound
	mux := overviewMux(t, slice.DeliveryListing{}, nil, counter)

	for _, path := range overviewPaths() {
		rec := fetch(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200 (the panel's own failure is rendered inline)", path, rec.Code)
		}
		body := rec.Body.String()

		panel := panelRegion(t, body)
		if !strings.Contains(collapsed(panel), "could not be read") {
			t.Errorf("GET %s: the failed progress read is not reported in the panel: %s", path, collapsed(panel))
		}
		if strings.Contains(panel, `data-krill="overview-in-flight-panel-empty"`) {
			t.Errorf("GET %s: a failed read rendered as an empty panel", path)
		}
		if strings.Contains(panel, `data-krill="overview-in-flight-row"`) {
			t.Errorf("GET %s: a failed read rendered rows", path)
		}
	}
}

// TestInFlightPanelReadsTheWholeProductScope pins the shape of the read
// the panel makes: the deployment's own scope and this product, over
// every incomplete container. Narrowing it to one milestone would make
// the panel report a quiet product that is in fact being built.
func TestInFlightPanelReadsTheWholeProductScope(t *testing.T) {
	var got []store.ProductTaskProgressParams
	counter := &overviewCounter{readable: true}
	counterTaskStore := &recordingProgressStore{overviewCounter: counter, params: &got}

	app := newTestApp(t)
	app.spec = &fakeSpecReader{products: []store.Product{{ID: overviewProduct, Name: "krill"}}}
	app.tasks = counterTaskStore
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	fetch(t, mux, "/products/"+overviewProduct.String()+"/overview")

	if len(got) != 1 {
		t.Fatalf("SummarizeProductTaskProgress called %d times, want 1", len(got))
	}
	if got[0].ScopeID != chromeScopeID {
		t.Errorf("ScopeID = %s, want the deployment's scope %s", got[0].ScopeID, chromeScopeID)
	}
	if got[0].ProductID != overviewProduct {
		t.Errorf("ProductID = %s, want %s", got[0].ProductID, overviewProduct)
	}
	want := store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete}
	if got[0].Scope != want {
		t.Errorf("Scope = %+v, want %+v (every incomplete container, not one milestone)", got[0].Scope, want)
	}
}

// recordingProgressStore records the params the panel's read was called
// with, over the counter's own counter behaviour.
type recordingProgressStore struct {
	*overviewCounter
	params *[]store.ProductTaskProgressParams
}

func (r *recordingProgressStore) SummarizeProductTaskProgress(ctx context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	*r.params = append(*r.params, params)
	return r.overviewCounter.SummarizeProductTaskProgress(ctx, params)
}

// TestInFlightPanelAndHeaderListTheSameContainers is the one predicate,
// twice: a container badged in the header must appear in the panel, and
// one the panel lists must be badged in the header. Two classifications
// is how a product comes to show a milestone in flight in one place and
// nowhere else.
func TestInFlightPanelAndHeaderListTheSameContainers(t *testing.T) {
	listing := overviewListing()
	// The progress read's rows name the same containers the delivery
	// listing does, so the two lists can be compared row for row.
	mux := overviewMux(t, listing, nil, withProgress(
		milestoneProgress(uuid.MustParse(panelMilestone), "Build the thing",
			store.MilestoneStatusInProgress, store.TaskLaneCounts{Done: 1, Testing: 1}),
		milestoneProgress(uuid.MustParse(panelDesign), "Design the thing",
			store.MilestoneStatusInDesign, store.TaskLaneCounts{}),
	))

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		panel := panelRegion(t, body)
		if overviewRegion(body, "overview-in-flight") == "" {
			t.Fatalf("GET %s rendered no header badges to compare against", path)
		}
		for _, m := range inFlightOf(listing) {
			if !strings.Contains(panel, m.Name) {
				t.Errorf("GET %s: the header names %q but the panel omits it", path, m.Name)
			}
		}
		// And the other direction: nothing the panel lists is absent
		// from the header's badge list.
		if n := strings.Count(panel, `data-krill="overview-in-flight-row"`); n != len(inFlightOf(listing)) {
			t.Errorf("GET %s: the panel has %d rows for %d header badges", path, n, len(inFlightOf(listing)))
		}
	}
}


// the Needs-attention panel
// ---------------------------------------------------------------------------

// overviewNow is the instant the panel's fixtures are anchored to. It is
// the wall clock at test start, because the panel measures against the
// real clock -- so anchoring to a fixed date would render every row
// "N days ago". The relative wording is written out below regardless, so
// what is asserted is the unit, not a recomputation of the arithmetic.
var overviewNow = time.Now()

// overviewEscalationTitles are the seven fixtures' titles, newest first --
// seven so the panel's limit of five has something to cut, and the two
// cut rows (the two oldest) are the ones that must not appear.
var overviewEscalationTitles = []string{
	"Escalate newest",
	"Escalate second",
	"Escalate third",
	"Escalate fourth",
	"Escalate fifth",
	"Escalate sixth is cut",
	"Escalate oldest is cut",
}

// overviewEscalationAges is how long before overviewNow each row
// escalated, parallel to overviewEscalationTitles. Distinct values in
// distinct units so a row landing in the wrong slot is visible as the
// wrong wording rather than as a count that still matches.
var overviewEscalationAges = []time.Duration{
	12 * time.Minute,
	1 * time.Hour,
	3 * time.Hour,
	30 * time.Second,
	5 * time.Minute,
	2 * time.Hour,
	26 * time.Hour,
}

// overviewEscalationReasons cycles the store's whole reason vocabulary
// across the seven rows, so the panel cannot satisfy its badge rule by
// rendering one reason on every row: a fixture where all seven escalate
// for the same reason would pass a panel that ignored each row's own.
var overviewEscalationReasons = []store.EscalationReason{
	store.EscalationReasonThrashCap,
	store.EscalationReasonAttemptCap,
	store.EscalationReasonManual,
	store.EscalationReasonThrashCap,
	store.EscalationReasonAttemptCap,
	store.EscalationReasonManual,
	store.EscalationReasonThrashCap,
}

// wantReasonLabels and wantReasonVariants are what each reason above must
// render as -- the human wording and the colour class, both written out
// rather than read back from the style function under test.
var wantReasonLabels = []string{
	"thrash cap", "attempt cap", "manual", "thrash cap", "attempt cap", "manual", "thrash cap",
}

var wantReasonVariants = []string{
	"badge-error", "badge-error", "badge-warning", "badge-error", "badge-error", "badge-warning", "badge-error",
}

// escalatedFixtures builds n escalation rows, newest first, carrying the
// titles, ages and reasons above. The ids and container are derived from the
// index rather than random, so a failing href assertion names the same id
// every run.
func escalatedFixtures(n int) []store.EscalatedTaskRow {
	rows := make([]store.EscalatedTaskRow, 0, n)
	for i := 0; i < n; i++ {
		rows = append(rows, store.EscalatedTaskRow{
			TaskID:      uuid.MustParse(fmt.Sprintf("66666666-6666-6666-6666-00000000000%d", i+1)),
			Title:       overviewEscalationTitles[i],
			DeliveryRef: store.EscalatedTaskDeliveryRef{ID: overviewMilestone, Kind: store.MilestoneKindMilepebble, Title: "P1"},
			EscalationID: uuid.MustParse(
				fmt.Sprintf("77777777-7777-7777-7777-00000000000%d", i+1)),
			Reason:      overviewEscalationReasons[i],
			Lane:        store.LaneTesting,
			EscalatedAt: overviewNow.Add(-overviewEscalationAges[i]),
		})
	}
	return rows
}

// wantRelative is the wording each age in overviewEscalationAges must
// produce -- the panel's own units, written out.
var wantRelative = []string{
	"12 min ago", "1 h ago", "3 h ago", "just now", "5 min ago", "2 h ago", "1 day ago",
}

// attentionMux serves the Overview with n escalations waiting in the
// fixture. The count is set to n as well, so the header's action and the
// panel agree the way they must in production.
func attentionMux(t *testing.T, n int) (*http.ServeMux, *overviewCounter) {
	t.Helper()
	counter := &overviewCounter{
		count:     n,
		readable:  true,
		escalated: escalatedFixtures(n),
	}
	return overviewMux(t, overviewListing(), nil, counter), counter
}

// TestNeedsAttentionListsFiveMostRecentInOrder is the panel's own case:
// seven escalations exist and the panel renders five, newest first, with
// each row's reason badge, title and time. Titles and expected wording
// are literals -- a panel checked against its own fixture would pass an
// implementation that dropped rows or reordered them.
func TestNeedsAttentionListsFiveMostRecentInOrder(t *testing.T) {
	mux, _ := attentionMux(t, 7)

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		list := overviewRegion(body, "needs-attention-list")
		if list == "" {
			t.Errorf("GET %s rendered no needs-attention list at N=7", path)
			continue
		}

		// The panel's own limit: the two oldest rows must not appear.
		if n := strings.Count(list, `data-krill="needs-attention-task"`); n != 5 {
			t.Errorf("GET %s: panel rendered %d rows, want exactly 5", path, n)
		}
		for _, cut := range overviewEscalationTitles[5:] {
			if strings.Contains(list, cut) {
				t.Errorf("GET %s: panel listed %q, which is past its 5-row limit", path, cut)
			}
		}

		// Descending order: each title must appear before the next one's.
		prev := -1
		for i, title := range overviewEscalationTitles[:5] {
			at := strings.Index(list, title)
			if at < 0 {
				t.Errorf("GET %s: panel omitted %q (row %d of 5)", path, title, i+1)
				continue
			}
			if at < prev {
				t.Errorf("GET %s: %q appears after a newer row; the panel is not in descending time order", path, title)
			}
			prev = at
		}

		for i, want := range wantRelative[:5] {
			if !strings.Contains(collapsed(list), want) {
				t.Errorf("GET %s: panel rendered no %q for row %d", path, want, i+1)
			}
		}

		if n := strings.Count(list, `data-krill="needs-attention-reason"`); n != 5 {
			t.Errorf("GET %s: %d reason badges rendered, want one per row (5)", path, n)
		}
		if !strings.Contains(collapsed(list), "thrash cap") {
			t.Errorf("GET %s: no human-readable reason label in the panel", path)
		}
	}
}

// TestNeedsAttentionAsksTheStoreForItsOwnFive pins the narrowing and the
// page size rather than trusting the rendering: the panel must ask for
// this product across all its milestones, and for five rows, so it never
// renders a stale sixth-instead-of-fifth row from a default-size page.
func TestNeedsAttentionAsksTheStoreForItsOwnFive(t *testing.T) {
	mux, counter := attentionMux(t, 7)
	fetch(t, mux, overviewPaths()[0])

	var panel *store.ListEscalatedTasksParams
	for i := range counter.escalations {
		if counter.escalations[i].Page.PageSize == pages.NeedsAttentionMax {
			panel = &counter.escalations[i]
		}
	}
	if panel == nil {
		t.Fatalf("no escalated read asked for a page of %d rows; the panel read a default page instead",
			pages.NeedsAttentionMax)
	}
	if panel.ConsoleFilter.ProductID == nil || *panel.ConsoleFilter.ProductID != overviewProduct {
		t.Errorf("panel read product %v, want the current product %s -- the badge and the panel would describe different tasks",
			panel.ConsoleFilter.ProductID, overviewProduct)
	}
	if panel.ConsoleFilter.MilestoneID != nil {
		t.Errorf("panel narrowed to milestone %s; it must cover the product across all its milestones",
			panel.ConsoleFilter.MilestoneID)
	}
}

// TestNeedsAttentionRowsCarryAbsoluteTimeOnHover is what makes the
// relative figure trustworthy: the exact instant is reachable on hover,
// and it is the same instant the relative figure was derived from.
func TestNeedsAttentionRowsCarryAbsoluteTimeOnHover(t *testing.T) {
	mux, _ := attentionMux(t, 3)

	body := fetch(t, mux, overviewPaths()[0]).Body.String()
	list := overviewRegion(body, "needs-attention-list")

	for i := range 3 {
		want := overviewNow.Add(-overviewEscalationAges[i]).Format(time.RFC3339)
		if !strings.Contains(list, `title="`+want+`"`) {
			t.Errorf("row %d carries no %q title attribute; the exact time is unreachable from the page", i+1, want)
		}
	}
}

// TestNeedsAttentionRowsLinkToTaskDetail checks the drill-in is the task's
// own page, under the current product and the container the escalation
// belongs to.
func TestNeedsAttentionRowsLinkToTaskDetail(t *testing.T) {
	mux, rows := attentionMux(t, 3)

	body := fetch(t, mux, overviewPaths()[0]).Body.String()
	if !strings.Contains(body, `data-krill="needs-attention-list"`) {
		t.Fatalf("no needs-attention list rendered")
	}
	for _, row := range rows.escalated[:3] {
		want := taskDetailPath(overviewProduct, row.DeliveryRef.ID, row.TaskID)
		if !strings.Contains(body, `href="`+want+`"`) {
			t.Errorf("no row links to task detail %s", want)
		}
	}
}

// TestNeedsAttentionEmptyStateIsDesigned is the "none escalated" case:
// a designed empty state, a See all that still works, and never the word
// "0" standing in for an answer.
func TestNeedsAttentionEmptyStateIsDesigned(t *testing.T) {
	mux, _ := attentionMux(t, 0)

	for _, path := range overviewPaths() {
		body := fetch(t, mux, path).Body.String()

		panel := overviewRegion(body, "needs-attention-panel")
		if panel == "" {
			t.Errorf("GET %s rendered no needs-attention panel at all; an absent panel is not an empty state", path)
			continue
		}
		empty := overviewRegion(body, "needs-attention-empty")
		if empty == "" {
			t.Errorf("GET %s rendered a panel with no empty state and no rows -- a blank card", path)
			continue
		}
		text := collapsed(empty)
		if !strings.Contains(text, "Nothing needs attention") {
			t.Errorf("GET %s: empty state reads %q, want it to say nothing is escalated", path, text)
		}
		if strings.Contains(collapsed(panel), "0 escalated") {
			t.Errorf("GET %s: empty state shows a count of 0 in place of a message", path)
		}
		if tag := elementTag(body, "needs-attention-see-all"); !strings.Contains(tag, `href="`+escalatedTabHref+`"`) {
			t.Errorf("GET %s: See all tag %q does not link to the Escalated tab", path, tag)
		}
	}
}

// TestNeedsAttentionFailedReadShowsAnErrorNotAnEmptyList is the panel's
// half of the per-region failure rule: a failed read must not render as
// "nothing is escalated", which is the one claim an operator would act
// on. The rest of the page still renders.
func TestNeedsAttentionFailedReadShowsAnErrorNotAnEmptyList(t *testing.T) {
	counter := &overviewCounter{count: 2, readable: true, escalatedErr: store.ErrNotFound}
	mux := overviewMux(t, overviewListing(), nil, counter)

	for _, path := range overviewPaths() {
		rec := fetch(t, mux, path)
		body := rec.Body.String()

		if rec.Code != http.StatusOK {
			t.Errorf("GET %s: status %d, want 200 -- one failed region must not fail the page", path, rec.Code)
		}
		if errRegion := overviewRegion(body, "needs-attention-error"); errRegion == "" {
			t.Errorf("GET %s: failed panel read rendered no inline error", path)
		}
		if empty := overviewRegion(body, "needs-attention-empty"); empty != "" {
			t.Errorf("GET %s: a failed read rendered the empty state, claiming nothing is escalated", path)
		}
		if rows := attentionRows(body); len(rows) != 0 {
			t.Errorf("GET %s: a failed read rendered %d rows anyway", path, len(rows))
		}
		// The failure must not reach the page as a figure either. The count
		// read is separate and still succeeded here, so the header keeps its
		// "2" -- what must not appear is the panel standing in for that count
		// with a fabricated one.
		panel := collapsed(overviewRegion(body, "needs-attention-panel"))
		if strings.Contains(panel, "0 escalated") || strings.Contains(panel, "Nothing needs attention") {
			t.Errorf("GET %s: the failed panel rendered a count or empty message: %s", path, panel)
		}
		// No store text: the operator is told the read failed in the page's
		// own words, not handed the store's error string.
		if strings.Contains(panel, store.ErrNotFound.Error()) {
			t.Errorf("GET %s: the panel leaked store text into the page: %s", path, panel)
		}
		if inFlight := overviewRegion(body, "overview-in-flight"); inFlight == "" {
			t.Errorf("GET %s: the failed panel read cost the page its in-flight list", path)
		}
		if !strings.Contains(body, `data-krill="needs-attention-see-all"`) {
			t.Errorf("GET %s: the failed read took the See all link with it", path)
		}
	}
}

// TestRelativeTimeWordsEveryUnit pins the panel's wording per unit --
// including the singular forms, which are the ones that go wrong when a
// plural is built by appending an "s" to a shared branch.
func TestRelativeTimeWordsEveryUnit(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{d: 0, want: "just now"},
		{d: 59 * time.Second, want: "just now"},
		{d: time.Minute, want: "1 min ago"},
		{d: 2 * time.Minute, want: "2 min ago"},
		{d: 59 * time.Minute, want: "59 min ago"},
		{d: time.Hour, want: "1 h ago"},
		{d: 2 * time.Hour, want: "2 h ago"},
		{d: 23 * time.Hour, want: "23 h ago"},
		{d: 24 * time.Hour, want: "1 day ago"},
		{d: 72 * time.Hour, want: "3 days ago"},
	} {
		if got := relativeTime(overviewNow.Add(-tc.d), overviewNow); got != tc.want {
			t.Errorf("relativeTime(-%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

// TestEscalationReasonTableCoversEveryValidReason closes the same hole
// the reason table cannot see alone: a fourth reason added to the store
// would fall through the default branch and never appear here. Comparing
// against the store's exported reasons, not against the style function,
// makes a new reason fail instead.
func TestEscalationReasonTableCoversEveryValidReason(t *testing.T) {
	want := map[store.EscalationReason]struct{ label, variant string }{
		store.EscalationReasonThrashCap:  {"thrash cap", "badge-error"},
		store.EscalationReasonAttemptCap: {"attempt cap", "badge-error"},
		store.EscalationReasonManual:     {"manual", "badge-warning"},
	}
	for _, reason := range []store.EscalationReason{
		store.EscalationReasonThrashCap,
		store.EscalationReasonAttemptCap,
		store.EscalationReasonManual,
	} {
		tc, ok := want[reason]
		if !ok {
			t.Errorf("reason %q has no row in the reason table; add one", reason)
			continue
		}
		if got := components.EscalationReasonLabel(string(reason)); got != tc.label {
			t.Errorf("EscalationReasonLabel(%q) = %q, want %q", reason, got, tc.label)
		}
		style := components.EscalationReasonStyle(string(reason))
		if string(style.Variant) != tc.variant {
			t.Errorf("EscalationReasonStyle(%q) variant = %q, want %q", reason, style.Variant, tc.variant)
		}
		if !style.Soft {
			t.Errorf("EscalationReasonStyle(%q) is not soft; the wireframe's badges are", reason)
		}
	}

	// An unknown reason must still render something: a blank badge is the
	// failure the style function's default arm exists to prevent.
	if got := components.EscalationReasonLabel("something-new"); got != "something-new" {
		t.Errorf("an unknown reason must pass through as its own label, got %q", got)
	}
	if v := components.EscalationReasonStyle("something-new").Variant; v != htmxui.BadgeNeutral {
		t.Errorf("an unknown reason rendered as %q, want a neutral badge", v)
	}
}

// TestNeedsAttentionEachRowCarriesItsOwnReason is the badge rule read per
// row rather than per panel. The fixture escalates for a different reason
// on each row, so a panel that rendered one reason on every row -- or that
// reused the previous row's -- is caught here even though every individual
// badge would look correct in isolation.
//
// The label and the colour class are both checked, because a badge whose
// text is right and whose colour is wrong is the same defect as one whose
// text is wrong: an operator scans for the reason, not reads it.
func TestNeedsAttentionEachRowCarriesItsOwnReason(t *testing.T) {
	mux, _ := attentionMux(t, 5)

	rows := attentionRows(fetch(t, mux, overviewPaths()[0]).Body.String())
	if len(rows) != 5 {
		t.Fatalf("panel rendered %d rows, want 5: %q", len(rows), rows)
	}

	for i, row := range rows {
		if want := wantReasonLabels[i]; !strings.Contains(collapsed(row), want) {
			t.Errorf("row %d (%s): no %q badge; it shows another row's reason", i+1, overviewEscalationTitles[i], want)
		}
		if want := wantReasonVariants[i]; !strings.Contains(row, want) {
			t.Errorf("row %d (%s): badge carries no %q class; the reason's colour is wrong",
				i+1, overviewEscalationTitles[i], want)
		}
		if !strings.Contains(row, "badge-soft") {
			t.Errorf("row %d (%s): badge is not soft; the wireframe's are", i+1, overviewEscalationTitles[i])
		}
		// A row must not carry a second reason's wording: two labels on one
		// badge is the shape a template that renders both the raw wire value
		// and the human one takes.
		if n := strings.Count(row, `data-krill="needs-attention-reason"`); n != 1 {
			t.Errorf("row %d carries %d reason badges, want exactly 1", i+1, n)
		}
	}
}

// TestNeedsAttentionRowsAreSelfContained pins the three facts a row
// carries together as one row: its own title, its own link, and its own
// exact instant. Checking each fact against the whole list cannot tell a
// row whose href belongs to a different task, or whose hover instant is a
// neighbour's -- the panel would still contain every expected string, just
// not on the same row.
func TestNeedsAttentionRowsAreSelfContained(t *testing.T) {
	mux, _ := attentionMux(t, 5)

	rows := attentionRows(fetch(t, mux, overviewPaths()[0]).Body.String())
	if len(rows) != 5 {
		t.Fatalf("panel rendered %d rows, want 5: %q", len(rows), rows)
	}

	for i, row := range rows {
		if !strings.Contains(collapsed(row), overviewEscalationTitles[i]) {
			t.Errorf("row %d carries no title %q; the titles are not in row order", i+1, overviewEscalationTitles[i])
		}
		wantHref := taskDetailPath(overviewProduct, overviewMilestone,
			uuid.MustParse(fmt.Sprintf("66666666-6666-6666-6666-00000000000%d", i+1)))
		if !strings.Contains(row, `href="`+wantHref+`"`) {
			t.Errorf("row %d (%s) does not link to its own task detail %s",
				i+1, overviewEscalationTitles[i], wantHref)
		}
		wantInstant := overviewNow.Add(-overviewEscalationAges[i]).Format(time.RFC3339)
		if !strings.Contains(row, `title="`+wantInstant+`"`) {
			t.Errorf("row %d (%s) carries no hover instant %q; it shows a neighbour's",
				i+1, overviewEscalationTitles[i], wantInstant)
		}
		want := wantRelative[i]
		if !strings.Contains(collapsed(row), want) {
			t.Errorf("row %d (%s) renders no %q; its relative time belongs to another row",
				i+1, overviewEscalationTitles[i], want)
		}
		// The instant and the wording derived from it ride one element, so
		// hovering the time the operator actually reads is what surfaces
		// the exact one. Split across two elements the hover would land on
		// something that carries no title at all.
		if !strings.Contains(collapsed(row), `title="`+wantInstant+`"> `+want+` </span>`) &&
			!strings.Contains(collapsed(row), `title="`+wantInstant+`">`+want+`</span>`) {
			t.Errorf("row %d (%s): %q and its relative wording are not on the same element: %s",
				i+1, overviewEscalationTitles[i], wantInstant, row)
		}
	}
}

// TestNeedsAttentionSeeAllLinksToTheEscalatedTab is the panel's own
// "See all", which the FR asks for separately from the header's primary
// action: the panel is where an operator who has read five rows decides
// they want the rest, so this is the link that has to work.
//
// Asserted against the same href constant the panel builds its link from
// rather than a literal path, so this checks that the panel and the header
// point one place without pinning which place that is -- the Escalated tab
// moves when Needs attention ships, and the header's own test is what
// pins the current path.
func TestNeedsAttentionSeeAllLinksToTheEscalatedTab(t *testing.T) {
	// Both panel shapes, because "See all" is the way out of the empty
	// state too: an operator told nothing needs attention is still owed the
	// list that says so.
	for _, n := range []int{0, 5} {
		mux, _ := attentionMux(t, n)

		for _, path := range overviewPaths() {
			body := fetch(t, mux, path).Body.String()
			i := strings.Index(body, `data-krill="needs-attention-see-all"`)
			if i < 0 {
				t.Errorf("GET %s at N=%d: no See all link", path, n)
				continue
			}

			tag := elementTag(body, "needs-attention-see-all")
			if tag == "" {
				t.Errorf("GET %s at N=%d: no See all link", path, n)
				continue
			}
			if want := `href="` + escalatedTabHref + `"`; !strings.Contains(tag, want) {
				t.Errorf("GET %s at N=%d: See all tag %q does not carry %s", path, n, tag, want)
			}
			// The label, not just the destination: a link an operator cannot
			// read is not a way out of the panel.
			if !strings.Contains(collapsed(body[i:]), "See all") {
				t.Errorf("GET %s at N=%d: the See all link carries no readable label", path, n)
			}
		}
	}
}

// TestNeedsAttentionFailedInFlightReadLeavesThePanelAlone is the coupling
// case the per-region rule turns on: two regions, two reads, and one of
// them failing.
//
// The header's in-flight read returning early is correct for the header --
// it says what it could not read rather than claiming nothing is in flight.
// But it must not cost the panel its own read: a product with seven
// escalated tasks whose delivery listing failed to load would otherwise
// render "Nothing needs attention" beside an inline error about something
// else entirely. The empty state is a positive claim about the escalation
// queue, and nothing checked it.
func TestNeedsAttentionFailedInFlightReadLeavesThePanelAlone(t *testing.T) {
	counter := &overviewCounter{
		count: 7, readable: true,
		escalated: escalatedFixtures(7),
	}
	mux := overviewMux(t, slice.DeliveryListing{}, store.ErrNotFound, counter)

	for _, path := range overviewPaths() {
		rec := fetch(t, mux, path)
		if rec.Code != http.StatusOK {
			t.Errorf("GET %s = %d, want 200", path, rec.Code)
		}
		body := rec.Body.String()

		// The header's own failure still renders as the header's failure.
		if !strings.Contains(body, `data-krill="overview-in-flight-error"`) {
			t.Errorf("GET %s: no inline error for the failed in-flight read", path)
		}

		// The panel is a separate region with a separate read, and its own
		// read succeeded. It must render what it read.
		rows := attentionRows(body)
		if len(rows) != 5 {
			t.Errorf("GET %s: panel rendered %d rows after the in-flight read failed, want the 5 it read",
				path, len(rows))
		}
		if empty := overviewRegion(body, "needs-attention-empty"); empty != "" {
			t.Errorf("GET %s: panel rendered its empty state after an unrelated read failed, "+
				"claiming nothing is escalated when the panel's own read succeeded", path)
		}
		if errRegion := overviewRegion(body, "needs-attention-error"); errRegion != "" {
			t.Errorf("GET %s: panel rendered an error though its own read succeeded: %s",
				path, collapsed(errRegion))
		}
	}
}
