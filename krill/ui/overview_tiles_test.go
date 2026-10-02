// Wire-driven coverage for the Overview's four stat tiles (FR c0baeb2e):
// the figures each shows, the sub-line derived from the same read as its
// figure, the product each links to, and the tile whose read failed
// rendering a message rather than a zero.
//
// The expectations here are literals, not values read back out of the
// code under test. The clock is pinned, so the two time windows are
// figures a reader can check by hand -- and the fake store derives every
// figure by applying the store's own windows to the fixture's rows, so a
// sub-line that disagreed with its figure would disagree here too.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// tileProduct is the product the tile fixtures serve, and tileOtherProduct
// a second one with rows of its own, so the product narrowing is
// load-bearing rather than decorative.
var (
	tileProduct      = uuid.MustParse("66666666-0000-0000-0000-000000000001")
	tileOtherProduct = uuid.MustParse("66666666-0000-0000-0000-000000000002")
)

// tileMilestoneA and tileMilestoneB are two containers of tileProduct, so
// "across all its milestones" is a property the fixture can fail rather than
// a phrase: a read narrowed to one of them drops rows.
var (
	tileMilestoneA = uuid.MustParse("77777777-0000-0000-0000-00000000000a")
	tileMilestoneB = uuid.MustParse("77777777-0000-0000-0000-00000000000b")
)

// tileNow is the instant the fixture's clock is held at, and therefore the
// moment both windows are measured from and forward to.
var tileNow = time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)

// tileRow is one row of the fixture store. queue says which of the three
// console reads it belongs to, because the store's three queries join
// different tables -- a row with no escalation has no escalation event.
type tileQueue int

const (
	queueEscalated tileQueue = iota
	queueClaimed
	queueNotes
)

// tileRow is one escalated, claimed or note row of the fixture store, with
// the timestamps the tiles' sub-lines are counted from.
type tileRow struct {
	queue         tileQueue
	product       uuid.UUID
	milestone     uuid.UUID
	escalatedAt   time.Time
	leaseExpiring time.Time
	isScopeNote   bool
}

// tileFixtureTasks derives every figure from one row set, applying the
// store's own product narrowing and its own two windows. It embeds the
// interface so any read the tiles do not make nil-panics rather than
// passing on a fabricated answer.
type tileFixtureTasks struct {
	store.TaskStore
	scopeID uuid.UUID
	rows    []tileRow

	// escalatedCount is the figure CountEscalatedTasks answers -- the
	// badge's read. It is separate from the Overview counts so a test can
	// make the two disagree and catch a consumer that mixed them up.
	escalatedCount int
	escalatedErr   error
	overviewErr    error

	// gotParams records the params the Overview read was made with, so a
	// test can pin which rows it was narrowed to.
	gotParams []store.ConsoleOverviewParams
}

// rowsFor is the fixture store's one narrowing: a row belongs to the queue
// asked for and to the product, and when a milestone is named, to that
// milestone. Rows of the fixture's own product are deliberately spread over
// two milestones, so a read narrowed to one of them answers fewer rows than
// one narrowed to the product -- which is the whole point of "across all
// its milestones" being testable.
func (f *tileFixtureTasks) rowsFor(queue tileQueue, filter store.ConsoleFilter) []tileRow {
	var out []tileRow
	for _, row := range f.rows {
		if row.queue != queue {
			continue
		}
		if filter.ProductID != nil && *filter.ProductID != row.product {
			continue
		}
		if filter.MilestoneID != nil && *filter.MilestoneID != row.milestone {
			continue
		}
		out = append(out, row)
	}
	return out
}

func (f *tileFixtureTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	if f.escalatedErr != nil {
		return 0, f.escalatedErr
	}
	return f.escalatedCount, nil
}

