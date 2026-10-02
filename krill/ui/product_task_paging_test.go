// Wire-driven coverage for the Tasks table's paging footer and its
// continuation-token guard (FR 7bff09fe): the explicit "Showing X of Y
// tasks", Next and Previous, the filters surviving a page move, and a token
// minted for a different scope or filter set rendering an inline error with
// a way back rather than an empty or wrong-scope page.
//
// The fixture here PAGES for real -- it applies store.ResolvePageSize and
// mints a genuine store.EncodeFilteredContinuationToken from the last row
// of each page -- because a fake that returned every row on every request
// would make "Showing X of Y" pass with Y == X forever, which is exactly
// the silently truncated table the FR rules out.
package main

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// pagedProductTasks is a stand-in for the real read that pages, so the
// footer's two numbers can disagree the way they do in production: X is one
// page, Y is everything behind it.
//
// It is deliberately NOT the real store -- that needs Docker, and the
// store's own paging is already pinned in its own integration tests. What
// this fixture must get RIGHT is the part the UI depends on, and it is
// three things:
//
//   - NextToken is present exactly when rows remain, because the UI takes
//     the existence of a following page from that and nothing else;
//   - the token it mints is a real store.EncodeFilteredContinuationToken
//     over the same FilterSet the read applied, so the store's own scope
//     and filter binding is what refuses a mismatched resume;
//   - CountProductTasks ignores paging, exactly as the real one does, so
//     "Y" is the whole filtered set and not the current page.
type pagedProductTasks struct {
	store.TaskStore

	rows  []store.ProductTaskRow
	scope uuid.UUID

	listed  []store.ListProductTasksParams
	counted []store.ListProductTasksParams

	// err, when set, is returned by the LIST rather than paged -- the
	// read-failure and token-refusal cases both enter through here.
	err error
}

func (s *pagedProductTasks) ListProductTasks(_ context.Context, params store.ListProductTasksParams) (store.Page[store.ProductTaskRow], error) {
	s.listed = append(s.listed, params)
	if s.err != nil {
		return store.Page[store.ProductTaskRow]{}, s.err
	}

	filtered := filterRows(s.rows, params)
	size := store.ResolvePageSize(params.Page.PageSize)

	// The same refusal the real read makes: a token whose bound scope or
	// filter set differs from this request's, or which is not a token at
	// all, never yields rows.
	start := 0
	if params.Page.ContinuationToken != "" {
		cursor, err := store.DecodeFilteredContinuationToken(params.ScopeID, params.FilterSet(), params.Page.ContinuationToken)
		if err != nil {
			return store.Page[store.ProductTaskRow]{}, err
		}
		after, err := uuid.Parse(cursor.SortKey)
		if err != nil {
			return store.Page[store.ProductTaskRow]{}, fmt.Errorf("%w: fixture sort key", store.ErrInvalidContinuationToken)
		}
		for i, row := range filtered {
			if row.TaskID == after {
				start = i + 1
				break
			}
		}
	}

	page := store.Page[store.ProductTaskRow]{Items: filtered[start:min(start+size, len(filtered))]}
	if next := start + size; next < len(filtered) {
		// The token names the last row of THIS page, exactly as the real
		// read's does -- the keyset position a resume starts after.
		last := page.Items[len(page.Items)-1]
		page.NextToken = store.EncodeFilteredContinuationToken(params.ScopeID, params.FilterSet(), store.Cursor{
			SortKey: last.TaskID.String(),
			ID:      last.TaskID,
		})
	}
	return page, nil
}

func (s *pagedProductTasks) CountProductTasks(_ context.Context, params store.ListProductTasksParams) (int, error) {
	s.counted = append(s.counted, params)
	if s.err != nil {
		return 0, s.err
	}
	// Paging is not part of the count, on either side: Y is the whole set.
	return len(filterRows(s.rows, params)), nil
}

