// The Needs attention page's contract (FR 5fd47f4d): one page whose tabs
// are the ops console's four queues, each labelled with its count, the
// selected tab carried in the URL, the four /ops URLs retired into it, and
// the header's "Updated N ago" stamp carrying the exact instant on hover.
//
// The Escalated tab's own row contract (FR 772b044b) is pinned at the end
// of this file: the columns, the observed escalation id each row carries,
// and the two actions -- with no Release, and no Cancel on a Done-lane task.
//
// The fixture is a record-keeping task store, so the two claims that make
// the page more than a re-render -- "the tab's rows are the matching
// console read, under the current product" and "the badge is the count
// read for the same filters" -- are asserted against what the reads were
// actually asked for rather than against a restatement of it.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// needsAttentionNow is the instant these tests hold the clock at, so the
// freshness stamp's server-rendered relative text and the exact instant in
// its title are both assertions rather than a race against the wall clock.
var needsAttentionNow = time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)

// needsAttentionTasks is the page's whole store surface: the four list
// reads the tabs render, the four count reads their badges show, and a
// record of the ConsoleFilter each was narrowed by.
//
// It embeds store.TaskStore so a read this page does not make nil-panics
// rather than quietly answering from nowhere.
type needsAttentionTasks struct {
	store.TaskStore

	claimed   []store.ClaimedTaskRow
	escalated []store.EscalatedTaskRow
	cancelled []store.CancelledTaskRow
	notes     []store.OpenNoteRow

	// counts is each queue's figure. countErr, when set for a tab, makes
	// that one count read fail -- the case a badge must omit rather than
	// render as a zero.
	counts   map[string]int
	countErr map[string]error

	listFilters  []store.ConsoleFilter
	countFilters map[string][]store.ConsoleFilter

	// listReasons and countReasons keep the escalated read's own reason
	// narrowing -- ListEscalatedTasksParams' field rather than the
	// ConsoleFilter -- so a test can assert the reason filter reached both
	// the list and the count (FR 6369e312).
	listReasons  []*store.EscalationReason
	countReasons map[string][]*store.EscalationReason
}

// recordList and recordCount keep what the page asked for, which is what
// lets a test assert the narrowing rather than restate it.
func (s *needsAttentionTasks) recordList(f store.ConsoleFilter) {
	s.listFilters = append(s.listFilters, f)
}

func (s *needsAttentionTasks) recordCount(tab string, f store.ConsoleFilter) {
	if s.countFilters == nil {
		s.countFilters = map[string][]store.ConsoleFilter{}
	}
	s.countFilters[tab] = append(s.countFilters[tab], f)
}

func (s *needsAttentionTasks) recordListReason(reason *store.EscalationReason) {
	s.listReasons = append(s.listReasons, reason)
}

func (s *needsAttentionTasks) recordCountReason(tab string, reason *store.EscalationReason) {
	if s.countReasons == nil {
		s.countReasons = map[string][]*store.EscalationReason{}
	}
	s.countReasons[tab] = append(s.countReasons[tab], reason)
}

func (s *needsAttentionTasks) ListClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	s.recordList(p.ConsoleFilter)
	return store.Page[store.ClaimedTaskRow]{Items: s.claimed}, nil
}

func (s *needsAttentionTasks) ListEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	s.recordList(p.ConsoleFilter)
	s.recordListReason(p.Reason)
	return store.Page[store.EscalatedTaskRow]{Items: s.escalated}, nil
}

func (s *needsAttentionTasks) ListCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	s.recordList(p.ConsoleFilter)
	return store.Page[store.CancelledTaskRow]{Items: s.cancelled}, nil
}

func (s *needsAttentionTasks) ListOpenNotes(_ context.Context, p store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	s.recordList(p.ConsoleFilter)
	return store.Page[store.OpenNoteRow]{Items: s.notes}, nil
}

func (s *needsAttentionTasks) CountClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (int, error) {
	s.recordCount(needsAttentionTabClaimed, p.ConsoleFilter)
	return s.count(needsAttentionTabClaimed)
}

func (s *needsAttentionTasks) CountEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (int, error) {
	s.recordCount(needsAttentionTabEscalated, p.ConsoleFilter)
	s.recordCountReason(needsAttentionTabEscalated, p.Reason)
	return s.count(needsAttentionTabEscalated)
}

func (s *needsAttentionTasks) CountCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (int, error) {
	s.recordCount(needsAttentionTabCancelled, p.ConsoleFilter)
	return s.count(needsAttentionTabCancelled)
}

func (s *needsAttentionTasks) CountOpenNotes(_ context.Context, p store.ListOpenNotesParams) (int, error) {
	s.recordCount(needsAttentionTabNotes, p.ConsoleFilter)
	return s.count(needsAttentionTabNotes)
}

func (s *needsAttentionTasks) count(tab string) (int, error) {
	if err := s.countErr[tab]; err != nil {
		return 0, err
	}
	return s.counts[tab], nil
}

// needsAttentionFixture is the world these tests walk: one product, one
// scope, and a task store recording what the page asked for.
type needsAttentionFixture struct {
	mux   *http.ServeMux
	app   *App
	pid   uuid.UUID
	tasks *needsAttentionTasks
}