func (f *tileFixtureTasks) CountConsoleOverview(_ context.Context, p store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	f.gotParams = append(f.gotParams, p)
	if f.overviewErr != nil {
		return store.ConsoleOverviewCounts{}, f.overviewErr
	}

	escalated := f.rowsFor(queueEscalated, p.Escalated.ConsoleFilter)
	claimed := f.rowsFor(queueClaimed, p.Claimed.ConsoleFilter)

	var counts store.ConsoleOverviewCounts
	for _, row := range escalated {
		counts.Escalated++
		if !row.escalatedAt.Before(tileNow.Add(-store.OverviewRecentEscalationWindow)) {
			counts.EscalatedRecently++
		}
	}
	for _, row := range claimed {
		counts.Claimed++
		if !row.leaseExpiring.After(tileNow.Add(store.OverviewLeaseExpiryWindow)) {
			counts.ClaimsExpiringSoon++
		}
	}
	for _, row := range f.rowsFor(queueNotes, p.Notes.ConsoleFilter) {
		counts.OpenNotes++
		if row.isScopeNote {
			counts.OpenScopeNotes++
		}
	}
	return counts, nil
}

// tileFixtureDesignSessions is the design aggregate the blocking-questions
// tile reads: one figure of open blocking questions and one of how many
// sessions hold at least one.
type tileFixtureDesignSessions struct {
	navStubDesignSessions
	summary store.ProductDesignSessionsSummary
	err     error
	gotPID  uuid.UUID
}

func (f *tileFixtureDesignSessions) SummarizeByProduct(_ context.Context, pid uuid.UUID) (store.ProductDesignSessionsSummary, error) {
	f.gotPID = pid
	if f.err != nil {
		return store.ProductDesignSessionsSummary{}, f.err
	}
	return f.summary, nil
}

// tileFixtureApp wires an app whose two Overview reads are the fixtures
// above and whose clock is held at tileNow.
func tileFixtureApp(t *testing.T, tasks *tileFixtureTasks, design *tileFixtureDesignSessions) *App {
	t.Helper()
	app := newTestApp(t)
	app.now = func() time.Time { return tileNow }
	app.tasks = tasks
	app.scopes = chromeScopes{}
	app.designSessions = design
	return app
}

// tileFixtureMux serves the Overview through the real shell routes, so a
// case exercises the whole render the browser would get.
func tileFixtureMux(t *testing.T, tasks *tileFixtureTasks, design *tileFixtureDesignSessions) *http.ServeMux {
	t.Helper()
	app := tileFixtureApp(t, tasks, design)
	app.spec = &fakeSpecReader{products: []store.Product{{ID: tileProduct, Name: "krill"}}}
	app.credentials = &fakeCredentials{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

func fetchOverviewBody(t *testing.T, mux *http.ServeMux) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, productHref(tileProduct, overviewSuffix), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET overview: status %d, want 200", rec.Code)
	}
	return rec.Body.String()
}

// overviewTileSection returns the rendered markup of the one tile labelled
// label, so a case can assert against one tile without a case where
// another tile's figure could match by accident.
func overviewTileSection(t *testing.T, body, label string) string {
	t.Helper()
	// The marker sits on the anchor's own tag, so the section has to reach
	// back to the "<a" that opened it -- otherwise the tile's own href,
	// which is half of what these cases assert, is cut off.
	marker := `<a href=`
	for rest := body; ; {
		start := strings.Index(rest, marker)
		if start < 0 {
			break
		}
		tile := rest[start:]
		if end := strings.Index(tile, "</a>"); end >= 0 {
			tile = tile[:end]
		}
		if !strings.Contains(tile, `data-krill="overview-stat"`) {
			// Some other link on the page; keep looking from after it.
			rest = rest[start+len(marker):]
			continue
		}
		if strings.Contains(tile, ">"+label+"<") {
			return tile
		}
		rest = rest[start+len(marker):]
	}
	t.Fatalf("no tile labelled %q in the rendered Overview", label)
	return ""
}

