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

func (f *tileFixtureTasks) rowsFor(queue tileQueue, filter store.ConsoleFilter) []tileRow {
	var out []tileRow
	for _, row := range f.rows {
		if row.queue != queue {
			continue
		}
		if filter.ProductID != nil && *filter.ProductID != row.product {
			continue
		}
		if filter.MilestoneID != nil {
			// No fixture row names a milestone, so any milestone narrowing
			// returns nothing rather than everything.
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
func busyTileRows() []tileRow {
	return []tileRow{
		// Escalated: two inside the one-hour window, one well outside it.
		{queue: queueEscalated, product: tileProduct, escalatedAt: tileNow.Add(-5 * time.Minute)},
		{queue: queueEscalated, product: tileProduct, escalatedAt: tileNow.Add(-30 * time.Minute)},
		{queue: queueEscalated, product: tileProduct, escalatedAt: tileNow.Add(-4 * time.Hour)},
		// Claimed: one lapsing inside the ten-minute window, one well
		// outside it.
		{queue: queueClaimed, product: tileProduct, leaseExpiring: tileNow.Add(2 * time.Minute)},
		{queue: queueClaimed, product: tileProduct, leaseExpiring: tileNow.Add(3 * time.Hour)},
		// Open notes: two scope notes and one of another kind.
		{queue: queueNotes, product: tileProduct, isScopeNote: true},
		{queue: queueNotes, product: tileProduct, isScopeNote: true},
		{queue: queueNotes, product: tileProduct},
		// Another product's rows: one escalation and one scope note, so
		// a dropped product narrowing raises two of the figures above.
		{queue: queueEscalated, product: tileOtherProduct, escalatedAt: tileNow.Add(-time.Minute)},
		{queue: queueNotes, product: tileOtherProduct, isScopeNote: true},
	}
}

func busyTileFixture() *tileFixtureTasks {
	return &tileFixtureTasks{
		scopeID:         chromeScopeID,
		rows:            busyTileRows(),
		escalatedCount: 3,
	}
}

func tileDesignFixture() *tileFixtureDesignSessions {
	return &tileFixtureDesignSessions{
		summary: store.ProductDesignSessionsSummary{
			OpenBlockingQuestionCount:    5,
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

// TestStatTilesLinkToWhatTheyCount pins each destination: the three
// console queues' own views, and the design-sessions list for this product
// rather than the typed-id design root.
func TestStatTilesLinkToWhatTheyCount(t *testing.T) {
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), tileDesignFixture()))

	for _, want := range []struct {
		label string
		href  string
	}{
		{"Escalated", escalatedTabHref},
		{"Claimed", opsClaimedPath},
		{"Open notes", opsNotesPath},
		{"Blocking questions", designProductSessionsPath(tileProduct)},
	} {
		t.Run(want.label, func(t *testing.T) {
			tile := overviewTileSection(t, body, want.label)
			if !strings.Contains(tile, `href="`+want.href+`"`) {
				t.Errorf("tile does not link to %q; tile: %s", want.href, tile)
			}
		})
	}

	tile := overviewTileSection(t, body, "Blocking questions")
	if strings.Contains(tile, "/design?") || strings.Contains(tile, "product_id") {
		t.Errorf("the blocking-questions tile links somewhere other than the product's session list; tile: %s", tile)
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

// TestAFailedConsoleReadCostsOnlyItsOwnTiles pins that one failed read
// takes down the figures it answered and no others -- the seam the
// per-region error work needs -- and that a queue that could not be
// counted never renders as an empty one.
func TestAFailedConsoleReadCostsOnlyItsOwnTiles(t *testing.T) {
	tasks := busyTileFixture()
	tasks.overviewErr = store.ErrNotFound
	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	for _, label := range []string{"Claimed", "Open notes"} {
		tile := overviewTileSection(t, body, label)
		if !strings.Contains(tile, `data-krill="overview-stat-error"`) {
			t.Errorf("tile %q should report that its read failed; tile: %s", label, tile)
		}
		if strings.Contains(tile, ">0<") {
			t.Errorf("tile %q rendered an unreadable count as a zero; tile: %s", label, tile)
		}
	}

	// The escalated figure came from the badge's own read and the
	// blocking figure from the design summary; neither was affected.
	escalated := overviewTileSection(t, body, "Escalated")
	if !strings.Contains(escalated, ">3<") {
		t.Errorf("the escalated tile lost its figure to an unrelated read's failure; tile: %s", escalated)
	}
	blocking := overviewTileSection(t, body, "Blocking questions")
	if !strings.Contains(blocking, ">5<") {
		t.Errorf("the blocking-questions tile lost its figure to an unrelated read's failure; tile: %s", blocking)
	}
}

// TestAFailedDesignReadFailsOnlyItsOwnTile is the same seam on the other
// read: a design store that is down must not cost the console figures.
func TestAFailedDesignReadFailsOnlyItsOwnTile(t *testing.T) {
	design := tileDesignFixture()
	design.err = store.ErrNotFound
	body := fetchOverviewBody(t, tileFixtureMux(t, busyTileFixture(), design))

	tile := overviewTileSection(t, body, "Blocking questions")
	if !strings.Contains(tile, `data-krill="overview-stat-error"`) {
		t.Errorf("the blocking-questions tile should report the failed read; tile: %s", tile)
	}
	if strings.Contains(tile, ">0<") {
		t.Errorf("an unreadable question count rendered as a zero; tile: %s", tile)
	}
	for _, label := range []string{"Escalated", "Claimed", "Open notes"} {
		if other := overviewTileSection(t, body, label); strings.Contains(other, `data-krill="overview-stat-error"`) {
			t.Errorf("tile %q failed because the design store did; tile: %s", label, other)
		}
	}
}

// TestAnUnreadableEscalatedCountFailsOnlyItsOwnTile is the badge's own
// failure reaching the tile. The chrome already renders no badge for an
// unreadable count; the tile must not resolve that ambiguity into a 0.
func TestAnUnreadableEscalatedCountFailsOnlyItsOwnTile(t *testing.T) {
	tasks := busyTileFixture()
	tasks.escalatedErr = store.ErrNotFound
	body := fetchOverviewBody(t, tileFixtureMux(t, tasks, tileDesignFixture()))

	tile := overviewTileSection(t, body, "Escalated")
	if !strings.Contains(tile, `data-krill="overview-stat-error"`) {
		t.Errorf("the escalated tile should report the failed read; tile: %s", tile)
	}
	if strings.Contains(tile, ">0<") {
		t.Errorf("an unreadable escalated count rendered as a zero; tile: %s", tile)
	}
	if other := overviewTileSection(t, body, "Claimed"); !strings.Contains(other, ">2<") {
		t.Errorf("the claimed figure should survive an unrelated read's failure; tile: %s", other)
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