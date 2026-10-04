// Wire-driven coverage for the DESIGN SESSIONS table (FR b1f50086): the
// rows it renders, the order, the derived stage and blocking-question count
// each row shows, the empty state, and the two URLs that serve it -- the
// product-scoped one and the un-prefixed design root that resolves the
// product itself.
//
// Every assertion is against served HTML rather than a view model, so a
// builder that fills a field correctly and a template that never renders it
// both fail here. The stage badge and the blocking count are read back
// THROUGH components.DesignSessionStageStyle/Label and
// components.DesignSessionBlockingCountStyle rather than against literal
// class strings, so this file pins the mapper chain rather than the
// palette: a deliberate colour change lands in components/status.go and
// its own test, not as a silent break in every caller.
package main

import (
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/components"
	"github.com/whale-net/everything/krill/ui/pages"
)

// designListNow is the instant this file's sessions are measured back
// from, truncated to the second so the RFC3339 strings the <time> carries
// are exactly the ones the fixture states.
//
// It tracks the wall clock rather than being a fixed date, because the
// served-HTML cases assert the Opened column's relative wording and the
// handler measures against its own now(). Every age here sits well inside
// one relativeTime bucket with margin on both sides, so the few
// milliseconds between this and the handler's clock cannot move the
// wording. The builder case below hands this same instant in explicitly,
// which is what makes that one exact rather than merely stable.
var designListNow = time.Now().UTC().Truncate(time.Second)

// designListSessions is the aggregate a sessions-list case drives, in the
// order SummarizeByProduct returns it -- which is what the table must
// render in, unmodified. The literal ids let a failure name the same rows
// every run.
//
// Deliberately NOT alphabetical by opening request, by created_at, or by
// stage: a table that re-sorted its rows could otherwise agree with the
// read by accident.
func designListSessions(productID uuid.UUID) []store.DesignSessionSummary {
	return []store.DesignSessionSummary{
		// Newest, and mid-list by stage: an in-draft session holding two
		// blocking questions.
		designSummary(
			uuid.MustParse("33333333-3333-3333-3333-333333333333"), productID,
			"Facelift the operator UI onto a workspace shell\n\nThe second paragraph never reaches the list.",
			store.StageInDraft, designListNow.Add(-90*time.Minute), 2),
		// Middle, and the only approved one: the empty blocking cell.
		designSummary(
			uuid.MustParse("22222222-2222-2222-2222-222222222222"), productID,
			"Escalation console interventions",
			store.StageApproved, designListNow.Add(-14*24*time.Hour), 0),
		// Oldest, and never had a revision event: the "opened" stage.
		designSummary(
			uuid.MustParse("11111111-1111-1111-1111-111111111111"), productID,
			"  Work axis: claim and lease\n",
			store.StageOpened, designListNow.Add(-35*24*time.Hour), 1),
	}
}

// designListApp is an App whose session list reads the aggregate above and
// whose scope resolves exactly one product -- so the un-prefixed /design
// root has a product to resolve.
func designListApp(productID uuid.UUID) *App {
	app := newDesignReadApp(
		fakeDesignSessions{summaries: designSummaries(productID, designListSessions(productID)...)},
		fakeRevisionEvents{},
	)
	app.spec = scopedProductsReader{products: []store.Product{{ID: productID, Name: "krill"}}}
	return app
}

// renderedDesignRow is one row of the rendered table, parsed back out so
// the columns can be compared field by field.
type renderedDesignRow struct {
	ID            string
	DetailPath    string
	OpeningText   string
	StageLabel    string
	BlockingCell  string
	OpenedText    string
	OpenedAt      string
	OpenedAtTitle string
}

// reDesignRow keys on the row's own stable data-krill hook and captures
// the four cells in column order. The stage and blocking cells are matched
// as markup because that is where the badge's class lives -- the assertion
// below reads that class back through the mapper rather than against a
// literal.
var reDesignRow = regexp.MustCompile(
	`data-krill-design-session-id="([^"]+)">(.*?)</td>\s*<td>(.*?)</td>\s*<td class="text-right">(.*?)</td>\s*<td>(.*?)</td>\s*</tr>`)

var reDesignCell = regexp.MustCompile(`<a href="([^"]+)"[^>]*>(.*?)</a>`)
var reTimeCell = regexp.MustCompile(`datetime="([^"]+)"\s+title="([^"]+)"[^>]*>(.*?)</time>`)