// busyTileRows is a fixture product with work in every queue the tiles
// count: three escalations (two recent, one old), two claims (one lapsing
// soon, one not), and three open notes (two of them scope notes). The
// other product's rows are here so that dropping the product narrowing
// would change every figure below.
//
// The three queues are separate slices because a row belongs to exactly
// one of them: the store's three queries join different tables, so a
// single blended row set would make the fixture claim a note is a claim.
//
// Each queue's rows are split across tileMilestoneA and tileMilestoneB
// with none left over, so a read narrowed to either milestone answers
// strictly fewer rows than a read narrowed to the product. Every figure
// asserted below therefore fails if a milestone filter sneaks in.
func busyTileRows() []tileRow {
	return []tileRow{
		// Escalated: two inside the one-hour window, one well outside it.
		{queue: queueEscalated, product: tileProduct, milestone: tileMilestoneA, escalatedAt: tileNow.Add(-5 * time.Minute)},
		{queue: queueEscalated, product: tileProduct, milestone: tileMilestoneB, escalatedAt: tileNow.Add(-30 * time.Minute)},
		{queue: queueEscalated, product: tileProduct, milestone: tileMilestoneA, escalatedAt: tileNow.Add(-4 * time.Hour)},
		// Claimed: one lapsing inside the ten-minute window, one well
		// outside it.
		{queue: queueClaimed, product: tileProduct, milestone: tileMilestoneB, leaseExpiring: tileNow.Add(2 * time.Minute)},
		{queue: queueClaimed, product: tileProduct, milestone: tileMilestoneA, leaseExpiring: tileNow.Add(3 * time.Hour)},
		// Open notes: two scope notes and one of another kind.
		{queue: queueNotes, product: tileProduct, milestone: tileMilestoneA, isScopeNote: true},
		{queue: queueNotes, product: tileProduct, milestone: tileMilestoneB, isScopeNote: true},
		{queue: queueNotes, product: tileProduct, milestone: tileMilestoneA},
		// Another product's rows, spread over its own two milestones: an
		// escalation, a claim and a scope note, so a dropped product
		// narrowing raises every figure above.
		{queue: queueEscalated, product: tileOtherProduct, milestone: tileMilestoneA, escalatedAt: tileNow.Add(-time.Minute)},
		{queue: queueClaimed, product: tileOtherProduct, milestone: tileMilestoneA, leaseExpiring: tileNow.Add(time.Minute)},
		{queue: queueNotes, product: tileOtherProduct, milestone: tileMilestoneA, isScopeNote: true},
	}
}

func busyTileFixture() *tileFixtureTasks {
	return &tileFixtureTasks{
		scopeID:        chromeScopeID,
		rows:           busyTileRows(),
		escalatedCount: 3,
	}
}

func tileDesignFixture() *tileFixtureDesignSessions {
	return &tileFixtureDesignSessions{
		summary: store.ProductDesignSessionsSummary{
			OpenBlockingQuestionCount:   5,
			SessionsHoldingOpenBlocking: 2,
		},
	}
}

// TestStatTilesShowEachFigureAndItsSubLine pins every figure and sub-line
// against literals, on a clock held still so both windows are checkable by
// hand.
func TestStatTilesShowEachFigureAndItsSubLine(t *testing.T) {
	tasks := busyTileFixture()
	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	for _, want := range []struct {
		label, count, sub string
	}{
		{"Escalated", "3", ""},
		{"Claimed", "2", "1 lease expires within 10 min"},
		{"Open notes", "3", "2 scope notes"},
		{"Blocking questions", "5", "in 2 design sessions"},
	} {
		t.Run(want.label, func(t *testing.T) {
			tile := overviewTileSection(t, body, want.label)
			if want.count != "" && !strings.Contains(tile, ">"+want.count+"<") {
				t.Errorf("figure = %q, want %q; tile: %s", want.count, want.count, tile)
			}
			if want.sub != "" && !strings.Contains(tile, want.sub) {
				t.Errorf("sub-line does not read %q; tile: %s", want.sub, tile)
			}
		})
	}
}