// The chrome reads the shell makes on every page it renders.
func (pagedProductTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}
func (pagedProductTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, nil
}
func (pagedProductTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, nil
}
func (pagedProductTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, nil
}
func (pagedProductTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, nil
}
func (pagedProductTasks) CountConsoleOverview(context.Context, store.ConsoleOverviewParams) (store.ConsoleOverviewCounts, error) {
	return store.ConsoleOverviewCounts{}, nil
}
func (pagedProductTasks) SummarizeProductTaskProgress(_ context.Context, params store.ProductTaskProgressParams) (store.ProductTaskProgress, error) {
	return store.ProductTaskProgress{ProductID: params.ProductID}, nil
}

// pagedRowFixture is more tasks than fit one page at the page size these
// tests request, in the read's own order -- three per milestone so a page
// boundary lands mid-milestone, which is where a page that re-sorts or
// re-groups would show it.
func pagedRowFixture(n int) []store.ProductTaskRow {
	out := make([]store.ProductTaskRow, 0, n)
	for i := 0; i < n; i++ {
		out = append(out, store.ProductTaskRow{
			TaskID:      uuid.New(),
			Title:       fmt.Sprintf("Task %02d", i+1),
			CurrentLane: store.CanonicalLaneOrder[i%len(store.CanonicalLaneOrder)],
			State:       store.TaskStateActive,
			AttemptCap:  store.DefaultAttemptCap,
		})
	}
	return out
}

// pagedTaskMux mounts the real shell routes against the paging fixture.
func pagedTaskMux(t *testing.T, tasks store.TaskStore) *http.ServeMux {
	t.Helper()

	app := newTestApp(t)
	app.spec = &fakeSpecReader{
		products: []store.Product{
			{ID: productTaskProduct, Name: "krill"},
			{ID: productTaskOtherProduct, Name: "another product"},
		},
		listing: productTaskListing(),
	}
	app.tasks = tasks
	app.credentials = &fakeCredentials{}
	app.designSessions = navStubDesignSessions{}
	app.revisionEvents = navStubRevisionEvents{}

	mux := http.NewServeMux()
	app.mountShellRoutes(mux)
	return mux
}

// pagingSummary is the footer's sentence, read out of the rendered markup.
func pagingSummary(t *testing.T, body string) string {
	t.Helper()
	const marker = `data-krill="paging-summary">`
	at := strings.Index(body, marker)
	require.NotEqual(t, -1, at, "the table rendered no footer: %s", body)
	rest := body[at+len(marker):]
	end := strings.Index(rest, "<")
	require.NotEqual(t, -1, end)
	return strings.TrimSpace(rest[:end])
}

// pagingLinkHref is the href of the footer control carrying this marker,
// empty when the control is a disabled button rather than a link.
//
// The href is unescaped before it is returned: templ HTML-escapes an
// attribute value, so a URL carrying more than one query parameter arrives
// as "a&amp;b" and handing THAT to a request builder would produce a query
// with a parameter literally named "amp;b" -- a broken link that reads as a
// paging bug rather than as a test artefact.
func pagingLinkHref(body, marker string) string {
	at := strings.Index(body, marker)
	if at == -1 {
		return ""
	}
	rowStart := strings.LastIndex(body[:at], "<a ")
	rowEnd := strings.LastIndex(body[:at], "<button")
	if rowStart < rowEnd {
		return ""
	}
	rest := body[rowStart:]
	hrefAt := strings.Index(rest, `href="`)
	if hrefAt == -1 {
		return ""
	}
	rest = rest[hrefAt+len(`href="`):]
	return html.UnescapeString(rest[:strings.Index(rest, `"`)])
}

