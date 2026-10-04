// Coverage for the Milestone status-history view (FR 9a6e7924): the register
// the detail page's rail links to, listed oldest first, each transition with
// its own badge, actor, relative instant with the exact one on hover, and
// its note when it has one.
//
// The invariant this file exists to protect is the rail's count against the
// number of rows rendered here: both come from one StatusHistory read, so an
// operator who followed "3 changes" and landed on a list of a different
// length would be told two things about one container. It is asserted from
// the RENDERED page both times -- the rail's own data-krill-change-count
// attribute and the row elements -- not from the builder, because a builder
// assertion would pass while the markup printed something else.
//
// The region extractor here REQUIRES its marker rather than returning "" on a
// miss: this milestone has already shipped one vacuous-passing region helper
// (productPageRegion returning "" and letting every assertion against it
// succeed), and TestStatusHistoryRegionIsNotVacuous checks the extractor
// actually refuses a page whose region is gone.

package main

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// the specReadClient this view's two reads need
// ---------------------------------------------------------------------------

// statusHistoryReader answers the two reads the view makes -- Delivery, to
// resolve which container the id in the URL is, and StatusHistory, to render
// its register -- and records the ids StatusHistory was asked for.
//
// It is its own type rather than a reuse of the rail's fake because the seam
// additions belong to the file that declares them: this milestone's three
// sibling tasks each added methods to the same seam from parallel branches,
// and a fourth edit into one of their test files is exactly the collision
// that has to be merged by hand.
type statusHistoryReader struct {
	specReadClient
	listing    slice.DeliveryListing
	history    map[uuid.UUID][]store.MilestoneStatusEvent
	historyErr error
	askedFor   []uuid.UUID
}

func (r *statusHistoryReader) Delivery(context.Context, uuid.UUID, []store.MilestoneStatus) (slice.DeliveryListing, error) {
	return r.listing, nil
}

func (r *statusHistoryReader) StatusHistory(_ context.Context, id uuid.UUID) ([]store.MilestoneStatusEvent, error) {
	r.askedFor = append(r.askedFor, id)
	if r.historyErr != nil {
		return nil, r.historyErr
	}
	return r.history[id], nil
}

// DeliveryBreakdown answers empty. The Milestone DETAIL page reads it for its
// own Delivery card, which the count-vs-rows case drives in order to read the
// rail off a real page; this view never calls it.
func (r *statusHistoryReader) DeliveryBreakdown(context.Context, uuid.UUID) (slice.Document, slice.Document, error) {
	return slice.Document{}, slice.Document{}, nil
}

var _ specReadClient = (*statusHistoryReader)(nil)

// ---------------------------------------------------------------------------
// fixtures
// ---------------------------------------------------------------------------

var (
	historyMilestoneID = uuid.MustParse("11111111-1111-1111-1111-111111111111")
	historyPebbleID    = uuid.MustParse("22222222-2222-2222-2222-222222222222")
	historyParentID    = uuid.MustParse("33333333-3333-3333-3333-333333333333")
	historyProductID   = uuid.MustParse("44444444-4444-4444-4444-444444444444")
)

var historyProduct = store.Product{ID: historyProductID, Name: "krill"}

// historyListing is the delivery read's answer: a cut milestone, the
// milepebble under it, and an uncut one.
//
// The milepebble is here so the cases can prove this view answers for either
// kind of container -- they share one {mid} wildcard, and only the listing
// says which an id is.
var historyListing = slice.DeliveryListing{
	Milestones: []slice.MilestoneListingEntry{
		{
			ID:       historyParentID,
			Name:     "M13 Roadmap",
			Status:   store.MilestoneStatusInProgress,
			Milepebbles: []slice.MilepebbleListingEntry{
				{ID: historyPebbleID, Name: "P1 Status history", Status: store.MilestoneStatusPlanned},
			},
		},
		{ID: historyMilestoneID, Name: "M12 UI facelift", Status: store.MilestoneStatusShipped},
	},
}

// historyNow is the clock the pure-builder cases pass, so their rendered ages
// are a function of their inputs rather than of when the suite ran.
var historyNow = time.Date(2026, 3, 14, 12, 0, 0, 0, time.UTC)