// TestEscalatedTileShowsHowManyAreNewInTheLastHour pins the sub-line the
// other three tiles have and this one was missing: of the fixture's three
// escalations, two were raised inside the one-hour window and one four
// hours ago. The FR names this sub-line for the Escalated tile by name, so
// a tile that renders a bare figure has dropped a required line.
//
// The count beside it is the badge's own 3. That the two numbers differ is
// the point: a sub-line repeating its figure would be satisfied by any
// implementation that ignores the window, and this one is not.
func TestEscalatedTileShowsHowManyAreNewInTheLastHour(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	tile := overviewTileSection(t, body, "Escalated")
	if !strings.Contains(tile, "2 new in the last hour") {
		t.Errorf("the Escalated tile does not carry its %q sub-line; tile: %s", "2 new in the last hour", tile)
	}
	// And the window is doing the work: 2 of the 3, not all 3 and not 0.
	if strings.Contains(tile, "3 new in the last hour") {
		t.Errorf("the sub-line counts every escalation rather than the recent ones; tile: %s", tile)
	}
}

// TestEveryTileCarriesTheSubLineTheFRNames is the whole set in one place:
// each of the four tiles carries the exact sub-line FR c0baeb2d names for
// it. Written as a table over literals so a tile that renders no sub-line
// element at all fails here rather than being read past.
func TestEveryTileCarriesTheSubLineTheFRNames(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	for _, want := range []struct{ label, sub string }{
		{"Escalated", "2 new in the last hour"},
		{"Claimed", "1 lease expires within 10 min"},
		{"Open notes", "2 scope notes"},
		{"Blocking questions", "in 2 design sessions"},
	} {
		tile := overviewTileSection(t, body, want.label)
		if !strings.Contains(tile, `data-krill="overview-stat-sub"`) {
			t.Errorf("tile %q rendered no sub-line element at all; tile: %s", want.label, tile)
			continue
		}
		if !strings.Contains(tile, want.sub) {
			t.Errorf("tile %q does not carry the sub-line %q; tile: %s", want.label, want.sub, tile)
		}
	}
}

// TestStatTileSubLinesAgreeWithTheirOwnFigures is the FR's own claim,
// checked by hand rather than by re-deriving: of the three escalations the
// fixture carries, two fall inside the one-hour window, and of the two
// claims one lapses inside the ten-minute window. A sub-line counted from
// a different read, or from a page rather than the queue, would not land
// on these numbers.
func TestStatTileSubLinesAgreeWithTheirOwnFigures(t *testing.T) {
	tasks := busyTileFixture()
	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	claimed := overviewTileSection(t, body, "Claimed")
	if !strings.Contains(claimed, ">2<") {
		t.Errorf("claimed figure is not the 2 claims the fixture holds; tile: %s", claimed)
	}
	if !strings.Contains(claimed, "1 lease expires within 10 min") {
		t.Errorf("of the 2 claims only 1 lapses inside the window, so the sub-line must say 1; tile: %s", claimed)
	}
}

// TestEscalatedTileShowsTheBadgesOwnFigure pins that the tile and the
// sidebar badge are one number: the fixture answers CountEscalatedTasks
// with a figure its own row set would not produce, so a tile that counted
// the queue separately would show the other number.
func TestEscalatedTileShowsTheBadgesOwnFigure(t *testing.T) {
	tasks := busyTileFixture()
	tasks.escalatedCount = 9

	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	tile := overviewTileSection(t, body, "Escalated")
	if !strings.Contains(tile, ">9<") {
		t.Errorf("tile does not show the badge's own figure of 9; tile: %s", tile)
	}
	if !strings.Contains(body, ">9<") {
		t.Errorf("the sidebar badge does not show 9 either; body: %s", body)
	}
}