func parseDesignRows(t *testing.T, body string) []renderedDesignRow {
	t.Helper()
	region := pageSection(t, body, regionSessions, regionSessionsEnd)
	var rows []renderedDesignRow
	for _, m := range reDesignRow.FindAllStringSubmatch(region, -1) {
		link := reDesignCell.FindStringSubmatch(m[2])
		require.Len(t, link, 3, "the opening-request cell must carry one link: %q", m[2])
		at := reTimeCell.FindStringSubmatch(m[5])
		require.Len(t, at, 4, "the Opened cell must carry a <time> with datetime and title: %q", m[5])
		rows = append(rows, renderedDesignRow{
			ID:            m[1],
			DetailPath:    link[1],
			OpeningText:   strings.TrimSpace(link[2]),
			StageLabel:    strings.TrimSpace(stripTags(m[3])),
			BlockingCell:  strings.TrimSpace(stripTags(m[4])),
			OpenedText:    strings.TrimSpace(at[3]),
			OpenedAt:      at[1],
			OpenedAtTitle: at[2],
		})
	}
	return rows
}

// stripTags removes the tags in one cell so its text can be compared, and
// leaves the badge's class attribute behind for badgeVariant to read.
func stripTags(s string) string {
	return regexp.MustCompile(`<[^>]*>`).ReplaceAllString(s, "")
}

// TestDesignSessionList_RendersTheAggregateInReadOrder is the table's
// central claim (FR b1f50086): the product's sessions, newest first,
// exactly as the aggregate read returned them, each row a working link.
func TestDesignSessionList_RendersTheAggregateInReadOrder(t *testing.T) {
	productID := uuid.New()
	sessions := designListSessions(productID)

	rec := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := parseDesignRows(t, rec.Body.String())
	require.Len(t, rows, len(sessions), "one row per session the read returned, no more, no fewer")

	for i, want := range sessions {
		got := rows[i]
		where := want.OpeningSubmission
		assert.Equal(t, want.ID.String(), got.ID, "%s: rows must render in the read's own order", where)
		assert.Equal(t, designSessionPath(productID, want.ID), got.DetailPath,
			"%s: the row must link at the canonical, product-scoped detail URL", where)
	}
}

// TestDesignSessionList_OpeningRequestIsTheFirstLineOnly is FR b1f50086's
// "first line": the list shows what a session is about, not its whole
// opening submission -- which stays the detail page's subject.
func TestDesignSessionList_OpeningRequestIsTheFirstLineOnly(t *testing.T) {
	productID := uuid.New()
	rec := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := parseDesignRows(t, rec.Body.String())
	require.Len(t, rows, 3)

	assert.Equal(t, "Facelift the operator UI onto a workspace shell", rows[0].OpeningText,
		"a multi-line submission shows its first line alone")
	assert.NotContains(t, rows[0].OpeningText, "second paragraph")

	assert.Equal(t, "Work axis: claim and lease", rows[2].OpeningText,
		"a submission that opens with whitespace still shows its first line, trimmed")
}

// TestDesignSessionList_StageBadgeResolvesThroughTheMapper pins the stage
// cell to components.DesignSessionStageStyle/Label for every wire value
// the aggregate can carry, plus an unknown one -- so a stage can never
// render as a blank badge, and the list and the detail's own header cannot
// disagree about what a stage looks like.
func TestDesignSessionList_StageBadgeResolvesThroughTheMapper(t *testing.T) {
	for _, stage := range []store.Stage{
		store.StageOpened,
		store.StageApproved,
		store.StageChangesRequested,
		store.StageArchitectReview,
		store.StageInDraft,
		store.StageAnswered,
		store.StageRuled,
		"a stage this build does not know",
	} {
		t.Run(string(stage), func(t *testing.T) {
			productID := uuid.New()
			app := newDesignReadApp(
				fakeDesignSessions{summaries: designSummaries(productID, designSummary(
					uuid.New(), productID, "a request", stage, designListNow.Add(-time.Hour), 0))},
				fakeRevisionEvents{},
			)
			rec := get(designReadMux(app), designProductSessionsPath(productID))
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

			rows := parseDesignRows(t, rec.Body.String())
			require.Len(t, rows, 1)

			assert.Equal(t, components.DesignSessionStageLabel(string(stage)), rows[0].StageLabel,
				"the badge's wording is the mapper's, not the wire value")
			assertStageBadgeMatchesMapper(t, rec.Body.String(), stage)
			assert.NotEmpty(t, rows[0].StageLabel, "a stage badge is never blank, known or not")
		})
	}
}