func newNeedsAttentionFixture(t *testing.T) *needsAttentionFixture {
	t.Helper()
	app := newTestApp(t)
	pid := uuid.New()

	app.spec = scopedProductsReader{
		specReadClient: &fakeSpecReader{},
		products:       []store.Product{{ID: pid, Name: "Test product"}},
	}
	app.scopes = chromeScopes{}
	tasks := &needsAttentionTasks{
		claimed: []store.ClaimedTaskRow{{
			TaskID:      uuid.New(),
			Title:       "a claimed task",
			CurrentLane: store.LaneImplementation,
		}},
		escalated: []store.EscalatedTaskRow{{
			TaskID: uuid.New(),
			Title:  "an escalated task",
			Reason: store.EscalationReasonThrashCap,
		}},
		cancelled: []store.CancelledTaskRow{{
			TaskID: uuid.New(),
			Title:  "a cancelled task",
		}},
		notes: []store.OpenNoteRow{{
			NoteID: uuid.New(),
			Kind:   store.NoteKindScopeNote,
			Body:   "an open note",
		}},
		counts:   map[string]int{},
		countErr: map[string]error{},
	}
	app.tasks = tasks
	app.now = func() time.Time { return needsAttentionNow }

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return &needsAttentionFixture{mux: mux, app: app, pid: pid, tasks: tasks}
}

// path is the page's own URL for a tab, spelled with the production builder
// rather than a literal so the tests and the page cannot disagree about the
// address; the successor tests below assert the literal spelling against it.
func (f *needsAttentionFixture) path(tab string) string {
	return needsAttentionTabHref(f.pid, tab)
}