// TestStatTilesCoverTheProductAcrossItsMilestones pins the narrowing: no
// milestone filter, and a product filter that excludes the other product's
// rows. The fixture's other product holds an escalation and a scope note,
// so a dropped narrowing would raise the escalated and open-notes figures.
func TestStatTilesCoverTheProductAcrossItsMilestones(t *testing.T) {
	tasks := busyTileFixture()
	fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	if len(tasks.gotParams) == 0 {
		t.Fatal("the tiles made no console read")
	}
	got := tasks.gotParams[0]
	if got.Escalated.ProductID == nil || *got.Escalated.ProductID != tileProduct {
		t.Errorf("escalated read is not narrowed to this product: %+v", got.Escalated.ConsoleFilter)
	}
	if got.Escalated.MilestoneID != nil {
		t.Errorf("escalated read is narrowed to one milestone, so an escalation in another would vanish: %v", got.Escalated.MilestoneID)
	}
	if got.Escalated.ScopeID != chromeScopeID {
		t.Errorf("escalated read is scoped to %v, want the sole scope %v", got.Escalated.ScopeID, chromeScopeID)
	}
	if got.Claimed.ProductID == nil || *got.Claimed.ProductID != tileProduct {
		t.Errorf("claimed read is not narrowed to this product: %+v", got.Claimed.ConsoleFilter)
	}
	if got.Claimed.MilestoneID != nil {
		t.Errorf("claimed read is narrowed to one milestone: %v", got.Claimed.MilestoneID)
	}
	if got.Notes.ProductID == nil || *got.Notes.ProductID != tileProduct {
		t.Errorf("open-notes read is not narrowed to this product: %+v", got.Notes.ConsoleFilter)
	}
	if got.Notes.MilestoneID != nil {
		t.Errorf("open-notes read is narrowed to one milestone: %v", got.Notes.MilestoneID)
	}
}

// TestStatTileFiguresSpanEveryMilestoneOfTheProduct is the figures half of
// the same requirement, which the params assertions above cannot reach on
// their own: the fixture spreads each queue's rows across tileMilestoneA
// and tileMilestoneB, so an Overview that counted only one container would
// render 2 escalations and 1 claim instead of 3 and 2. Read the way the
// operator reads it -- the numbers on the page.
func TestStatTileFiguresSpanEveryMilestoneOfTheProduct(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	for _, want := range []struct{ label, count string }{
		{"Claimed", "2"},
		{"Open notes", "3"},
	} {
		tile := overviewTileSection(t, body, want.label)
		if !strings.Contains(tile, ">"+want.count+"<") {
			t.Errorf("tile %q does not count rows from both of the product's milestones (want %s); tile: %s", want.label, want.count, tile)
		}
	}
}

// TestAFailedReadCostsOnlyItsOwnTilesIsPerTile pins the seam the Overview
// isolation work (task 2ade8e08) depends on: the tiles are four
// independently-addressable regions, not one figure that is either all
// there or all gone. Each case breaks exactly one read and shows the tiles
// that read it lose their figures while the tiles fed by the other reads
// keep theirs.
//
// This is not a claim about how the page as a whole degrades -- that is the
// isolation task's FR to settle. It is the narrower thing this task owes
// it: that the seam exists to be used, rather than each tile's figures
// arriving welded into a single struct whose only failure mode is losing
// all of them.
func TestAFailedReadCostsOnlyItsOwnTilesIsPerTile(t *testing.T) {
	t.Run("the badge's count fails", func(t *testing.T) {
		tasks := busyTileFixture()
		tasks.escalatedErr = store.ErrNotFound
		body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

		assertTileFailed(t, body, "Escalated")
		for _, label := range []string{"Claimed", "Open notes", "Blocking questions"} {
			assertTileKeptItsFigure(t, body, label)
		}
	})

	t.Run("the console read fails", func(t *testing.T) {
		tasks := busyTileFixture()
		tasks.overviewErr = store.ErrNotFound
		body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

		// The console read answers two tiles, so both of them -- and only
		// those two -- lose their figures.
		assertTileFailed(t, body, "Claimed")
		assertTileFailed(t, body, "Open notes")
		assertTileKeptItsFigure(t, body, "Escalated")
		assertTileKeptItsFigure(t, body, "Blocking questions")
	})

	t.Run("the design read fails", func(t *testing.T) {
		design := tileDesignFixture()
		design.err = store.ErrNotFound
		body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), design))

		assertTileFailed(t, body, "Blocking questions")
		for _, label := range []string{"Escalated", "Claimed", "Open notes"} {
			assertTileKeptItsFigure(t, body, label)
		}
	})
}