// historyEvent builds one transition n days before historyNow, with the
// status, actor and note the cases name.
func historyEvent(id uuid.UUID, status store.MilestoneStatus, daysAgo int, sub, note string) store.MilestoneStatusEvent {
	e := store.MilestoneStatusEvent{
		ID:        id,
		Status:    status,
		CreatedAt: historyNow.AddDate(0, 0, -daysAgo),
		CreatedByActing: store.Subject{
			Iss: "https://krill", Sub: sub, Kind: store.SubjectKindHuman,
		},
	}
	if note != "" {
		e.Note = &note
	}
	// The ordinary case: nobody is acting on anyone else's behalf, so the
	// pair is the actor's own self and the second line is suppressed.
	e.CreatedByOnBehalfOf = e.CreatedByActing
	return e
}

// historyFixture wires an App against the reader above, so a case drives the
// REAL handler through the REAL registered route.
type historyFixture struct {
	reader *statusHistoryReader
	mux    *http.ServeMux
}

func newHistoryFixture(t *testing.T, history map[uuid.UUID][]store.MilestoneStatusEvent) *historyFixture {
	t.Helper()
	reader := &statusHistoryReader{listing: historyListing, history: history}
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: reader, products: []store.Product{historyProduct}}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = &railTasks{}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return &historyFixture{reader: reader, mux: mux}
}

// historyURL is the rail's link target for one container -- built from the
// rail's own helper, so a case naming it cannot drift from what the rail
// renders.
func historyURL(id uuid.UUID) string {
	return milestoneStatusHistoryHref(historyProductID, id)
}