// fetchHX issues one htmx request with the given resolved target id, which
// is the header the handler branches on.
func fetchHX(t *testing.T, mux *http.ServeMux, target, hxTarget string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	if hxTarget != "" {
		req.Header.Set("HX-Target", hxTarget)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// 1. the tab set, its labels, and the URL-carried tab
// ---------------------------------------------------------------------------

// TestNeedsAttentionRendersTheFourTabsWithTheirLabels pins the tab set in
// the FR's own order, each at its own URL, with the request's tab marked
// active -- carried in the URL, so a copied link opens on the tab it names.
func TestNeedsAttentionRendersTheFourTabsWithTheirLabels(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	for _, tc := range []struct {
		tab   string
		label string
	}{
		{needsAttentionTabEscalated, "Escalated"},
		{needsAttentionTabClaimed, "Claimed"},
		{needsAttentionTabCancelled, "Cancelled"},
		{needsAttentionTabNotes, "Open notes"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			body := fetch(t, f.mux, f.path(tc.tab)).Body.String()

			for _, other := range []string{
				needsAttentionTabEscalated, needsAttentionTabClaimed,
				needsAttentionTabCancelled, needsAttentionTabNotes,
			} {
				assert.Contains(t, body, `data-krill="needs-attention-tab" data-krill-tab="`+other+`"`,
					"every tab is offered on every tab's page")
				assert.Contains(t, body, `href="`+needsAttentionTabHref(f.pid, other)+`"`)
			}
			// The requested tab is the one the page says it is showing, and
			// the only one marked selected -- so "which tab am I on" is
			// answered by the markup and not by the URL alone.
			assert.Contains(t, body, `data-krill-tab="`+tc.tab+`"`)
			assert.Contains(t, body, `aria-selected="true"`)
			assert.Equal(t, 1, strings.Count(body, `aria-selected="true"`),
				"exactly one tab is selected")
			assert.Contains(t, body, tc.label)
		})
	}
}

// TestNeedsAttentionUnknownOrAbsentTabResolvesToEscalated is the
// degradation rule: the tab is URL-carried, so a hand-edited or stale link
// reaches the page as readily as a copied one and must render a page rather
// than 404 or an empty region.
func TestNeedsAttentionUnknownOrAbsentTabResolvesToEscalated(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	for _, target := range []string{
		productHref(f.pid, needsAttentionSuffix),
		f.path(needsAttentionTabEscalated),
		needsAttentionTabHref(f.pid, "") + "?tab=nope",
		needsAttentionTabHref(f.pid, "") + "?tab=",
	} {
		t.Run(target, func(t *testing.T) {
			rec := fetch(t, f.mux, target)
			assert.Equal(t, http.StatusOK, rec.Code)
			assert.Contains(t, rec.Body.String(), `data-krill-tab="escalated"`,
				"an absent or unrecognised tab renders the default tab")
		})
	}
}

// ---------------------------------------------------------------------------
// 2. each tab renders its loader's rows, under the current product
// ---------------------------------------------------------------------------

// TestNeedsAttentionTabsRenderTheirOwnQueues is why the page is the four
// queues rather than a second rendering of them: each tab shows the row its
// matching store read returned, and the read was narrowed to the product
// the URL names -- the page's whole scope, across every milestone.
func TestNeedsAttentionTabsRenderTheirOwnQueues(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	for _, tc := range []struct {
		tab  string
		want string
	}{
		{needsAttentionTabEscalated, "an escalated task"},
		{needsAttentionTabClaimed, "a claimed task"},
		{needsAttentionTabCancelled, "a cancelled task"},
		{needsAttentionTabNotes, "an open note"},
	} {
		t.Run(tc.tab, func(t *testing.T) {
			f.tasks.listFilters = nil
			body := fetch(t, f.mux, f.path(tc.tab)).Body.String()

			assert.Contains(t, body, tc.want, "the tab renders the row its own read returned")
			if assert.Len(t, f.tasks.listFilters, 1, "the request makes exactly one list read") {
				filter := f.tasks.listFilters[0]
				if assert.NotNil(t, filter.ProductID, "the tab is narrowed to the current product") {
					assert.Equal(t, f.pid, *filter.ProductID)
				}
				assert.Nil(t, filter.MilestoneID,
					"the scope is the whole product: no milestone narrowing yet")
			}
		})
	}
}

// TestNeedsAttentionScopesToTheProductInTheURL is the other half of the
// rule above, and the one a shared link turns on: another product's page
// reads for THAT product, never for the deployment's first one.
func TestNeedsAttentionScopesToTheProductInTheURL(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	other := uuid.New()
	f.app.spec = scopedProductsReader{
		specReadClient: &fakeSpecReader{},
		products: []store.Product{
			{ID: other, Name: "Another product"},
			{ID: f.pid, Name: "Test product"},
		},
	}

	body := fetch(t, f.mux, needsAttentionTabHref(other, needsAttentionTabEscalated)).Body.String()

	assert.Contains(t, body, `data-krill="needs-attention"`)
	if assert.Len(t, f.tasks.listFilters, 1) {
		if assert.NotNil(t, f.tasks.listFilters[0].ProductID) {
			assert.Equal(t, other, *f.tasks.listFilters[0].ProductID,
				"the read is narrowed to the product the URL names")
		}
	}
}

// ---------------------------------------------------------------------------
// 3. the counts
// ---------------------------------------------------------------------------

// TestNeedsAttentionBadgeEqualsTheCountReadForTheSameFilters is the FR's
// "each labelled with its count (same filters as the table)": the figure on
// a tab is the store's own count read for that queue, under the same
// ConsoleFilter -- and same params type -- the list under it was read with.
func TestNeedsAttentionBadgeEqualsTheCountReadForTheSameFilters(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.tasks.counts = map[string]int{
		needsAttentionTabEscalated: 3,
		needsAttentionTabClaimed:   5,
		needsAttentionTabCancelled: 7,
		needsAttentionTabNotes:     11,
	}

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	for tab, want := range f.tasks.counts {
		assert.Equal(t, want, needsAttentionBadge(t, body, tab),
			"the %s tab's badge is its count read", tab)
		// The count read is narrowed by the same product the table is:
		// same filter on the badge and on the rows beneath it.
		reads := f.tasks.countFilters[tab]
		if assert.NotEmpty(t, reads, "%s: the page reads this tab's count", tab) {
			for _, read := range reads {
				if assert.NotNil(t, read.ProductID, "%s: the count is narrowed to the product", tab) {
					assert.Equal(t, f.pid, *read.ProductID, "%s: the count's product", tab)
				}
			}
		}
	}

	// And the badge is the store's read, not the rendered page's row count:
	// a page showing one row still reports the queue's whole size.
	assert.Equal(t, 3, needsAttentionBadge(t, body, needsAttentionTabEscalated))
}

// TestNeedsAttentionCountReadIsTheWholeQueueNotThePage pins the figure
// against the rows actually on the page: the count is the unfiltered-by-page
// total, so a tab whose table holds one row still reports 42.
func TestNeedsAttentionCountReadIsTheWholeQueueNotThePage(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.tasks.counts[needsAttentionTabClaimed] = 42

	body := fetch(t, f.mux, f.path(needsAttentionTabClaimed)).Body.String()

	assert.Contains(t, body, "a claimed task", "the table still shows its one row")
	assert.Equal(t, 42, needsAttentionBadge(t, body, needsAttentionTabClaimed))
}

// TestNeedsAttentionUnreadableCountRendersNoBadge is the honest-degradation
// case: a count that could not be read is not a zero. The badge is omitted,
// the page still renders, and the failure is logged at WARNING rather than
// reported to the operator as an empty queue.
func TestNeedsAttentionUnreadableCountRendersNoBadge(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.tasks.countErr[needsAttentionTabClaimed] = assert.AnError

	logs := captureWarnings(t)

	rec := fetch(t, f.mux, f.path(needsAttentionTabEscalated))
	body := rec.Body.String()

	assert.Equal(t, http.StatusOK, rec.Code, "an unreadable count must not fail the page")
	assert.NotContains(t, body, `data-krill="needs-attention-count" data-krill-tab="claimed"`,
		"an unreadable count renders no badge")
	// The other three tabs still carry theirs: one failed read is one
	// missing badge, not a strip without counts.
	assert.Contains(t, body, `data-krill="needs-attention-count" data-krill-tab="escalated"`)
	assert.Contains(t, logs.String(), "level=WARN")
	assert.Contains(t, logs.String(), "tab count unreadable")
}

// needsAttentionBadge returns the figure rendered on one tab, failing the
// test when the tab carries no badge element at all -- the distinction the
// FR turns on, and the reason this reads the element rather than a number.
func needsAttentionBadge(t *testing.T, body, tab string) int {
	t.Helper()
	marker := `data-krill="needs-attention-count" data-krill-tab="` + tab + `">`
	start := strings.Index(body, marker)
	if start < 0 {
		t.Fatalf("the %s tab rendered no count badge", tab)
	}
	rest := body[start+len(marker):]
	end := strings.Index(rest, "<")
	if end < 0 {
		t.Fatalf("the %s tab's badge is unterminated", tab)
	}
	n, err := strconv.Atoi(strings.TrimSpace(rest[:end]))
	if err != nil {
		t.Fatalf("the %s tab's badge %q is not a number: %v", tab, rest[:end], err)
	}
	return n
}

// ---------------------------------------------------------------------------
// 4. one route, three response shapes
// ---------------------------------------------------------------------------

// TestNeedsAttentionTabSwapReturnsTheWholeRegion is the in-place swap: a
// tab click names the region and gets it back whole -- strip, counts,
// freshness stamp and the new table together -- so the active marking
// travels with the results. The fragment is bracketed by its own root, not
// a second copy of the strip, which is what makes the click repeatable.
func TestNeedsAttentionTabSwapReturnsTheWholeRegion(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	rec := fetchHX(t, f.mux, f.path(needsAttentionTabNotes), "krill-needs-attention-panel")
	body := rec.Body.String()

	assert.Equal(t, http.StatusOK, rec.Code, "a fragment branch is always 200")
	assert.NotContains(t, body, "<html", "a fragment carries no document")
	// One root carrying the swap target's id: without it htmx deletes the
	// element it was asked to replace and the next click no-ops.
	assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="krill-needs-attention-panel"`),
		"the fragment's root is the swap target:\n%s", body)
	assert.Equal(t, 1, strings.Count(body, `id="krill-needs-attention-panel"`))
	assert.Contains(t, body, `data-krill-tab="notes"`, "the requested tab is the one rendered")
	assert.Contains(t, body, "an open note", "and its own rows reached the fragment")
	assert.NotContains(t, body, "a claimed task", "another tab's rows did not")
	for _, tab := range []string{"escalated", "claimed", "cancelled", "notes"} {
		assert.Contains(t, body, `data-krill="needs-attention-tab" data-krill-tab="`+tab+`"`,
			"the strip travels with the panel, so a second tab click works")
	}
}

// TestNeedsAttentionPollAndRefreshGetTheResultsBlockAlone pins the shape a
// poll receives. The claimed tab's poll and the Refresh button name the
// results block, so the strip -- and with it the freshness stamp, whose
// instant moves on every read -- must not be in the response: a polled
// fragment owes byte-identical bytes for unchanged state.
func TestNeedsAttentionPollAndRefreshGetTheResultsBlockAlone(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	target := f.path(needsAttentionTabClaimed)

	first := fetchHX(t, f.mux, target, "ops-results")
	second := fetchHX(t, f.mux, target, "ops-results")
	body := first.Body.String()

	assert.Equal(t, http.StatusOK, first.Code)
	assert.True(t, strings.HasPrefix(strings.TrimSpace(body), `<div id="ops-results"`),
		"the results block is the fragment's root:\n%s", body)
	assert.NotContains(t, body, "<html")
	assert.NotContains(t, body, "krill-needs-attention-panel",
		"the poll must not receive the swap region it would delete")
	assert.NotContains(t, body, "needs-attention-updated-at",
		"the freshness stamp moves every read and must stay out of the polled fragment")
	assert.NotContains(t, body, `data-krill="needs-attention-tabs"`)
	assert.Equal(t, body, second.Body.String(),
		"an unchanged claimed page must render the same bytes twice")
}

// TestNeedsAttentionHeaderCarriesTheUpdatedAgoStamp is the header element:
// the relative age as the visible text, the exact UTC instant in the
// datetime, the title and the data-krill-updated-at hook the head script
// ages from -- so hovering answers "exactly when" with or without script.
func TestNeedsAttentionHeaderCarriesTheUpdatedAgoStamp(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	// A second clock read, held still, so the relative text is derived from
	// relativeTime's two inputs rather than from the wall clock.
	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	exact := needsAttentionNow.UTC().Format(time.RFC3339)
	assert.Contains(t, body, `data-krill="needs-attention-updated-at"`)
	assert.Contains(t, body, `data-krill-updated-at="`+exact+`"`)
	assert.Contains(t, body, `datetime="`+exact+`"`)
	assert.Contains(t, body, `title="`+exact+`"`)
	// The server-rendered text is the relative form, so the page reads
	// without JavaScript.
	assert.Contains(t, body, "Updated "+relativeTime(needsAttentionNow, needsAttentionNow))
}

// TestNeedsAttentionFragmentSwapDoesNotRecordALastViewedProduct is the
// page-view/swap distinction: a mid-page swap is not a page view, so it
// must not move where the operator's next un-prefixed link lands.
func TestNeedsAttentionFragmentSwapDoesNotRecordALastViewedProduct(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	rec := fetchHX(t, f.mux, f.path(needsAttentionTabEscalated), "krill-needs-attention-panel")
	assert.NotContains(t, rec.Header().Get("Set-Cookie"), lastViewedProductCookie)

	page := fetch(t, f.mux, f.path(needsAttentionTabEscalated))
	assert.Contains(t, page.Header().Get("Set-Cookie"), lastViewedProductCookie,
		"a real page view does record the product")
}

// TestNeedsAttentionReadFailureKeepsTheStripAndSaysSo is the failure
// shape: a refused read must never render as an empty queue, and it must
// not take the strip with it -- a bare error fragment swapped into the
// region would delete the region's own id and leave every later tab click
// a no-op.
func TestNeedsAttentionReadFailureKeepsTheStripAndSaysSo(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.app.tasks = &failingNeedsAttentionTasks{
		needsAttentionTasks: *f.tasks,
		err:                 store.ErrTokenScopeMismatch,
	}

	rec := fetchHX(t, f.mux, f.path(needsAttentionTabClaimed), "krill-needs-attention-panel")
	body := rec.Body.String()

	assert.Equal(t, http.StatusOK, rec.Code, "an htmx swap target's status is not surfaced, so a refusal rides at 200")
	assert.Contains(t, body, `id="krill-needs-attention-panel"`, "the region survives the failure")
	assert.Contains(t, body, `data-krill="needs-attention-tabs"`, "so does the strip")
	assert.Contains(t, body, `data-krill="ops-inline-error"`, "and the refusal is inline in the results slot")
	assert.NotContains(t, body, "store.ErrTokenScopeMismatch")
	assert.NotContains(t, body, "krill/store", "no package-qualified store text reaches the browser")
}

// failingNeedsAttentionTasks fails every list read with err, standing in
// for a store that could not answer.
type failingNeedsAttentionTasks struct {
	needsAttentionTasks
	err error
}

func (f *failingNeedsAttentionTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, f.err
}

func (f *failingNeedsAttentionTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, f.err
}

func (f *failingNeedsAttentionTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, f.err
}

func (f *failingNeedsAttentionTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, f.err
}

// ---------------------------------------------------------------------------
// 5. the /ops cutover
// ---------------------------------------------------------------------------

// TestNeedsAttentionOpsURLsResolveToTheirMatchingTab is the FR's cutover
// clause, walked end to end: each of the five pre-redesign URLs answers a
// 302 (never 404) whose Location is the resolved product's Needs attention
// page at the matching tab, and following it renders that tab inside the
// shell.
//
// The tab is asserted from the LANDED PAGE's own markup as well as from the
// Location, because either alone is satisfiable by a wrong redirect: a
// Location naming the right tab that the page then refuses would still be a
// 302, and a page rendering the default tab behind a Location that said
// otherwise would still be a 200.
func TestNeedsAttentionOpsURLsResolveToTheirMatchingTab(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	for _, tc := range []struct {
		legacy string
		tab    string
		want   string
	}{
		// /ops names no queue of its own, so it lands on the page's bare
		// address -- the default tab, and the address the sidebar links.
		{legacy: opsPath, tab: needsAttentionTabEscalated, want: productHref(f.pid, needsAttentionSuffix)},
		{legacy: opsClaimedPath, tab: needsAttentionTabClaimed, want: needsAttentionTabHref(f.pid, needsAttentionTabClaimed)},
		{legacy: opsEscalatedPath, tab: needsAttentionTabEscalated, want: needsAttentionTabHref(f.pid, needsAttentionTabEscalated)},
		{legacy: opsCancelledPath, tab: needsAttentionTabCancelled, want: needsAttentionTabHref(f.pid, needsAttentionTabCancelled)},
		{legacy: opsNotesPath, tab: needsAttentionTabNotes, want: needsAttentionTabHref(f.pid, needsAttentionTabNotes)},
	} {
		t.Run(tc.legacy, func(t *testing.T) {
			rec := fetch(t, f.mux, tc.legacy)
			if rec.Code == http.StatusNotFound {
				t.Fatalf("GET %s 404s: a pre-redesign URL went dark", tc.legacy)
			}
			assert.Equal(t, http.StatusFound, rec.Code,
				"%s retires into the Needs attention page, so it redirects", tc.legacy)
			assert.Equal(t, tc.want, rec.Header().Get("Location"))

			code, final := followRedirect(t, &legacyFixture{mux: f.mux, app: f.app, pid: f.pid}, tc.legacy)
			assert.Equal(t, http.StatusOK, code, "%s must land on a page", tc.legacy)
			assert.Equal(t, tc.want, final)
			body := fetch(t, f.mux, final).Body.String()
			assert.Contains(t, body, `data-krill-tab="`+tc.tab+`"`,
				"%s lands on the matching tab", tc.legacy)
			assertShellChrome(t, tc.legacy, final, body)
		})
	}
}

// TestNeedsAttentionCutoverMovesOnlyTheFiveOpsGetPages pins the cutover's
// blast radius: the five GET pages move to a successor, and nothing under
// the intervention subtree (/ops/tasks/...) is in the table at all -- those
// routes keep their own registrations in main.go's setupRoutes, so a form
// posted from a console row still reaches them rather than being swallowed
// by a redirect.
func TestNeedsAttentionCutoverMovesOnlyTheFiveOpsGetPages(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	moved := map[string]string{
		opsPath:          "",
		opsClaimedPath:   needsAttentionTabClaimed,
		opsEscalatedPath: needsAttentionTabEscalated,
		opsCancelledPath: needsAttentionTabCancelled,
		opsNotesPath:     needsAttentionTabNotes,
	}

	seen := map[string]bool{}
	for _, l := range legacyURLs() {
		if strings.HasPrefix(l.Pattern, opsTaskActionBase) || strings.Contains(l.Pattern, cancelConfirmSuffix) {
			t.Errorf("legacyURLs now names %s: only the five GET pages are the cutover's to move", l.Pattern)
		}
		tab, isMoved := moved[l.Pattern]
		if !isMoved {
			continue
		}
		seen[l.Pattern] = true
		if l.Successor == nil {
			t.Errorf("%s still serves its page: the Needs attention tab replaces it", l.Pattern)
			continue
		}
		if l.Serve != nil {
			t.Errorf("%s names both a page and a successor", l.Pattern)
		}
		// The successor is driven for real, against a scope holding one
		// product, so the target is the tab this table claims it is.
		target, ok := l.Successor(f.app, httptest.NewRequest(http.MethodGet, l.Pattern, nil))
		if assert.True(t, ok, "%s: a product resolves, so the successor has a target", l.Pattern) {
			assert.Equal(t, needsAttentionTabHref(f.pid, tab), target)
		}
	}
	for pattern := range moved {
		if !seen[pattern] {
			t.Errorf("legacyURLs no longer names %s: an operator's bookmark would 404", pattern)
		}
	}
}

// ---------------------------------------------------------------------------
// 6. the Escalated tab's table (FR 772b044b)
// ---------------------------------------------------------------------------

// escalatedTabSubject is one side of an escalation's subject pair, built
// the way the store builds it, so the sub-line assertions below are about
// what the page renders rather than about a fixture-shaped string.
func escalatedTabSubject(iss, sub string, kind store.SubjectKind) store.Subject {
	return store.Subject{Iss: iss, Sub: sub, Kind: kind}
}

// escalatedTableRow is one row of the read, with every column the FR names
// populated. counter is nil for a manual escalation, which is the case
// with no triggering counter at all.
func escalatedTableRow(t *testing.T, title string, reason store.EscalationReason, counter *int, lane store.Lane, at time.Time) store.EscalatedTaskRow {
	t.Helper()
	return store.EscalatedTaskRow{
		TaskID:                uuid.New(),
		Title:                 title,
		DeliveryRef:           store.EscalatedTaskDeliveryRef{ID: uuid.New(), Kind: store.MilestoneKindMilestone, Title: "M5"},
		EscalationID:          uuid.New(),
		Reason:                reason,
		CounterValue:          counter,
		CapValue:              counter,
		Lane:                  lane,
		EscalatedAt:           at,
		EscalatedByActing:     escalatedTabSubject("worker-3", "w3", store.SubjectKindService),
		EscalatedByOnBehalfOf: escalatedTabSubject("alex", "alex", store.SubjectKindHuman),
		AttemptCount:          3,
	}
}

// TestEscalatedTabRendersTheFRRowContract is the FR's row-by-row contract:
// the seven columns, the task title linking to the product-scoped detail,
// the escalation's own subjects beneath it, the milestone, the shared lane
// badge, the reason badge in its human wording, the attempt count against
// the cap, and the escalated instant as a relative time whose title carries
// the exact UTC instant.
//
// It walks the three reasons the store can report -- the two automatic
// counter-driven ones and the manual one with no counter at all -- because
// the FR names all three and the manual row is the one with no CapValue to
// read.
func TestEscalatedTabRendersTheFRRowContract(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	counter := 3
	at := needsAttentionNow.Add(-12 * time.Minute)
	manualAt := needsAttentionNow.Add(-3 * time.Hour)

	f.tasks.escalated = []store.EscalatedTaskRow{
		escalatedTableRow(t, "a thrash-capped task", store.EscalationReasonThrashCap, &counter, store.LaneImplementation, at),
		escalatedTableRow(t, "an attempt-capped task", store.EscalationReasonAttemptCap, &counter, store.LaneTesting, at),
	}
	// The manual row has no on-behalf-of, which is the sub-line's other
	// shape: the acting half alone, never "for -".
	manual := escalatedTableRow(t, "a manually escalated task", store.EscalationReasonManual, nil, store.LaneScaffold, manualAt)
	manual.CounterValue, manual.CapValue = nil, nil
	manual.EscalatedByOnBehalfOf = store.Subject{}
	manual.AttemptCount = 1
	f.tasks.escalated = append(f.tasks.escalated, manual)

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	// The columns, in the FR's own order.
	assert.Contains(t, body, `<thead><tr><th>Task</th><th>Milestone</th><th>Lane</th><th>Reason</th>`+
		`<th class="text-right">Attempts</th><th>Escalated</th><th class="text-right">Actions</th></tr></thead>`,
		"the table carries the FR's seven columns")

	for _, row := range f.tasks.escalated {
		t.Run(row.Title, func(t *testing.T) {
			// The task cell: the title, linking to the PRODUCT-scoped
			// detail (the row may belong to any milestone under the
			// product).
			assert.Contains(t, body, `href="`+productTaskDetailPath(f.pid, row.TaskID)+`"`,
				"the title links to the product-scoped detail")
			assert.Contains(t, body, `data-krill="escalated-task-title">`+row.Title+`<`)
			// Milestone: the delivery reference's own title.
			assert.Contains(t, body, `data-krill="escalated-milestone">M5<`)
			// Escalated: the exact UTC instant on hover, the relative form
			// as the text.
			exact := row.EscalatedAt.UTC().Format(time.RFC3339)
			assert.Contains(t, body, `datetime="`+exact+`"`)
			assert.Contains(t, body, `title="`+exact+`"`)
			assert.Contains(t, body, ">"+relativeTime(row.EscalatedAt, needsAttentionNow)+"</time>")
		})
	}

	// The escalation's own subjects, not the operator's.
	assert.Contains(t, body, `data-krill="escalated-by">by worker-3 w3 (service) for alex alex<`)
	assert.Contains(t, body, `data-krill="escalated-by">by worker-3 w3 (service)<`,
		"a manual escalation with no on-behalf-of names only the acting half")

	// The reason badge, in the operator's wording for all three reasons.
	for _, label := range []string{"thrash cap", "attempt cap", "manual"} {
		assert.Contains(t, body, `data-krill="escalated-reason">`+label+`</span>`,
			"the reason %q renders through the shared vocabulary", label)
	}

	// The lane badge goes through the one shared mapper, so this table
	// cannot drift from the Tasks list, the Board or the detail.
	assert.Contains(t, body, `data-krill="task-lane">Implementation<`)
	assert.Contains(t, body, `data-krill="task-lane">Testing<`)
	assert.Contains(t, body, `data-krill="task-lane">Scaffold<`)

	// Attempts: the count against the cap. The manual row has no CapValue
	// and still states a cap -- the attempt cap, not the escalation's
	// counter cap, which a thrash-cap row does not have.
	assert.Contains(t, body, `data-krill="escalated-attempts">3 of 3<`)
	assert.Contains(t, body, `data-krill="escalated-attempts">1 of 3<`)
}

// TestEscalatedTabRowCarriesTheObservedEscalationID is the FR's guard
// clause: the row states the escalation it observed, and its two actions
// carry that same id -- as a hidden expected_escalation_id input, on both
// halves of the doubled Cancel control as well as on Requeue's form -- so a
// write is refused against what the operator saw rather than against whatever
// is current by then.
func TestEscalatedTabRowCarriesTheObservedEscalationID(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	counter := 3
	row := escalatedTableRow(t, "a thrash-capped task", store.EscalationReasonThrashCap, &counter, store.LaneImplementation, needsAttentionNow)
	f.tasks.escalated = []store.EscalatedTaskRow{row}
	escID := row.EscalationID.String()

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	assert.Contains(t, body, `data-krill-escalation-id="`+escID+`"`,
		"the row states the escalation it observed")
	assert.Equal(t, 2, strings.Count(body, `name="expected_escalation_id" value="`+escID+`"`),
		"Requeue and Cancel both carry the observed escalation id as their guard")
	assert.Contains(t, body, `hx-post="/ops/tasks/`+row.TaskID.String()+`/cancel"`,
		"Cancel's htmx half posts the cancel route, guarded by that id")
	assert.Contains(t, body, `action="/ops/tasks/`+row.TaskID.String()+`/cancel/confirm"`,
		"and its no-JS half opens the confirmation page, carrying it there")
	assert.Contains(t, body, `hx-confirm="`+cancelConfirmMessage(row.Title)+`"`,
		"the browser confirmation names the task, in the FR's own copy")
	// Release is never offered here: an escalated task holds no claim, so
	// there is nothing to force-close.
	assert.NotContains(t, body, actionRelease)
	assert.NotContains(t, body, ">Release<")
}

// TestEscalatedTabDoneLaneRowOffersNoCancel is the action-legality clause
// (FR af61631d, which the tab's rows must honour): a task in the Done lane
// is offered no Cancel, while Requeue -- the recovery that returns a
// finished-but-escalated task to claimable -- stays.
func TestEscalatedTabDoneLaneRowOffersNoCancel(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	row := escalatedTableRow(t, "a done but escalated task", store.EscalationReasonManual, nil, store.LaneDone, needsAttentionNow)
	row.CounterValue, row.CapValue = nil, nil
	f.tasks.escalated = []store.EscalatedTaskRow{row}

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	assert.Contains(t, body, ">Requeue<", "the recovery is still offered")
	assert.Contains(t, body, `name="expected_escalation_id" value="`+row.EscalationID.String()+`"`,
		"and it still carries the observed escalation id")
	assert.NotContains(t, body, cancelConfirmSuffix,
		"a Done-lane task is offered no Cancel, not even its confirmation page")
	assert.NotContains(t, body, ">Cancel</button>")
	assert.NotContains(t, body, "hx-confirm",
		"and nothing offers a confirmation for an action the row does not have")
}

// TestEscalatedTabRowsMatchTheRead is the FR's "content matches
// list_escalated_tasks": the table's rows are the ones the store read
// returned, under the same product narrowing the badge beside them is read
// with. A row the read did not return cannot appear, and a row it did
// cannot be dropped.
func TestEscalatedTabRowsMatchTheRead(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	counter := 2
	want := []store.EscalatedTaskRow{
		escalatedTableRow(t, "the first escalated task", store.EscalationReasonThrashCap, &counter, store.LaneScaffold, needsAttentionNow.Add(-time.Minute)),
		escalatedTableRow(t, "the second escalated task", store.EscalationReasonAttemptCap, &counter, store.LaneValidation, needsAttentionNow.Add(-2*time.Minute)),
	}
	f.tasks.escalated = want

	body := fetch(t, f.mux, f.path(needsAttentionTabEscalated)).Body.String()

	assert.Equal(t, len(want), strings.Count(body, `data-krill="escalated-row"`),
		"one table row per row the read returned")
	if assert.Len(t, f.tasks.listFilters, 1) {
		if assert.NotNil(t, f.tasks.listFilters[0].ProductID) {
			assert.Equal(t, f.pid, *f.tasks.listFilters[0].ProductID,
				"the rows are the read narrowed to the product the URL names")
		}
	}
	for _, row := range want {
		assert.Contains(t, body, row.Title)
		assert.Contains(t, body, row.EscalationID.String())
	}
}

// ---------------------------------------------------------------------------
// the Cancelled and Open notes tabs' row contracts (FR b22e1d60)
// ---------------------------------------------------------------------------

// TestNeedsAttentionCancelledTabRendersTheRowContract walks the FR's
// cancelled-row clause: the task title links to the product-scoped detail
// page, and the row names its milestone, its lane (as the shared lane
// badge), who cancelled it and when, and the reason when one was given.
func TestNeedsAttentionCancelledTabRendersTheRowContract(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	withReasonID, withoutReasonID := uuid.New(), uuid.New()
	reason := "superseded by the P5 recut"
	f.tasks.cancelled = []store.CancelledTaskRow{
		{
			TaskID:                withReasonID,
			Title:                 "dead-lettered with a reason",
			DeliveryRef:           store.CancelledTaskDeliveryRef{Kind: store.MilestoneKindMilestone, Title: "M5"},
			Lane:                  store.LaneImplementation,
			Reason:                &reason,
			CancelledByActing:     store.Subject{Iss: "https://kc", Sub: "carol", Kind: store.SubjectKindHuman},
			CancelledByOnBehalfOf: store.Subject{Iss: "https://svc", Sub: "operator"},
			CancelledAt:           needsAttentionNow,
		},
		{
			TaskID:      withoutReasonID,
			Title:       "dead-lettered without one",
			DeliveryRef: store.CancelledTaskDeliveryRef{Kind: store.MilestoneKindMilepebble, Title: "MP1"},
			Lane:        store.LaneTesting,
			CancelledAt: needsAttentionNow,
		},
	}

	got := fetchHX(t, f.mux, f.path(needsAttentionTabCancelled), "").Body.String()

	wantHref := productTaskDetailPath(f.pid, withReasonID)
	assert.Contains(t, got, `href="`+wantHref+`"`,
		"the cancelled row's title links to the product-scoped detail page")
	assert.Contains(t, got, productTaskDetailPath(f.pid, withoutReasonID),
		"every cancelled row links, reason or not")
	assert.Contains(t, got, "dead-lettered with a reason")
	assert.Contains(t, got, "milestone: M5", "the row names its milestone container")
	assert.Contains(t, got, "milepebble: MP1", "and a milepebble cut names itself")
	assert.Contains(t, got, `data-krill="task-lane"`, "the lane renders through the shared lane badge")
	assert.Contains(t, got, "Implementation", "the lane the task was cancelled out of")
	assert.Contains(t, got, "Testing")
	assert.Contains(t, got, "https://kc carol (human)", "who cancelled it, with its kind")
	assert.Contains(t, got, "https://svc operator", "and the on-behalf-of subject")
	assert.Contains(t, got, needsAttentionNow.UTC().Format(time.RFC3339), "and when")
	assert.Contains(t, got, reason, "the reason is shown when given")
	assert.Equal(t, 1, strings.Count(got, reason), "only the row that gave a reason carries one")
}

// TestNeedsAttentionCancelledTabEmptyState covers the FR's "empty tabs
// show an empty state" clause for the cancelled queue.
func TestNeedsAttentionCancelledTabEmptyState(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.tasks.cancelled = nil

	got := fetchHX(t, f.mux, f.path(needsAttentionTabCancelled), "").Body.String()
	assert.Contains(t, got, "No cancelled tasks.", "an empty cancelled tab says so")
	assert.NotContains(t, got, `data-krill="needs-attention-cancelled-row"`, "and renders no rows")
}

// TestNeedsAttentionNotesTabRendersTheRowContract walks the FR's
// open-notes-row clause: the task link, a note-kind badge, a
// note-lifecycle-status badge, the body as markdown, and the created
// time.
func TestNeedsAttentionNotesTabRendersTheRowContract(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	taskID, entityID := uuid.New(), uuid.New()
	f.tasks.notes = []store.OpenNoteRow{
		{
			NoteID:      uuid.New(),
			Kind:        store.NoteKindScopeNote,
			Body:        "**bold** body that must not be escaped",
			CreatedAt:   needsAttentionNow,
			TaskContext: &store.OpenNoteTaskContext{TaskID: taskID, Title: "the note's task"},
		},
		{
			NoteID:        uuid.New(),
			Kind:          store.NoteKindComment,
			Body:          "a spec-entity note",
			CreatedAt:     needsAttentionNow,
			EntityContext: &store.OpenNoteEntityContext{EntityKind: store.NoteEntityKindFeature, EntityID: entityID, Title: "a feature"},
		},
	}

	got := fetchHX(t, f.mux, f.path(needsAttentionTabNotes), "").Body.String()

	assert.Contains(t, got, `href="`+productTaskDetailPath(f.pid, taskID)+`"`,
		"a task-targeted note links to its task")
	assert.Contains(t, got, string(store.NoteKindScopeNote), "the note-kind badge renders the store's own kind")
	assert.Contains(t, got, `data-krill="needs-attention-note-kind"`)
	assert.Contains(t, got, string(store.NoteLifecycleStatusNoted), "the note-status badge renders the store's own status")
	assert.Contains(t, got, `data-krill="needs-attention-note-status"`)
	assert.Contains(t, got, "<strong>bold</strong>", "the body renders as markdown")
	assert.NotContains(t, got, "**bold**", "and is not left as raw markdown")
	assert.Contains(t, got, needsAttentionNow.UTC().Format(time.RFC3339), "the created time is shown")

	// A spec-entity note names its target but is NEVER given a task link:
	// exactly one row (the task-targeted one) carries one.
	assert.Contains(t, got, "a feature", "the spec-entity note names its target")
	assert.Equal(t, 1, strings.Count(got, `data-krill="needs-attention-task-link"`),
		"only the task-targeted note links; a spec entity has no task page")
}

// TestNeedsAttentionNotesTabEmptyState covers the FR's empty-tab clause
// for the open-notes queue.
func TestNeedsAttentionNotesTabEmptyState(t *testing.T) {
	f := newNeedsAttentionFixture(t)
	f.tasks.notes = nil

	got := fetchHX(t, f.mux, f.path(needsAttentionTabNotes), "").Body.String()
	assert.Contains(t, got, "No open notes.", "an empty open-notes tab says so")
	assert.NotContains(t, got, `data-krill="needs-attention-note-row"`, "and renders no rows")
}

// TestNeedsAttentionTabSwapAcceptsTheHtmx4TargetHeader guards the tab swap
// against htmx 4's "tag#id" HX-Target: a tab click must get the whole region
// back (strip included), not the bare results that wipe the strip off the page.
func TestNeedsAttentionTabSwapAcceptsTheHtmx4TargetHeader(t *testing.T) {
	f := newNeedsAttentionFixture(t)

	body := fetchHX(t, f.mux, f.path(needsAttentionTabClaimed), "div#"+pages.NeedsAttentionAnchor).Body.String()
	assert.Contains(t, body, `id="`+pages.NeedsAttentionAnchor+`"`, "the swap returns the region it replaces")
	assert.Contains(t, body, `data-krill="needs-attention-tabs"`, "and the tab strip travels with it")
}