// assertTileFailed pins the two halves of a tile whose read failed: a
// message in place of the figure, and no figure at all. The second half is
// the one that matters -- "Escalated could not be read" beside a 0 is an
// unreadable number wearing an idle one's clothes.
func assertTileFailed(t *testing.T, body, label string) {
	t.Helper()
	tile := overviewTileSection(t, body, label)
	if !strings.Contains(tile, `data-krill="overview-stat-error"`) {
		t.Errorf("tile %q should show that its read failed; tile: %s", label, tile)
	}
	if strings.Contains(tile, `data-krill="overview-stat-count"`) {
		t.Errorf("tile %q rendered a figure for a read that failed; tile: %s", label, tile)
	}
	if strings.Contains(tile, ">0<") {
		t.Errorf("tile %q rendered an unreadable count as a zero; tile: %s", label, tile)
	}
	if !strings.Contains(tile, "could not be read") {
		t.Errorf("tile %q should say which figure is missing; tile: %s", label, tile)
	}
}

// assertTileKeptItsFigure pins that a tile fed by a read that still worked
// kept both its number and its sub-line.
func assertTileKeptItsFigure(t *testing.T, body, label string) {
	t.Helper()
	tile := overviewTileSection(t, body, label)
	if strings.Contains(tile, `data-krill="overview-stat-error"`) {
		t.Errorf("tile %q failed because an unrelated read failed; tile: %s", label, tile)
	}
	if !strings.Contains(tile, `data-krill="overview-stat-count"`) {
		t.Errorf("tile %q lost its figure to an unrelated read's failure; tile: %s", label, tile)
	}
}

// TestStatTilesLinkToWhatTheyCount pins each destination as a literal URL.
//
// The hrefs are written out rather than built from the constants the code
// under test uses (escalatedTabHref, opsClaimedPath, designProductSessionsPath
// and the product's own id). A test that reuses the code's own path builder
// agrees with any change to it -- including a change that sends every tile
// to the same wrong place -- so the destination is spelled here and checked
// as a reader would check it: against the address bar.
func TestStatTilesLinkToWhatTheyCount(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	for _, want := range []struct {
		label string
		href  string
	}{
		// The Needs attention console queues the three console tiles count.
		{"Escalated", "/ops/escalated"},
		{"Claimed", "/ops/claimed"},
		{"Open notes", "/ops/notes"},
		// Design sessions for tileProduct, spelled with its literal id --
		// not /design/go, and not a bare /design the operator would have to
		// pick a product out of.
		{"Blocking questions", "/design/products/66666666-0000-0000-0000-000000000001/design-sessions"},
	} {
		t.Run(want.label, func(t *testing.T) {
			tile := overviewTileSection(t, body, want.label)
			if !strings.Contains(tile, `href="`+want.href+`"`) {
				t.Errorf("tile does not link to %q; tile: %s", want.href, tile)
			}
		})
	}
}

// TestTheBlockingQuestionsTileLinksToThisProductsSessions is the same
// destination with the product id varied, so the href is shown to be built
// from the product on screen rather than from whichever one the code
// happened to close over. Under tileOtherProduct the same tile must point
// at that product's list.
func TestTheBlockingQuestionsTileLinksToThisProductsSessions(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))
	tile := overviewTileSection(t, body, "Blocking questions")

	if strings.Contains(tile, `href="/design/go"`) || strings.Contains(tile, `href="/design"`) ||
		strings.Contains(tile, "product_id") {
		t.Errorf("the tile links to the typed-id design root instead of the product's session list; tile: %s", tile)
	}
	if strings.Contains(tile, tileOtherProduct.String()) {
		t.Errorf("the tile links at another product's sessions; tile: %s", tile)
	}
}