// get renders one container's status history through the registered route.
func (f *historyFixture) get(t *testing.T, id uuid.UUID) string {
	t.Helper()
	rec := fetch(t, f.mux, historyURL(id))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// getDetail renders the container's DETAIL page through the same fixture, so
// the rail's count and this view's rows are two renderings of one read.
func (f *historyFixture) getDetail(t *testing.T, id uuid.UUID) string {
	t.Helper()
	rec := fetch(t, f.mux,
		"/products/"+historyProductID.String()+"/milestones/"+id.String())
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	return rec.Body.String()
}

// historyRegion slices this view's own region out of a rendered page, and
// REQUIRES the marker. The fallible core is split out so the non-vacuity case
// can assert what happens when the region is absent -- a single helper that
// both found and asserted could only ever report success, which is the
// helper this milestone has already been bitten by.
func historyRegion(t *testing.T, html string) string {
	t.Helper()
	region, ok := findHistoryRegion(html)
	require.True(t, ok,
		"the status-history region did not render, so every assertion against it would be vacuous")
	require.NotEmpty(t, region, "the region rendered its marker but no content at all:\n%s", html)
	return region
}

func findHistoryRegion(html string) (string, bool) {
	start := strings.Index(html, `data-krill="milestone-status-history"`)
	if start < 0 {
		return "", false
	}
	end := strings.Index(html[start:], "</section>")
	if end < 0 {
		return html[start:], true
	}
	return html[start : start+end], true
}

// transitionRows slices out every rendered transition row, in document order,
// so an ordering assertion is about the order the operator sees.
func transitionRows(t *testing.T, region string) []string {
	t.Helper()
	const marker = `data-krill="status-transition-row"`
	var rows []string
	rest := region
	for {
		i := strings.Index(rest, marker)
		if i < 0 {
			return rows
		}
		rest = rest[i:]
		end := strings.Index(rest, "</tr>")
		require.GreaterOrEqual(t, end, 0, "an unclosed transition row:\n%s", rest)
		rows = append(rows, rest[:end])
		rest = rest[end:]
	}
}

// railChangeCount reads the rail's own count attribute off a rendered detail
// page -- the number an operator follows to get here.
func railChangeCount(t *testing.T, detailHTML string) int {
	t.Helper()
	raw, ok := attributeValue(detailHTML, `data-krill-change-count="`)
	require.True(t, ok,
		"the detail page's rail carried no change count, so the count this view must match is missing:\n%s",
		detailHTML)
	n, err := strconv.Atoi(raw)
	require.NoError(t, err, "the rail's change count is not a number: %q", raw)
	return n
}

// attributeValue pulls the value after the first occurrence of a
// `name="` prefix. It requires the prefix to be present, so a missing
// attribute fails rather than reading as an empty value.
func attributeValue(html, prefix string) (string, bool) {
	i := strings.Index(html, prefix)
	if i < 0 {
		return "", false
	}
	rest := html[i+len(prefix):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return "", false
	}
	return rest[:j], true
}

// badgeClassesOf returns the rendered class attribute of the first
// `class="badge` element in a region -- the daisyUI classes the operator
// actually sees, not a claim about which mapper was called.
func badgeClassesOf(t *testing.T, region string) string {
	t.Helper()
	from := strings.Index(region, `class="badge`)
	require.GreaterOrEqual(t, from, 0, "no badge rendered in %s", region)
	rest := region[from+len(`class="`):]
	to := strings.Index(rest, `"`)
	require.GreaterOrEqual(t, to, 0, "an unterminated class attribute in %s", region)
	return rest[:to]
}

// ---------------------------------------------------------------------------
// 1. the view model
// ---------------------------------------------------------------------------

// TestStatusHistoryCarriesEveryTransitionWhole is the FR's positive case on
// the view model: N transitions in, N out, each carrying its own status,
// actor, exact instant, relative age and note -- nothing dropped, nothing
// re-ordered.
func TestStatusHistoryCarriesEveryTransitionWhole(t *testing.T) {
	events := []store.MilestoneStatusEvent{
		historyEvent(uuid.MustParse("a0000000-0000-0000-0000-000000000001"),
			store.MilestoneStatusInDesign, 30, "alice", "cut from M11"),
		historyEvent(uuid.MustParse("a0000000-0000-0000-0000-000000000002"),
			store.MilestoneStatusPlanned, 14, "bob", ""),
		historyEvent(uuid.MustParse("a0000000-0000-0000-0000-000000000003"),
			store.MilestoneStatusShipped, 2, "carol", "shipped with M12"),
	}

	app := newTestApp(t)
	app.spec = scopedProductsReader{
		specReadClient: &statusHistoryReader{listing: historyListing, history: map[uuid.UUID][]store.MilestoneStatusEvent{
			historyMilestoneID: events,
		}},
	}
	c, found := resolveTaskContainer(historyListing, historyMilestoneID)
	require.True(t, found)

	page := app.buildMilestoneStatusHistoryPage(context.Background(), historyProduct, c)

	require.Len(t, page.Transitions, len(events), "every transition the read answered with must be listed")
	assert.Empty(t, page.Error)
	assert.Equal(t, "M12 UI facelift", page.ContainerName)
	assert.Equal(t, "Status history", page.Title)
	assert.Equal(t, historyMilestoneID.String(), page.ID)
	assert.Equal(t, string(store.MilestoneKindMilestone), page.Kind)
	assert.Equal(t, historyURL(historyMilestoneID), page.Path)
	assert.Equal(t,
		"/products/"+historyProductID.String()+"/milestones/"+historyMilestoneID.String(),
		page.DetailPath, "back leads to the detail page the operator came from")

	// The read's own order, not a sort this view could get wrong.
	for i, want := range events {
		got := page.Transitions[i]
		assert.Equal(t, want.ID.String(), got.ID)
		assert.Equal(t, string(want.Status), got.Status)
		assert.Equal(t, want.CreatedAt.UTC().Format(time.RFC3339), got.At)
		assert.Contains(t, got.Actor, want.CreatedByActing.Sub)
	}

	// The note only on the transition that recorded one.
	assert.Equal(t, "cut from M11", page.Transitions[0].Note)
	assert.Empty(t, page.Transitions[1].Note, "a transition with no note must not borrow its neighbour's")
	assert.Equal(t, "shipped with M12", page.Transitions[2].Note)

	// The on-behalf-of line is suppressed where the pair is the actor's own
	// self, which is every event historyEvent builds.
	for i, tr := range page.Transitions {
		assert.Empty(t, tr.OnBehalfOf,
			"transition %d acts for itself, so no second attribution line", i)
	}
}

// TestStatusHistoryShowsOnBehalfOfOnlyWhenItDiffers: LB4's distinction exists
// to say "this was done by an agent, for this person". A page that collapsed
// the pair, or that printed a second identical line for every row, would make
// exactly that statement inexpressible or unreadable.
func TestStatusHistoryShowsOnBehalfOfOnlyWhenItDiffers(t *testing.T) {
	agent := store.MilestoneStatusEvent{
		ID:        uuid.MustParse("b0000000-0000-0000-0000-000000000001"),
		Status:    store.MilestoneStatusInProgress,
		CreatedAt: historyNow.Add(-3 * time.Hour),
		CreatedByActing: store.Subject{
			Iss: "https://krill", Sub: "swarm-worker-3", Kind: store.SubjectKindAgent,
		},
		CreatedByOnBehalfOf: store.Subject{
			Iss: "https://krill", Sub: "dana", Kind: store.SubjectKindHuman,
		},
	}

	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: &statusHistoryReader{listing: historyListing}}
	c, _ := resolveTaskContainer(historyListing, historyMilestoneID)
	page := app.buildMilestoneStatusHistoryPage(context.Background(), historyProduct, c)
	_ = page

	row := statusTransitionOf(agent, historyNow)
	assert.Contains(t, row.Actor, "swarm-worker-3")
	assert.Contains(t, row.Actor, "agent")
	require.NotEmpty(t, row.OnBehalfOf, "an agent acting for a person must name that person")
	assert.Contains(t, row.OnBehalfOf, "dana")

	// And the same subject on both sides suppresses the line.
	self := agent
	self.CreatedByOnBehalfOf = agent.CreatedByActing
	assert.Empty(t, statusTransitionOnBehalfOf(self))

	// Two subjects differing only in KIND are different subjects: the
	// suppression must compare the whole triple, not just the sub.
	wrongKind := agent
	wrongKind.CreatedByOnBehalfOf.Sub = agent.CreatedByActing.Sub
	wrongKind.CreatedByOnBehalfOf.Iss = agent.CreatedByActing.Iss
	wrongKind.CreatedByOnBehalfOf.Kind = store.SubjectKindHuman
	assert.NotEmpty(t, statusTransitionOnBehalfOf(wrongKind),
		"the same sub recorded under a different kind is not the same subject")
}