// pagingControlIsDisabled reports whether the control carrying this marker
// rendered as an inert <button disabled> rather than a live link.
//
// The two cases are read apart by which tag the marker sits inside rather
// than by looking for "disabled" anywhere near it, so a control that
// rendered as a live link with the word elsewhere on the page cannot pass
// as disabled.
func pagingControlIsDisabled(t *testing.T, body, marker string) bool {
	t.Helper()
	at := strings.Index(body, marker)
	require.NotEqual(t, -1, at, "the footer rendered no %s control: %s", marker, body)
	start := strings.LastIndex(body[:at], "<button")
	if start == -1 || strings.LastIndex(body[:at], "<a ") > start {
		return false
	}
	open := body[start:at]
	return strings.Contains(open, "disabled")
}

// TestTasksFooterNamesTheRealTotal is the FR's first claim, asserted
// against a fixture that has genuinely more tasks than fit on the page.
//
// The point of a real count is that it CANNOT be derived from the rows: X is
// this page and Y is every page. A footer that counted what it had would
// read "Showing 2 of 2" here, which is the silently truncated table FR
// 7bff09fe rules out -- so the assertion is that the two numbers differ,
// and that the second one is the count read for the same parameters the
// rows were read with.
func TestTasksFooterNamesTheRealTotal(t *testing.T) {
	const total = 7
	tasks := &pagedProductTasks{rows: pagedRowFixture(total)}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("page_size=2"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	require.Len(t, taskRowIDsIn(rec.Body.String()), 2, "the fixture has more tasks than the page size")
	assert.Equal(t, fmt.Sprintf("Showing %d of %d tasks", 2, total), pagingSummary(t, rec.Body.String()),
		"Y must be the count read for the same filters, not the rows on screen")

	// And the count really is a separate read of the same parameter set --
	// a Y that happened to equal len(rows) could also come from a UI that
	// estimated it, and this is what rules that out.
	require.Len(t, tasks.listed, 1)
	require.Len(t, tasks.counted, 1)
	assert.Equal(t, tasks.listed[0], tasks.counted[0],
		"the count is read with the identical params the rows were")
	assert.Equal(t, 2, tasks.listed[0].Page.PageSize, "the URL's page size reached the read")
}

// TestTasksNextIsEnabledOnlyWhileRowsRemain is the FR's second claim, in
// both directions, over a real walk to the last page.
//
// The store's Page.NextToken is the only thing consulted: the UI does not
// compare X against Y to decide whether a following page exists, because
// that comparison is a second opinion about a fact the store already
// answered. A page whose rows exactly fill the last page and one whose
// count is short of a full page must both end the walk.
func TestTasksNextIsEnabledOnlyWhileRowsRemain(t *testing.T) {
	for _, tc := range []struct {
		name     string
		total    int
		pageSize int
		// pages is how many pages the walk visits before the last one.
		pages int
	}{
		// 7 rows at 2 per page: 2, 2, 2, 1 -- the last page is short, so
		// Next must go by the token, not by "the page was full".
		{name: "a short final page ends the walk", total: 7, pageSize: 2, pages: 4},
		// 6 rows at 2 per page: 2, 2, 2 -- the final page is exactly full
		// and still has no following page, which is the case a
		// "page-was-full" heuristic gets wrong.
		{name: "an exactly-full final page ends the walk too", total: 6, pageSize: 2, pages: 3},
		{name: "one page of everything has no Next", total: 2, pageSize: 25, pages: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tasks := &pagedProductTasks{rows: pagedRowFixture(tc.total)}
			mux := pagedTaskMux(t, tasks)

			target := productTaskTasksURL(fmt.Sprintf("page_size=%d", tc.pageSize))
			seen := map[string]bool{}
			for page := 1; page <= tc.pages; page++ {
				rec := fetch(t, mux, target)
				require.Equal(t, http.StatusOK, rec.Code, "page %d: %s", page, rec.Body.String())
				body := rec.Body.String()

				require.False(t, seen[target], "the walk revisited a page, so it is not advancing")
				seen[target] = true

				rows := taskRowIDsIn(body)
				assert.Equal(t, fmt.Sprintf("Showing %d of %d tasks", len(rows), tc.total), pagingSummary(t, body),
					"page %d names the real total", page)

				if page == tc.pages {
					assert.True(t, pagingControlIsDisabled(t, body, `data-krill="paging-next"`),
						"the last page has no Next: %s", pagingSummary(t, body))
					continue
				}
				require.False(t, pagingControlIsDisabled(t, body, `data-krill="paging-next"`),
					"page %d of %d has rows behind it and must offer Next", page, tc.pages)
				target = pagingLinkHref(body, `data-krill="paging-next"`)
				require.NotEmpty(t, target, "an enabled Next must be a real link")
				assert.Contains(t, target, "page_token=",
					"Next is a page position, so it carries the store's token")
			}
		})
	}
}