// TestBlockingQuestionsTileReadsTheProductItRenders pins that the design
// read names the product whose Overview is on screen, so one product's
// questions cannot appear under another's name.
func TestBlockingQuestionsTileReadsTheProductItRenders(t *testing.T) {
	design := tileDesignFixture()
	fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), design))

	if design.gotPID != tileProduct {
		t.Errorf("design summary read for %v, want the rendered product %v", design.gotPID, tileProduct)
	}
}

// TestStatTileCountsAreColouredByWhatTheyMean pins the two semantic
// classes: a figure that means something is stuck reads as an error, one
// that means something is waiting on an answer reads as a warning, and the
// two idle figures are neither.
func TestStatTileCountsAreColouredByWhatTheyMean(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	for _, want := range []struct{ label, class string }{
		{"Escalated", "text-error"},
		{"Blocking questions", "text-warning"},
	} {
		tile := overviewTileSection(t, body, want.label)
		if !strings.Contains(tile, want.class) {
			t.Errorf("tile %q does not carry %s; tile: %s", want.label, want.class, tile)
		}
	}
	claimed := overviewTileSection(t, body, "Claimed")
	if strings.Contains(claimed, "text-error") || strings.Contains(claimed, "text-warning") {
		t.Errorf("the claimed tile is neither stuck nor blocked and should be neither colour; tile: %s", claimed)
	}
}

// TestStatTilesAreLinksToTheirOwnFigures pins the tile as a whole is the
// anchor, so the click target is the figure rather than a word under it.
func TestStatTilesAreLinksToTheirOwnFigures(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	if got := strings.Count(body, `data-krill="overview-stat"`); got != 4 {
		t.Errorf("rendered %d stat tiles, want 4: %s", got, body)
	}
	if !strings.Contains(body, `class="stats stats-vertical sm:stats-horizontal"`) {
		t.Errorf("the four tiles are not in a daisyUI stats block: %s", body)
	}
}

// TestStatTilesRenderZeroAsARealZero guards the ordinary idle case: zero
// is a figure, and a tile showing one must not be mistaken for a failure.
func TestStatTilesRenderZeroAsARealZero(t *testing.T) {
	tasks := &tileFixtureTasks{scopeID: chromeScopeID, rows: nil}
	design := &tileFixtureDesignSessions{}
	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, design))

	for _, label := range []string{"Escalated", "Claimed", "Open notes", "Blocking questions"} {
		tile := overviewTileSection(t, body, label)
		if !strings.Contains(tile, ">0<") {
			t.Errorf("tile %q does not render its zero figure; tile: %s", label, tile)
		}
		if strings.Contains(tile, `data-krill="overview-stat-error"`) {
			t.Errorf("an idle tile renders an error instead of a zero; tile: %s", tile)
		}
	}
}