// assertStageBadgeMatchesMapper renders the mapper's own tuple and checks
// the served cell carries exactly that variant and soft treatment. The
// comparison is against components.DesignSessionStageStyle rather than a
// class literal, so this fails only if the call site stopped going through
// the mapper.
func assertStageBadgeMatchesMapper(t *testing.T, body string, stage store.Stage) {
	t.Helper()
	want := components.DesignSessionStageStyle(string(stage))
	cell := regexp.MustCompile(`class="([^"]*)"[^>]*data-krill="design-session-stage"`).FindStringSubmatch(body)
	require.Len(t, cell, 2, "the stage cell must carry the mapper-owned badge: %s", body)
	assertBadgeMatches(t, cell[1], want, "the stage badge's colour must be the mapper's")
}

// assertBadgeMatches is the shared assertion every badge on this page goes
// through: the rendered class set is compared against the mapper's own
// (variant, size, soft) tuple token by token. Nothing here names a colour,
// so a deliberate palette change lands in components/status.go and its own
// test rather than breaking every caller at once.
func assertBadgeMatches(t *testing.T, class string, want components.StatusStyle, where string) {
	t.Helper()
	// htmxui's Variant and Size already carry the badge- prefix, so the
	// mapper's tuple names the classes verbatim and nothing here has to
	// know how a badge's classes are spelled.
	tokens := strings.Fields(class)
	assert.Contains(t, tokens, string(want.Variant), "%s (got %q)", where, class)
	assert.Contains(t, tokens, string(want.Size), "%s (got %q)", where, class)
	assert.Equal(t, want.Soft, slices.Contains(tokens, "badge-soft"), "%s (got %q)", where, class)

	// Exactly one colour class: a badge carrying two has picked its own
	// answer rather than taking the mapper's.
	var colours []string
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "badge-") && tok != "badge-soft" && tok != string(want.Size) {
			colours = append(colours, tok)
		}
	}
	assert.Equal(t, []string{string(want.Variant)}, colours,
		"%s: the badge's only colour class must be the mapper's (got %q)", where, class)
}

// TestDesignSessionList_BlockingCellReadsThroughTheMapper is FR
// b1f50086's Open questions column: an error-variant "N blocking" badge
// when the session owes a blocking answer, a plain 0 when it owes none.
// The two are separate answers and neither borrows the other's badge.
func TestDesignSessionList_BlockingCellReadsThroughTheMapper(t *testing.T) {
	productID := uuid.New()
	rec := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	rows := parseDesignRows(t, body)
	require.Len(t, rows, 3)

	// The row the aggregate gave two blocking questions.
	assert.Equal(t, "2 blocking", rows[0].BlockingCell)
	// ...and the one it gave none.
	assert.Equal(t, "0", rows[1].BlockingCell,
		"a session owing nothing reads as a plain 0, not a badge")
	assert.NotContains(t, rows[1].BlockingCell, "blocking",
		"the zero cell must not read as though something were owed")

	want := components.DesignSessionBlockingCountStyle()
	badge := regexp.MustCompile(`class="([^"]*)"[^>]*data-krill="design-session-blocking"`).FindStringSubmatch(body)
	require.Len(t, badge, 2, "the blocking cell must carry the mapper-owned badge")
	assertBadgeMatches(t, badge[1], want, "the blocking badge")
}