// TestTasksPreviousIsDisabledAndNeverGuesses pins the other half of the
// second claim, and the reason it is always disabled.
//
// The store's keyset paging is forward-only: a token names a position to
// resume AFTER, so nothing in the read layer can name the position before
// the page being shown. A Previous that appeared anyway -- built from the
// current token, or from a row index -- would land the operator on a page
// they did not ask for and call it a position, which is the failure FR
// 7bff09fe exists to prevent.
//
// So the control is present and inert, and says why: absent on some pages
// and present on others, it would read as a rendering bug. The browser's
// own Back button is the way back, and it works because the pages are URLs.
func TestTasksPreviousIsDisabledAndNeverGuesses(t *testing.T) {
	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	first := fetch(t, mux, productTaskTasksURL("page_size=2"))
	require.Equal(t, http.StatusOK, first.Code)
	assert.True(t, pagingControlIsDisabled(t, first.Body.String(), `data-krill="paging-previous"`),
		"the first page has no page before it")
	assert.Empty(t, pagingLinkHref(first.Body.String(), `data-krill="paging-previous"`),
		"a disabled Previous must not still be an href to somewhere")

	// And on a page reached by a token it is STILL disabled, rather than
	// becoming a link whose target would have to be invented.
	next := pagingLinkHref(first.Body.String(), `data-krill="paging-next"`)
	require.NotEmpty(t, next, "sanity: there is a next page to go to")
	second := fetch(t, mux, next)
	require.Equal(t, http.StatusOK, second.Code)
	assert.NotEqual(t, taskRowIDsIn(first.Body.String()), taskRowIDsIn(second.Body.String()),
		"sanity: the second request really was a different page")
	assert.True(t, pagingControlIsDisabled(t, second.Body.String(), `data-krill="paging-previous"`),
		"a forward token names no preceding position, so Previous stays inert")
}

// TestTasksPageMoveKeepsEveryActiveFilter is the FR's third claim: moving
// between pages keeps scope mode, container id, lane, only-stuck and page
// size.
//
// Each of those rides in the URL because the link is rebuilt from the
// request's own query, so the risk is a parameter the rebuild forgot --
// which is why the case drives a URL carrying ALL of them at once and
// asserts each survives into the next page's own request to the store.
func TestTasksPageMoveKeepsEveryActiveFilter(t *testing.T) {
	// 20 rows, so the Testing lane alone still spans more than one page at
	// page_size=2: a filtered set that fits in one page would leave the
	// Next link absent and the case would assert nothing.
	tasks := &pagedProductTasks{rows: pagedRowFixture(20)}
	mux := pagedTaskMux(t, tasks)

	// Every filter at once, including the UI-only parent milestone param
	// that never reaches the store but is still state the control renders.
	query := url.Values{
		"scope":       {string(store.ProductTaskScopeMilepebble)},
		"container_id": {productTaskMilepebble.String()},
		"milestone":   {productTaskMilestone.String()},
		"lane":        {string(store.LaneTesting)},
		"only_stuck":  {"false"},
		"page_size":   {"2"},
	}
	first := fetch(t, mux, productTaskTasksURL(query.Encode()))
	require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())

	next := pagingLinkHref(first.Body.String(), `data-krill="paging-next"`)
	require.NotEmpty(t, next, "the fixture has rows behind this page")
	nextQuery, err := url.ParseQuery(strings.SplitN(next, "?", 2)[1])
	require.NoError(t, err)
	for _, param := range []string{"scope", "container_id", "milestone", "lane", "only_stuck", "page_size"} {
		assert.Equal(t, query.Get(param), nextQuery.Get(param),
			"%s must survive the page move, or the next page is a different set of tasks", param)
	}

	// And the moved-to page really asks the store for the same thing --
	// the assertion that matters, since a link can carry a parameter the
	// parser then drops.
	second := fetch(t, mux, next)
	require.Equal(t, http.StatusOK, second.Code, "body: %s", second.Body.String())
	require.GreaterOrEqual(t, len(tasks.listed), 2)
	moved := tasks.listed[len(tasks.listed)-1]
	assert.Equal(t, store.ProductTaskScope{Kind: store.ProductTaskScopeMilepebble, ContainerID: productTaskMilepebble}, moved.Scope)
	require.NotNil(t, moved.Lane)
	assert.Equal(t, store.LaneTesting, *moved.Lane)
	assert.Equal(t, 2, moved.Page.PageSize)
	assert.NotEmpty(t, moved.Page.ContinuationToken)
}