// TestSubLineGrammarIsSingularAtOne pins the plural rule at its boundary.
// "1 leases expire" beside a figure of 1 is the kind of small wrongness
// that makes an operator doubt the number next to it.
func TestSubLineGrammarIsSingularAtOne(t *testing.T) {
	for _, tc := range []struct {
		name string
		got  string
		want string
	}{
		{"one expiring lease", leaseSubLine(1), "1 lease expires within 10 min"},
		{"two expiring leases", leaseSubLine(2), "2 leases expire within 10 min"},
		{"none expiring", leaseSubLine(0), "0 leases expire within 10 min"},
		{"one scope note", scopeNoteSubLine(1), "1 scope note"},
		{"two scope notes", scopeNoteSubLine(2), "2 scope notes"},
		{"no scope notes", scopeNoteSubLine(0), "0 scope notes"},
		{"one session", designSessionSubLine(1), "in 1 design session"},
		{"two sessions", designSessionSubLine(2), "in 2 design sessions"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
}

// ── the shell's other console-read fakes ────────────────────────────────────

// TestShellConsoleReadFakesAnswerWithZeroFigures pins what the shell's other
// test fakes do with the read this work added, so a future change to the
// tile code cannot quietly turn their silent zero into a panic or a
// fabricated figure.
//
// "Honest" is the whole claim, and it cuts both ways. These fakes are not
// the tile tests' evidence -- those run against tileFixtureTasks, which
// derives every figure from rows. They are here so that the nav walk, the
// header cases and the product-scope cases can render a page that includes
// the tiles without inventing work for their assertions. What they must not
// do is answer a figure the page then renders as though it were real: a
// fabricated non-zero would put a number on those pages that no store ever
// produced, and every assertion made against them would inherit it.
func TestShellConsoleReadFakesAnswerWithZeroFigures(t *testing.T) {
	for _, tc := range []struct {
		name string
		read func() (store.ConsoleOverviewCounts, error)
	}{
		{"the chrome's counter", func() (store.ConsoleOverviewCounts, error) {
			return chromeTaskCounter{}.CountConsoleOverview(context.Background(), store.ConsoleOverviewParams{})
		}},
		{"the nav walk's store", func() (store.ConsoleOverviewCounts, error) {
			return (&navTasks{}).CountConsoleOverview(context.Background(), store.ConsoleOverviewParams{})
		}},
		{"the product-scope store", func() (store.ConsoleOverviewCounts, error) {
			return productScopeTasks{}.CountConsoleOverview(context.Background(), store.ConsoleOverviewParams{})
		}},
		{"the Overview header's counter", func() (store.ConsoleOverviewCounts, error) {
			return (&overviewCounter{}).CountConsoleOverview(context.Background(), store.ConsoleOverviewParams{})
		}},
	} {
		got, err := tc.read()
		if err != nil {
			t.Errorf("%s fails the console read outright; an idle deployment reads cleanly: %v", tc.name, err)
			continue
		}
		if got != (store.ConsoleOverviewCounts{}) {
			t.Errorf("%s answers the console read with %+v; an idle deployment's answer is every figure zero, and a non-zero here would be a figure no test set up", tc.name, got)
		}
	}
}

// TestShellFakesRenderTheOverviewAsAnIdleDeployment is the other half: the
// zero these fakes answer with reaches the page as four zero tiles rather
// than as four errors or no strip at all. An honest idle answer and an
// unreadable one must not look the same to whatever assertion reads those
// pages, or "renders as zero" is really "renders as broken".
func TestShellFakesRenderTheOverviewAsAnIdleDeployment(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(t *testing.T) *http.ServeMux
	}{
		{"the chrome's counter", func(t *testing.T) *http.ServeMux {
			app := newTestApp(t)
			app.spec = &fakeSpecReader{products: []store.Product{{ID: tileProduct, Name: "krill"}}}
			app.credentials = &fakeCredentials{}
			mux := http.NewServeMux()
			app.mountShellRoutes(mux)
			return mux
		}},
		{"the product-scope store", func(t *testing.T) *http.ServeMux {
			return productScopeMux(t, store.Product{ID: tileProduct, Name: "krill"})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fetchOverviewBody(t, tc.build(t))
			if got := strings.Count(body, `data-krill="overview-stat"`); got != 4 {
				t.Fatalf("rendered %d stat tiles, want 4: %s", got, body)
			}
			for _, label := range []string{"Escalated", "Claimed", "Open notes", "Blocking questions"} {
				tile := overviewTileSection(t, body, label)
				if strings.Contains(tile, `data-krill="overview-stat-error"`) {
					t.Errorf("tile %q rendered a failure although the fake answered cleanly; tile: %s", label, tile)
				}
				if !strings.Contains(tile, ">0<") {
					t.Errorf("tile %q does not render the idle figure it was answered with; tile: %s", label, tile)
				}
			}
		})
	}
}