// TestDesignSessionList_OpenedCellCarriesRelativeAndExact is FR b1f50086's
// Opened column: the relative age is what renders, and the exact instant
// rides in the datetime and the title so hovering answers "exactly when"
// with or without the head script.
func TestDesignSessionList_OpenedCellCarriesRelativeAndExact(t *testing.T) {
	productID := uuid.New()
	rec := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	rows := parseDesignRows(t, rec.Body.String())
	require.Len(t, rows, 3)

	// Each age is mid-bucket by a wide margin (90 min inside the hour
	// bucket, 14 days well inside the days bucket), so the wording is
	// package main's own relativeTime and not a wall-clock coincidence.
	want := []struct{ relative, exact string }{
		{"1 h ago", designListNow.Add(-90 * time.Minute).Format(time.RFC3339)},
		{"14 days ago", designListNow.Add(-14 * 24 * time.Hour).Format(time.RFC3339)},
		{"35 days ago", designListNow.Add(-35 * 24 * time.Hour).Format(time.RFC3339)},
	}
	for i, w := range want {
		where := rows[i].ID
		assert.Equal(t, w.relative, rows[i].OpenedText, "%s: the visible age is package main's own relative wording", where)
		assert.Equal(t, w.exact, rows[i].OpenedAt, "%s: datetime carries the exact instant", where)
		assert.Equal(t, w.exact, rows[i].OpenedAtTitle, "%s: title carries the same instant, so hover works without JavaScript", where)
	}

	assert.Contains(t, rec.Body.String(), "data-krill-updated-at=",
		"the <time> carries the data-krill-updated-at hook the status register and task detail share")
}

// TestDesignSessionList_EmptyState is the htmxui.EmptyState case (FR
// b1f50086): a product with no sessions is a deliberate empty state, not
// an error, and it says so in the words the operator already reads -- and
// offers the one action that opens a session.
func TestDesignSessionList_EmptyState(t *testing.T) {
	productID := uuid.New()
	app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})

	rec := get(designReadMux(app), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	body := rec.Body.String()
	assert.Contains(t, body, "No design sessions for this product yet.")
	assert.NotContains(t, body, "<table", "an empty product renders the empty state, not an empty table")
	assert.Contains(t, body, "New design session",
		"the empty state offers the one action that opens a session")
}

// TestDesignRoot_ServesTheResolvedProductsList is the design root's whole
// job now (FR c4bd4bf8, b1f50086): /design names no product, so it
// resolves one and serves that product's Design sessions table -- one page
// at two URLs, and never a form asking the operator to type an id.
func TestDesignRoot_ServesTheResolvedProductsList(t *testing.T) {
	productID := uuid.New()

	rec := get(designReadMux(designListApp(productID)), designPath)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()

	// The same table the product-scoped URL serves, for the resolved
	// product: the same rows, at the same canonical detail URLs.
	rows := parseDesignRows(t, body)
	require.Len(t, rows, 3, "/design must serve the resolved product's sessions, not a landing page")
	assert.Equal(t, designSessionPath(productID, designListSessions(productID)[0].ID), rows[0].DetailPath)
	assert.Contains(t, body, "Facelift the operator UI onto a workspace shell")

	// Nothing in the page asks the operator for an id any more. Scoped to
	// the page body rather than the whole document: the shell's own theme
	// bootstrap is a <script>, and it was always there.
	region := pageSection(t, body, regionSessions, regionSessionsEnd)
	assert.NotContains(t, region, `name="product_id"`, "no shell page asks the operator for a product id")
	assert.NotContains(t, region, "<script", "the server owns the resolution")
	assert.Contains(t, body, "<main", "the root serves the page inside the shell, not a bare fragment")
}

// TestDesignSessionList_ErrorPaths covers the list's own failure modes: a
// malformed product id, a failed read, and an unknown product (which is an
// empty list, not an error -- the URL resolved, there is just nothing
// behind it).
func TestDesignSessionList_ErrorPaths(t *testing.T) {
	t.Run("malformed product id is 400", func(t *testing.T) {
		app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
		rec := get(designReadMux(app), "/design/products/not-a-uuid/design-sessions")
		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
	})

	t.Run("a failed read is 500 with no store text", func(t *testing.T) {
		boom := fmt.Errorf("pq: password authentication failed for user krill")
		app := newDesignReadApp(fakeDesignSessions{err: boom}, fakeRevisionEvents{})
		rec := get(designReadMux(app), designProductSessionsPath(uuid.New()))
		assert.Equal(t, http.StatusInternalServerError, rec.Code)
		assert.NotEmpty(t, strings.TrimSpace(rec.Body.String()))
		assert.NotContains(t, rec.Body.String(), "password authentication",
			"store error text must not reach the browser")
	})

	t.Run("an unknown product is an empty list, not an error", func(t *testing.T) {
		app := newDesignReadApp(fakeDesignSessions{}, fakeRevisionEvents{})
		rec := get(designReadMux(app), designProductSessionsPath(uuid.New()))
		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), "No design sessions")
	})
}