// TestTasksReturningToTheFirstPageShowsTheSameRows closes the loop the
// previous control cannot: because the pages are URLs, coming back to the
// first one is a plain GET, and it must reproduce the first page exactly.
//
// This is what a Previous link would have to guarantee if it existed, and
// it is what the browser's Back button gives an operator instead -- so the
// claim is tested even though the control is disabled.
func TestTasksReturningToTheFirstPageShowsTheSameRows(t *testing.T) {
	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	first := fetch(t, mux, productTaskTasksURL("page_size=2"))
	require.Equal(t, http.StatusOK, first.Code)
	firstRows := taskRowIDsIn(first.Body.String())
	require.Len(t, firstRows, 2)

	forward := fetch(t, mux, pagingLinkHref(first.Body.String(), `data-krill="paging-next"`))
	require.Equal(t, http.StatusOK, forward.Code)
	backRows := taskRowIDsIn(forward.Body.String())
	require.Len(t, backRows, 2)
	assert.NotEqual(t, firstRows, backRows, "sanity: the walk actually advanced")

	// Back to the first page's own URL, which is what Back does.
	again := fetch(t, mux, productTaskTasksURL("page_size=2"))
	require.Equal(t, http.StatusOK, again.Code)
	assert.Equal(t, firstRows, taskRowIDsIn(again.Body.String()),
		"the first page is the same page again, rows and order")
}