// TestStatusHistoryRendersTheReadOrderNotASort: oldest-first is the READ's
// guarantee (ListTransitions orders by created_at ASC, id ASC), and this view
// passes it through rather than re-deriving an order it could get wrong. The
// rendered document order is checked against the read's order exactly.
func TestStatusHistoryRendersTheReadOrderNotASort(t *testing.T) {
	// Three DISTINCT statuses, so a sort by status -- the plausible wrong
	// implementation -- cannot coincide with the read's order by accident.
	events := []store.MilestoneStatusEvent{
		historyEvent(uuid.MustParse("c0000000-0000-0000-0000-000000000001"),
			store.MilestoneStatusShipped, 40, "first-actor", ""),
		historyEvent(uuid.MustParse("c0000000-0000-0000-0000-000000000002"),
			store.MilestoneStatusInDesign, 20, "second-actor", ""),
		historyEvent(uuid.MustParse("c0000000-0000-0000-0000-000000000003"),
			store.MilestoneStatusAbandoned, 5, "third-actor", ""),
	}
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: events,
	})

	rows := transitionRows(t, historyRegion(t, f.get(t, historyMilestoneID)))
	require.Len(t, rows, len(events), "the register must list every transition")

	for i, want := range events {
		assert.Contains(t, rows[i], want.ID.String(),
			"row %d must be the %dth transition the read returned", i, i)
		assert.Contains(t, rows[i], want.CreatedByActing.Sub,
			"row %d must show ITS actor, not another's", i)
	}
}

// TestStatusHistoryBadgesResolveThroughTheSharedMapper: a green suite is not
// proof of correct badge resolution. Every row's badge is compared against
// the class components.MilestoneStatusStyle gives that status -- assembled the
// way htmxui's badgeClasses does -- so a locally-defined second mapper, or a
// hard-coded variant, fails here rather than shipping a wrong colour.
func TestStatusHistoryBadgesResolveThroughTheSharedMapper(t *testing.T) {
	statuses := []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusDesigned,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	}
	// An unknown wire value too: the mapper's neutral fallback must be what
	// renders, not a blank badge.
	labels := append([]string{}, func() []string {
		out := make([]string, 0, len(statuses)+1)
		for _, s := range statuses {
			out = append(out, string(s))
		}
		return append(out, "not a real status")
	}()...)

	events := make([]store.MilestoneStatusEvent, 0, len(labels))
	for i, label := range labels {
		events = append(events, historyEvent(
			uuid.MustParse("d0000000-0000-0000-0000-00000000000"+strconv.Itoa(i)),
			store.MilestoneStatus(label), i+1, "actor", ""))
	}
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: events,
	})

	rows := transitionRows(t, historyRegion(t, f.get(t, historyMilestoneID)))
	require.Len(t, rows, len(labels))

	seen := map[string]bool{}
	for i, label := range labels {
		style := components.MilestoneStatusStyle(label)
		want := "badge"
		if style.Soft {
			want += " badge-soft"
		}
		want += " " + string(style.Variant) + " " + string(style.Size)

		assert.Equal(t, want, badgeClassesOf(t, rows[i]),
			"row %d (%s) must carry the shared mapper's classes", i, label)
		assert.Contains(t, rows[i], label, "the badge is labelled with the status itself")
		seen[string(style.Variant)+"/"+boolSoft(style.Soft)] = true
	}

	// The eight statuses are distinguishable. Several share a hue by
	// design and are separated only by the soft treatment, so a mapper that
	// collapsed them would show one colour twice -- this asserts the palette
	// really does carry the distinction the rest of the page relies on.
	assert.GreaterOrEqual(t, len(seen), 5,
		"the mapper must separate the eight statuses, not paint them all alike")
}

