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

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

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

func (*overviewCounter) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
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

// TestShellPathTargetsBuildsTheContainerNavLinks is what the ids are
// read for: a page under a container links Tasks and Board at that
// container's own pages, while the same product's pages with no container
// in the URL link at the delivery page instead.
func TestShellPathTargetsBuildsTheContainerNavLinks(t *testing.T) {
	path := "/spec/products/" + overviewProduct.String() + "/milestones/" + overviewMilestone.String() + "/board"
	product, milestone := shellPathTargets(path)

	hrefs := sidebarHrefsByLabel(workspaceNav(navTargets{Product: product, Milestone: milestone}, path))
	if want := "/spec/products/" + overviewProduct.String() + "/milestones/" + overviewMilestone.String() + "/tasks"; hrefs["Tasks"] != want {
		t.Errorf("Tasks href = %q, want %q", hrefs["Tasks"], want)
	}
	if want := "/spec/products/" + overviewProduct.String() + "/milestones/" + overviewMilestone.String() + "/board"; hrefs["Board"] != want {
		t.Errorf("Board href = %q, want %q", hrefs["Board"], want)
	}

	// The same product with no container in the URL cannot build either
	// href, and must not hand out a uuid.Nil path.
	bare := "/products/" + overviewProduct.String() + "/milestones"
	bareProduct, bareMilestone := shellPathTargets(bare)
	if bareMilestone != uuid.Nil {
		t.Errorf("shellPathTargets(%q) milestone = %s, want uuid.Nil", bare, bareMilestone)
	}
	bareHrefs := sidebarHrefsByLabel(workspaceNav(navTargets{Product: bareProduct, Milestone: bareMilestone}, bare))
	if strings.Contains(bareHrefs["Tasks"], uuid.Nil.String()) {
		t.Errorf("Tasks href %q carries uuid.Nil", bareHrefs["Tasks"])
	}
	if bareHrefs["Tasks"] != bareHrefs["Board"] {
		t.Errorf("Tasks %q and Board %q disagree with no container in scope", bareHrefs["Tasks"], bareHrefs["Board"])
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
		body := fetch(t, mux, path).Body.String()

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