// TestTasksTokenForAnotherScopeIsARefusalWithAWayBack is the FR's fourth
// claim, for the scope half.
//
// The case is real because the token is URL-carried: this fixture mints a
// genuine token bound to a DIFFERENT scope and presents it here. The store
// refuses it before any cursor is handed back, and the page must answer
// with an inline error and a way back -- never rows (which would be another
// scope's tasks), and never an empty page (which would read as "this scope
// has no work").
func TestTasksTokenForAnotherScopeIsARefusalWithAWayBack(t *testing.T) {
	foreign := store.EncodeFilteredContinuationToken(uuid.New(), store.FilterSet{}, store.Cursor{
		SortKey: uuid.NewString(),
		ID:      uuid.New(),
	})

	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("page_size=2&page_token="+url.QueryEscape(foreign)))

	assert.Equal(t, http.StatusBadRequest, rec.Code, "a caller's own bad token is the caller's error")
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-page-error"`)
	assert.Contains(t, body, "different scope",
		"the operator is told which of the two bindings failed")
	assert.NotContains(t, body, `data-krill="product-tasks-empty"`,
		"a refused token must never read as a scope with no tasks")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`,
		"a refused token is not a failed read, and must not send anyone to the logs")
	assert.Empty(t, taskRowIDsIn(body), "no rows of any scope may render behind a refused token")
	assert.Contains(t, body, `data-krill="product-tasks-page-recovery"`, "and there is a way back")

	// The way back is a working link to the first page, and following it
	// gives the rows the operator was looking at before the token went
	// stale -- with the filters they had set still applied.
	recovery := pagingLinkHref(body, `data-krill="product-tasks-page-recovery"`)
	require.NotEmpty(t, recovery, "the refusal must offer a way back, not a dead end")
	assert.NotContains(t, recovery, "page_token=", "the way back drops the refused token")

	back := fetch(t, mux, recovery)
	require.Equal(t, http.StatusOK, back.Code, "body: %s", back.Body.String())
	assert.Len(t, taskRowIDsIn(back.Body.String()), 2)
}

// TestTasksTokenForAnotherFilterSetIsARefusal pins the filter half, and
// the case that is easier to miss: the scope is right and the page would
// otherwise be a plausible one.
//
// A token minted before the operator changed the lane filter names a
// position in a different keyset. Answering it with rows would show tasks
// the URL does not ask for -- so it gets the same inline error and the
// same way back, and that way back KEEPS the lane, because the filter is
// what the operator wants, not the token.
func TestTasksTokenForAnotherFilterSetIsARefusal(t *testing.T) {
	// 20 rows, so the Testing lane the recovery link keeps spans more than
	// the page size -- otherwise the assertion below would be satisfied by a
	// one-page set and would not show that the recovery link re-reads at all.
	rows := pagedRowFixture(20)
	stale := store.EncodeFilteredContinuationToken(chromeScopeID,
		store.FilterSet{}.With("lane", string(store.LaneDone)),
		store.Cursor{SortKey: rows[1].TaskID.String(), ID: rows[1].TaskID})

	tasks := &pagedProductTasks{rows: rows}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("page_size=2&lane=Testing&page_token="+url.QueryEscape(stale)))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-page-error"`)
	assert.Contains(t, body, "different scope",
		"one sentence covers both bindings, so a shared link never leaks which one failed")
	assert.Empty(t, taskRowIDsIn(body))

	recovery := pagingLinkHref(body, `data-krill="product-tasks-page-recovery"`)
	require.NotEmpty(t, recovery)
	assert.Contains(t, recovery, "lane=Testing",
		"the way back keeps the operator's filter; dropping it would discard what they asked for")

	back := fetch(t, mux, recovery)
	require.Equal(t, http.StatusOK, back.Code, "body: %s", back.Body.String())
	assert.Len(t, taskRowIDsIn(back.Body.String()), 2)
}

// TestTasksRefusedTokenIsInlineForHTMX is the same refusal as an htmx swap
// gets: 200 with the error inline.
//
// htmx does not swap on a non-2xx, so a 400 fragment would leave the
// operator clicking Next against an unchanged table with no explanation --
// the failure the whole two-mode split in this handler exists to prevent.
func TestTasksRefusedTokenIsInlineForHTMX(t *testing.T) {
	foreign := store.EncodeFilteredContinuationToken(uuid.New(), store.FilterSet{}, store.Cursor{
		SortKey: uuid.NewString(),
		ID:      uuid.New(),
	})

	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	rec := htmxGet(mux, productTaskTasksURL("page_size=2&page_token="+url.QueryEscape(foreign)))

	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-page-error"`)
	assert.Contains(t, body, `id="`+pages.ProductTasksAnchor+`"`,
		"the fragment keeps the region's own id, or the swap deletes its own target")
	assert.NotContains(t, body, "<html", "an htmx swap gets a fragment")
	assert.Contains(t, body, `data-krill="product-tasks-page-recovery"`)
}