func boolSoft(b bool) string {
	if b {
		return "soft"
	}
	return "solid"
}

// TestStatusHistoryRendersRelativeTimeWithTheExactInstantOnHover: the FR
// requires a relative time with the exact time on hover. The relative text is
// what the operator reads; the exact instant is in the title and the
// datetime, and it must be the transition's own -- a page that printed a
// server-side "now" or reused the previous row's instant would answer
// "exactly when" wrongly.
func TestStatusHistoryRendersRelativeTimeWithTheExactInstantOnHover(t *testing.T) {
	at := time.Date(2026, 3, 10, 8, 30, 0, 0, time.UTC)
	older := time.Date(2026, 1, 2, 3, 4, 0, 0, time.UTC)
	events := []store.MilestoneStatusEvent{
		{
			ID:        uuid.MustParse("e0000000-0000-0000-0000-000000000001"),
			Status:    store.MilestoneStatusInProgress,
			CreatedAt: at,
		},
		{
			ID:        uuid.MustParse("e0000000-0000-0000-0000-000000000002"),
			Status:    store.MilestoneStatusShipped,
			CreatedAt: older,
		},
	}
	for i := range events {
		events[i].CreatedByActing = store.Subject{Iss: "https://krill", Sub: "actor", Kind: store.SubjectKindHuman}
		events[i].CreatedByOnBehalfOf = events[i].CreatedByActing
	}

	rows := transitionRows(t, historyRegion(t, newHistoryFixture(t,
		map[uuid.UUID][]store.MilestoneStatusEvent{historyMilestoneID: events}).get(t, historyMilestoneID)))
	require.Len(t, rows, len(events))

	for i, want := range []time.Time{at, older} {
		exact := want.UTC().Format(time.RFC3339)
		assert.Contains(t, rows[i], `datetime="`+exact+`"`,
			"row %d must carry its own exact instant", i)
		assert.Contains(t, rows[i], `title="`+exact+`"`,
			"row %d must show the exact instant on hover", i)
		assert.Contains(t, rows[i], `data-krill="status-transition-at"`)
		assert.NotContains(t, rows[i], ">just now<",
			"row %d is days old and must not read as just now", i)
	}

	// Two rows, two DIFFERENT instants: a bug that rendered one timestamp
	// for every row would satisfy a single-row assertion.
	assert.NotContains(t, rows[0], older.UTC().Format(time.RFC3339),
		"row 0 must not carry row 1's instant")
	assert.NotContains(t, rows[1], at.UTC().Format(time.RFC3339),
		"row 1 must not carry row 0's instant")
}

// TestStatusHistoryRendersTheNoteWhenThereIsOneAndSaysSoWhenThereIsNot: a
// blank note cell and a note the read dropped look identical, so the absent
// case renders its own explicit marker rather than nothing.
func TestStatusHistoryRendersTheNoteWhenThereIsOneAndSaysSoWhenThereIsNot(t *testing.T) {
	events := []store.MilestoneStatusEvent{
		historyEvent(uuid.MustParse("f0000000-0000-0000-0000-000000000001"),
			store.MilestoneStatusAbandoned, 40, "alice", "superseded by M13"),
		historyEvent(uuid.MustParse("f0000000-0000-0000-0000-000000000002"),
			store.MilestoneStatusPlanned, 3, "bob", ""),
	}
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: events,
	})

	rows := transitionRows(t, historyRegion(t, f.get(t, historyMilestoneID)))
	require.Len(t, rows, len(events))

	assert.Contains(t, rows[0], "superseded by M13")
	assert.Contains(t, rows[0], `data-krill="status-transition-note"`)
	assert.NotContains(t, rows[0], `data-krill="status-transition-note-empty"`,
		"a transition with a note must not also render the none marker")

	assert.NotContains(t, rows[1], "superseded by M13",
		"the second row must not borrow the first row's note")
	assert.Contains(t, rows[1], `data-krill="status-transition-note-empty"`)
}