// TestDesignSessionList_PageCarriesTheRegionIdTheTestsSlice pins the one
// thing every assertion in this file depends on: the region id. It is a
// test rather than a comment because a renamed region would make every
// other case in this file vacuously empty.
func TestDesignSessionList_PageCarriesTheRegionIdTheTestsSlice(t *testing.T) {
	rec := get(designReadMux(designListApp(uuid.New())), designProductSessionsPath(uuid.New()))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), regionSessions,
		"the sessions region id is what these tests slice on")
}

// TestDesignSessionList_BadgesAreNeverHandRolled is the cross-cutting
// FR 21f5f0d0 / dcecb049 check for this page: every badge here resolves
// through a components mapper, and no daisyUI colour class is spelled at a
// call site. Asserted as an absence of hand-written badge markup rather
// than as a presence, so a future inline <span class="badge-..."> fails.
func TestDesignSessionList_BadgesAreNeverHandRolled(t *testing.T) {
	rec := get(designReadMux(designListApp(uuid.New())), designProductSessionsPath(uuid.New()))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()

	// Scoped to this page's own region: the shell's chrome legitimately
	// carries a <style> block for its daisyUI themes.
	region := pageSection(t, body, regionSessions, regionSessionsEnd)
	assert.NotContains(t, region, "<style", "no page carries hand-written CSS")
	assert.NotContains(t, region, "alert-", "the alert primitive is htmxui.Alert's, never a hand-rolled class")

	// Every badge the page renders carries one of the two mapper-owned
	// data-krill hooks. A badge with neither was styled at its call site,
	// which is what a second owner of a colour looks like.
	owned := []string{`data-krill="design-session-stage"`, `data-krill="design-session-blocking"`}
	for _, badge := range regexp.MustCompile(`<span class="badge[^"]*"[^>]*>`).FindAllString(region, -1) {
		var isOwned bool
		for _, hook := range owned {
			isOwned = isOwned || strings.Contains(badge, hook)
		}
		assert.True(t, isOwned, "a badge outside the mapper-owned hooks is hand-rolled: %s", badge)
	}
}

// TestDesignSessionList_RowIsAddressable proves the row carries its own
// session id, so a row can be checked against the session it claims to be
// -- which is what makes the order and link assertions above able to name
// a row at all.
func TestDesignSessionList_RowIsAddressable(t *testing.T) {
	productID := uuid.New()
	rec := get(designReadMux(designListApp(productID)), designProductSessionsPath(productID))
	require.Equal(t, http.StatusOK, rec.Code)

	got := map[string]bool{}
	for _, row := range parseDesignRows(t, rec.Body.String()) {
		got[row.ID] = true
	}
	for _, want := range designListSessions(productID) {
		assert.True(t, got[want.ID.String()],
			"every session the read returned has a row carrying its own id")
	}
}

// renderDesignSessionList is the view model the list builder produces,
// used here only to assert the shape the template consumes: the row
// carries the aggregate's stage and counts through, never a re-derivation.
func TestDesignSessionList_BuilderPassesTheAggregatesOwnValuesThrough(t *testing.T) {
	productID := uuid.New()
	app := newDesignReadApp(
		fakeDesignSessions{summaries: designSummaries(productID, designListSessions(productID)...)},
		fakeRevisionEvents{},
	)

	rows, err := app.designSessionRows(t.Context(), productID, designListNow)
	require.NoError(t, err)
	require.Len(t, rows, len(designListSessions(productID)))

	for i, want := range designListSessions(productID) {
		got := rows[i]
		assert.Equal(t, string(want.Stage), got.Stage, "the stage is the read's, not a re-derivation")
		assert.Equal(t, want.OpenBlockingQuestions, got.OpenBlockingQuestions,
			"the blocking count is the read's own")
		assert.Equal(t, firstLine(want.OpeningSubmission), got.OpeningRequest)
		assert.Equal(t, designSessionPath(productID, want.ID), got.DetailPath)
		assert.Equal(t, relativeTime(want.CreatedAt, designListNow), got.OpenedRelative)
		assert.Equal(t, want.CreatedAt.UTC().Format(time.RFC3339), got.OpenedExact)
	}

	// The page the rows render into names the product and its own URL, so
	// the template never rebuilds a path package main owns.
	assert.Equal(t, designProductSessionsPath(productID),
		pages.DesignSessionListPage{FormAction: designProductSessionsPath(productID)}.FormAction)
}