// TestTasksMalformedTokenIsTheSameRefusal covers the third refusal the
// store can make: a page_token that is not a token at all.
//
// It belongs with the mismatches rather than with the read failure, because
// an operator who hand-edits a shared link or truncates one has a stale URL
// and not a broken database -- and telling them to check the logs would
// send them somewhere the answer is not.
func TestTasksMalformedTokenIsTheSameRefusal(t *testing.T) {
	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("page_size=2&page_token=not-a-real-token"))

	assert.Equal(t, http.StatusBadRequest, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-page-error"`)
	assert.Contains(t, body, "not a valid continuation token")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`)
	assert.Contains(t, body, `data-krill="product-tasks-page-recovery"`)
}

// TestTasksFailedReadIsNotTheTokenRefusal is the negative that keeps the
// two apart in the other direction.
//
// A store that is simply down is NOT a refused token. If it were answered
// as one, every database blip would tell an operator their link was stale
// and offer them a "back to the first page" link that cannot help -- while
// the 500 an on-call human needs never fires.
func TestTasksFailedReadIsNotTheTokenRefusal(t *testing.T) {
	tasks := &pagedProductTasks{err: store.ErrInvalidContinuationToken}
	// Replace the error with a genuine store failure, since the fixture
	// returns one error for both reads.
	tasks.err = context.DeadlineExceeded

	mux := pagedTaskMux(t, tasks)
	rec := fetch(t, mux, productTaskTasksURL("page_size=2&page_token=whatever"))

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `data-krill="product-tasks-error"`)
	assert.NotContains(t, body, `data-krill="product-tasks-page-error"`,
		"a dead store is not a stale link")
}

// TestTasksPastTheEndPageIsNotAnEmptyScope is the case paging introduces
// that the filters' empty state did not have to think about.
//
// A token that was valid when the operator followed it can point past the
// end of a set that has since shrunk: the page comes back with no rows
// while the count for the SAME filters says there are some. That is not a
// scope with no work and it is not a failed read -- it is a page position
// that has run out -- so the empty state has to say so, or the operator is
// sent to change a filter that is not the problem.
func TestTasksPastTheEndPageIsNotAnEmptyScope(t *testing.T) {
	rows := pagedRowFixture(4)
	tasks := &pagedProductTasks{rows: rows}
	mux := pagedTaskMux(t, tasks)

	// A token naming the LAST row: resuming after it legitimately yields
	// nothing, while the count still says four.
	past := store.EncodeFilteredContinuationToken(chromeScopeID, (&store.ListProductTasksParams{
		ScopeID:   chromeScopeID,
		ProductID: productTaskProduct,
		Scope:     store.ProductTaskScope{Kind: store.ProductTaskScopeIncomplete},
	}).FilterSet(), store.Cursor{SortKey: rows[3].TaskID.String(), ID: rows[3].TaskID})

	rec := fetch(t, mux, productTaskTasksURL("page_size=2&page_token="+url.QueryEscape(past)))

	require.Equal(t, http.StatusOK, rec.Code, "an exhausted page is an ordinary answer, not a failure")
	body := rec.Body.String()
	require.Contains(t, body, `data-krill="product-tasks-empty"`)
	empty := emptyStateOf(t, body)
	assert.Contains(t, empty, "past the last one",
		"the sentence must blame the page, not the filters: %s", empty)
	assert.Contains(t, empty, "All incomplete milestones",
		"and it still names the scope, the way every other empty state does")
	assert.NotContains(t, body, `data-krill="product-tasks-error"`)
}

// TestTasksFooterIsNotOnTheBoard pins the footer's scope: the Tasks
// table's.
//
// The Board is the same region with its own requirement about what it does
// when a scope exceeds one render (FR cf000440), and this footer's Next
// link pages a board that has no lanes of its own yet. Rendering it there
// would be answering for a view this task does not own.
func TestTasksFooterIsNotOnTheBoard(t *testing.T) {
	tasks := &pagedProductTasks{rows: pagedRowFixture(7)}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, "/products/"+productTaskProduct.String()+"/board?page_size=2")

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	assert.NotContains(t, regionHTML(t, rec.Body.String()), `data-krill="task-paging"`,
		"the Board's own rendering requirement is not this footer's to answer")
}