// TestStatusHistoryEmptyRegisterIsAnAnswerNotAFailure: a container whose
// status has never been set has no transitions, which is exactly why it reads
// "not started". That is a real answer, so it gets a designed empty state --
// and never the table, and never a count of zero dressed as a register.
func TestStatusHistoryEmptyRegisterIsAnAnswerNotAFailure(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{historyMilestoneID: nil})
	region := historyRegion(t, f.get(t, historyMilestoneID))

	assert.Contains(t, region, `data-krill="status-history-empty"`)
	assert.Contains(t, region, "No status changes yet.")
	assert.NotContains(t, region, `data-krill="status-history-table"`,
		"an empty register must not render a table with no rows")
	assert.NotContains(t, region, `data-krill="status-transition-row"`)
	assert.NotContains(t, region, `data-krill="status-history-error"`,
		"no history is not a failed read")

	// The page still names the container and still offers the way back.
	assert.Contains(t, region, "M12 UI facelift")
	assert.Contains(t, region, `/products/`+historyProductID.String()+`/milestones/`+historyMilestoneID.String()+`"`)
}

// TestStatusHistoryAFailedReadSaysSoRatherThanShowingNoHistory: an absent
// count and a count of zero are different facts. A failed read rendering the
// empty state would tell an operator the container's status was never
// touched, which is a claim about the container the page never learned.
func TestStatusHistoryAFailedReadSaysSoRatherThanShowingNoHistory(t *testing.T) {
	reader := &statusHistoryReader{listing: historyListing, historyErr: assert.AnError}
	app := newTestApp(t)
	app.spec = scopedProductsReader{specReadClient: reader, products: []store.Product{historyProduct}}
	app.scopes = productScopeScopes{scope: store.Scope{ID: chromeScopeID}}
	app.tasks = &railTasks{}
	app.designSessions = navStubDesignSessions{}
	mux := http.NewServeMux()
	app.mountShellRoutes(mux)

	rec := fetch(t, mux, historyURL(historyMilestoneID))
	require.Equal(t, http.StatusOK, rec.Code, "a failed register read is not a 500: the page itself is fine")

	region := historyRegion(t, rec.Body.String())
	assert.Contains(t, region, `data-krill="status-history-error"`)
	assert.Contains(t, region, "could not be read")
	assert.NotContains(t, region, "No status changes yet.",
		"a failed read must not render as a container with no history")
	assert.NotContains(t, region, `data-krill="status-transition-row"`)
	assert.NotContains(t, region, "0 status changes",
		"a failed read is not a count of zero")

	// The header survives: the operator still knows which milestone they
	// are on and can go back.
	assert.Contains(t, region, "M12 UI facelift")
	assert.Contains(t, region, `data-krill="status-history-back"`)
}

// ---------------------------------------------------------------------------
// 2. the rail's count against this view's rows
// ---------------------------------------------------------------------------

// TestStatusHistoryRowCountEqualsTheRailLinkCount is the FR's own closing
// clause and the invariant most likely to rot: "the link text count equals
// the number of listed transitions".
//
// It is asserted from two RENDERED pages off one fixture -- the detail page's
// rail, read off its data-krill-change-count attribute, and this view's own
// row elements -- at several register lengths including zero and one, and for
// both kinds of container. A view that paginated, deduplicated, re-sorted into
// a summary, or rendered the parent's register would break it.
func TestStatusHistoryRowCountEqualsTheRailLinkCount(t *testing.T) {
	for _, n := range []int{0, 1, 2, 5} {
		for _, id := range []struct {
			name string
			id   uuid.UUID
		}{
			{"milestone", historyMilestoneID},
			{"milepebble", historyPebbleID},
		} {
			t.Run(id.name+"/"+strconv.Itoa(n)+" changes", func(t *testing.T) {
				history := map[uuid.UUID][]store.MilestoneStatusEvent{
					id.id: historyEventsFor(n),
					// The parent holds a DIFFERENT count on purpose, so a
					// view that read the parent's register for a milepebble
					// would be caught rather than coincidentally agreeing.
					historyParentID: historyEventsFor(n + 4),
				}
				f := newHistoryFixture(t, history)

				rows := transitionRows(t, historyRegion(t, f.get(t, id.id)))
				linked := railChangeCount(t, f.getDetail(t, id.id))

				assert.Len(t, rows, n, "the view must list every transition the read answered with")
				assert.Equal(t, n, linked,
					"the number the operator followed off the rail must be the number of rows they land on")

				// The count is not merely equal but equal to THIS
				// container's: no other register's length may appear.
				for other, events := range history {
					if other == id.id {
						continue
					}
					assert.NotEqual(t, len(events), linked,
						"the rail printed another container's count (%d)", len(events))
				}
			})
		}
	}
}

// TestStatusHistoryRowCountEqualsTheRailLinkCountAfterADroppedTransition is
// the deliberate regression this file's headline case exists for: with the
// view listing one row fewer than the rail counted, the assertion above must
// go red. It is checked here rather than in a comment because the failure it
// guards against is an assertion that cannot tell a dropped row from a
// correct page.
func TestStatusHistoryRowCountEqualsTheRailLinkCountAfterADroppedTransition(t *testing.T) {
	history := map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(4),
	}
	f := newHistoryFixture(t, history)

	rows := transitionRows(t, historyRegion(t, f.get(t, historyMilestoneID)))
	linked := railChangeCount(t, f.getDetail(t, historyMilestoneID))

	require.Len(t, rows, 4, "the fixture must really have listed all four")
	require.Equal(t, 4, linked)

	// Drop the last row, as a truncated render or an off-by-one slice
	// would.
	short := rows[:len(rows)-1]
	assert.NotEqual(t, linked, len(short),
		"a view that drops a transition is exactly what the FR's clause forbids")
	assert.Equal(t, 1, linked-len(short),
		"exactly one row short, which is what the equality assertion above detects")
}

// ---------------------------------------------------------------------------
// 3. routing, scope and the seam
// ---------------------------------------------------------------------------

// TestStatusHistoryReadsTheRegisterOfTheContainerItIsRendering: the read is
// issued for the id the URL named -- not the parent, not the product, and not
// whatever the reader happened to hold first.
func TestStatusHistoryReadsTheRegisterOfTheContainerItIsRendering(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyPebbleID: historyEventsFor(3),
		historyParentID: historyEventsFor(9),
	})

	f.get(t, historyPebbleID)

	require.NotEmpty(t, f.reader.askedFor, "the view must actually read a register")
	for _, asked := range f.reader.askedFor {
		assert.Equal(t, historyPebbleID, asked,
			"the register read must name the container the URL named")
	}
}

// TestStatusHistoryAnswersForAMilepebbleToo: a milepebble is its own
// milestone_ref row and shares the {mid} wildcard, so it answers at this URL
// with its OWN register and its own name -- never its parent's.
func TestStatusHistoryAnswersForAMilepebbleToo(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyPebbleID: historyEventsFor(2),
	})
	region := historyRegion(t, f.get(t, historyPebbleID))

	assert.Contains(t, region, "P1 Status history", "the page names the milepebble")
	assert.Contains(t, region, `data-krill-container-kind="milepebble"`)
	assert.Len(t, transitionRows(t, region), 2)
	assert.NotContains(t, region, "M13 Roadmap",
		"the parent milestone's name must not stand in for the milepebble's")
}

// TestStatusHistoryHeaderNamesTheContainerAndLinksBackToItsDetail: the page
// is a sub-page of the detail, so the trail climbs back up the same way it
// came down, and "Back to milestone" goes to the detail of THIS container.
func TestStatusHistoryHeaderNamesTheContainerAndLinksBackToItsDetail(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(1),
	})
	region := historyRegion(t, f.get(t, historyMilestoneID))

	detailPath := "/products/" + historyProductID.String() + "/milestones/" + historyMilestoneID.String()
	assert.Contains(t, region, `data-krill="status-history-back"`)
	assert.Contains(t, region, `href="`+detailPath+`"`)

	// Breadcrumb: product, Milestones, the container's name, and this page
	// -- the last of which carries no href, because the operator is on it.
	crumbs := regionBetween(t, region, `data-krill="milestone-breadcrumb"`, "</nav>")
	assert.Contains(t, crumbs, "krill")
	assert.Contains(t, crumbs, "M12 UI facelift")
	assert.Contains(t, crumbs, "Status history")
	assert.Equal(t, 4, strings.Count(crumbs, `data-krill="breadcrumb-crumb"`),
		"four levels: product, Milestones, the container, this page")
	assert.Equal(t, 3, strings.Count(crumbs, "<a "),
		"product, Milestones and the container's name are links; only this page, "+
			"which the operator is already on, is not")
	assert.Contains(t, crumbs, `href="`+detailPath+`"`,
		"the container's crumb is the way back to its detail")
}

// TestStatusHistoryIsAnInShell404ForAnIDThisProductDoesNotOwn: an id that
// belongs to another product must never render as though it were this one's,
// and a bad link an operator followed must land inside the shell they can
// navigate back out of.
func TestStatusHistoryIsAnInShell404ForAnIDThisProductDoesNotOwn(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(2),
	})

	for _, tc := range []struct {
		name string
		id   string
	}{
		{"an id this product does not carry", uuid.New().String()},
		{"an id that is not a UUID at all", "not-a-uuid"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := fetch(t, f.mux,
				"/products/"+historyProductID.String()+"/milestones/"+tc.id+milestoneStatusHistorySuffix)

			require.Equal(t, http.StatusNotFound, rec.Code)
			assert.Contains(t, rec.Body.String(), "<html",
				"the 404 must render inside the shell")
			assert.NotContains(t, rec.Body.String(), `data-krill="status-transition-row"`,
				"a refused id must not render somebody's register")
		})
	}
}

// TestStatusHistoryCountsTheRegisterNotTheListing: a product whose listing
// holds several containers must not render all of their transitions on one
// container's page.
func TestStatusHistoryCountsTheRegisterNotTheListing(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(1),
		historyPebbleID:    historyEventsFor(6),
		historyParentID:    historyEventsFor(6),
	})

	region := historyRegion(t, f.get(t, historyMilestoneID))
	assert.Len(t, transitionRows(t, region), 1,
		"one container's page lists one container's register")
	assert.NotContains(t, region, "6 status changes")
}

// TestStatusHistoryRendersInsideTheProductScopedRegion keeps
// productPageRegion honest for this route too: the shared helper slices the
// routed page's own region, and a URL whose region it cannot find would make
// every assertion made against it pass vacuously -- the failure this
// milestone has already shipped once.
func TestStatusHistoryRendersInsideTheProductScopedRegion(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(3),
	})
	html := f.get(t, historyMilestoneID)

	assert.Contains(t, html, `data-krill="milestone-status-history"`,
		"the served page must carry the region marker itself")
	page := productPageRegion(html)
	require.NotEmpty(t, page,
		"productPageRegion must find the status-history region, or assertions "+
			"made against it in other files pass vacuously")
	assert.Contains(t, page, `data-krill="milestone-status-history"`)
}

// TestStatusHistoryRegionIsNotVacuous is the guard against this milestone's
// own defect class. It checks both halves: that the served page really does
// carry content the extractor finds, and that the extractor REFUSES a page
// whose region is gone -- rather than handing back "" and letting every
// assertion against it succeed on nothing.
func TestStatusHistoryRegionIsNotVacuous(t *testing.T) {
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(3),
	})
	html := f.get(t, historyMilestoneID)

	region := historyRegion(t, html)
	require.NotEmpty(t, region)
	assert.Len(t, transitionRows(t, region), 3,
		"the extractor must return the real content, not just the marker")

	// Removing the region takes the marker with it, and the extractor says
	// so.
	without := strings.Replace(html, region, "", 1)
	require.NotContains(t, without, `data-krill="milestone-status-history"`,
		"the removal must actually take the marker out for this to mean anything")
	got, ok := findHistoryRegion(without)
	assert.False(t, ok,
		"a region that did not render must be reported absent, not returned as an empty region")
	assert.Empty(t, got)
}

// TestStatusHistorySummaryLineCountsTheRegister: the sentence under the
// heading is the FR's own words, singular included -- and it counts what the
// page lists, so it cannot say "3 status changes" above two rows.
func TestStatusHistorySummaryLineCountsTheRegister(t *testing.T) {
	assert.Equal(t, "No status changes recorded.",
		pages.MilestoneStatusHistoryPage{}.SummaryLine())
	assert.Equal(t, "1 status change, oldest first.",
		pages.MilestoneStatusHistoryPage{Transitions: []pages.StatusTransition{{}}}.SummaryLine())
	assert.Equal(t, "4 status changes, oldest first.",
		pages.MilestoneStatusHistoryPage{Transitions: make([]pages.StatusTransition, 4)}.SummaryLine())

	// And the rendered page carries the same figure it listed.
	f := newHistoryFixture(t, map[uuid.UUID][]store.MilestoneStatusEvent{
		historyMilestoneID: historyEventsFor(4),
	})
	region := historyRegion(t, f.get(t, historyMilestoneID))
	assert.Contains(t, region, "4 status changes, oldest first.")
	assert.Len(t, transitionRows(t, region), 4)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// historyEventsFor builds n transitions with distinct statuses, actors and
// instants, so a row rendered against the wrong one is visible rather than
// coincidentally right.
func historyEventsFor(n int) []store.MilestoneStatusEvent {
	statuses := []store.MilestoneStatus{
		store.MilestoneStatusInDesign,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusShipped,
	}
	events := make([]store.MilestoneStatusEvent, 0, n)
	for i := range n {
		events = append(events, historyEvent(
			uuid.MustParse("0"+strconv.Itoa(i%10)+"000000-0000-0000-0000-000000000001"),
			statuses[i%len(statuses)], i+1, "actor-"+strconv.Itoa(i), ""))
	}
	return events
}