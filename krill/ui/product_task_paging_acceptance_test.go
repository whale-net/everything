// The Testing lane's own additions for FR 7bff09fe, aimed at the claims the
// Implementation phase's paging tests do not fully pin.
//
// The Implementation file (product_task_paging_test.go) walks a real
// keyset-paged fixture and covers the acceptance list directly. What it does
// not do is make the store DISAGREE with itself: every count it serves is
// len(rows), so a footer that derived Y from the rows would satisfy all of
// it. The cases here serve a count that no derivation from the page could
// produce, extend the never-share-markup matrix to the fourth state, and
// pin the two operator paths paging adds that the first file does not
// reach -- Refresh on a page other than the first, and the recovery link
// when the token is the only thing in the query.
package main

import (
	"context"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/store"
)

// miscountingProductTasks pages for real (the embedded fixture) but answers
// CountProductTasks with a number deliberately unrelated to the rows it
// holds.
//
// This is the fixture the other paging tests cannot be. There, Y is always
// len(filtered), so a UI that computed "Showing X of Y" by counting the
// rows it had would agree with the store on every page and no assertion
// would notice. Here the two can only both be right if the footer really
// took Y from the count read.
type miscountingProductTasks struct {
	*pagedProductTasks
	total int
}

func (s *miscountingProductTasks) CountProductTasks(_ context.Context, params store.ListProductTasksParams) (int, error) {
	s.counted = append(s.counted, params)
	return s.total, nil
}

// TestTasksFooterCountIsTheStoresAnswerNotTheRows is the strongest form of
// the FR's first claim: a Y that could not have come from the page.
//
// The fixture holds three rows and reports a total of 413. Every plausible
// wrong answer is excluded by construction:
//
//   - Y from len(rows) would be 2, the length of the page;
//   - Y from the whole filtered set would be 3, the fixture's own size;
//   - Y guessed as a multiple of the page size would be some round number.
//
// 413 is none of those, so the only way to render it is to have read it.
func TestTasksFooterCountIsTheStoresAnswerNotTheRows(t *testing.T) {
	tasks := &miscountingProductTasks{
		pagedProductTasks: &pagedProductTasks{rows: pagedRowFixture(3)},
		total:             413,
	}
	mux := pagedTaskMux(t, tasks)

	rec := fetch(t, mux, productTaskTasksURL("page_size=2"))

	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	require.Len(t, taskRowIDsIn(body), 2, "the fixture has more tasks than the page size")

	summary := pagingSummary(t, body)
	assert.Equal(t, "Showing 2 of 413 tasks", summary,
		"the footer must report the count read, which is not derivable from these rows")
	assert.NotContains(t, summary, "of 2 ",
		"a total equal to the page length is the silently truncated table the FR rules out")
	assert.NotContains(t, summary, "of 3 ",
		"nor is the size of the store's own row set")

	// And it is that store's count for that store's params, so the number on
	// screen is traceable to a read rather than to a constant.
	require.Len(t, tasks.counted, 1)
	require.Len(t, tasks.listed, 1)
	assert.Equal(t, tasks.listed[0], tasks.counted[0],
		"the count read the identical params the rows were read with")
}

// TestTasksPageErrorIsAFourthStateDistinctFromTheOtherThree extends the
// three-states matrix 79bad831 established to the state this task adds.
//
// A fourth alert in the same region is exactly where a marker collision
// hides: the three before it are all `data-krill="product-tasks-..."` on a
// div wrapping an alert, and so is the new one. Each state is driven
// through BOTH request modes -- a full page and an htmx swap -- and each
// must carry its own marker and none of the other three.
//
// The htmx half is the load-bearing one, exactly as in the original matrix:
// a full-page scope error renders the shell's status page, which carries no
// region marker at all, so an absence assertion made there would pass for
// the wrong reason and stay green if the markers were merged.
func TestTasksPageErrorIsAFourthStateDistinctFromTheOtherThree(t *testing.T) {
	const (
		emptyMark = `data-krill="product-tasks-empty"`
		readMark  = `data-krill="product-tasks-error"`
		scopeMark = `data-krill="product-tasks-scope-error"`
		pageMark  = `data-krill="product-tasks-page-error"`
	)
	allMarks := []string{emptyMark, readMark, scopeMark, pageMark}

	// A token minted for another scope, which the store refuses before it
	// hands back a cursor.
	foreign := store.EncodeFilteredContinuationToken(uuid.New(), store.FilterSet{}, store.Cursor{
		SortKey: uuid.NewString(), ID: uuid.New(),
	})
	// A container belonging to another product, which the resolver refuses
	// before any read.
	outOfProduct := "scope=milestone&container_id=" + productTaskOtherMilestone.String()

	for _, path := range []struct {
		name   string
		get    func(*testing.T, *http.ServeMux, string) *httptest.ResponseRecorder
		status map[string]int
	}{
		{
			name: "full page",
			get:  fetch,
			status: map[string]int{
				"empty": http.StatusOK, "read failed": http.StatusInternalServerError,
				"out of product": http.StatusNotFound, "refused token": http.StatusBadRequest,
			},
		},
		{
			// htmx gets 200 for all four -- it does not swap on a non-2xx,
			// so the status cannot be what tells them apart here. Only the
			// markup can, which is why this half is the load-bearing one.
			name: "htmx swap",
			get: func(_ *testing.T, mux *http.ServeMux, target string) *httptest.ResponseRecorder {
				return htmxGet(mux, target)
			},
			status: map[string]int{
				"empty": http.StatusOK, "read failed": http.StatusOK,
				"out of product": http.StatusOK, "refused token": http.StatusOK,
			},
		},
	} {
		t.Run(path.name, func(t *testing.T) {
			for _, tc := range []struct {
				name  string
				query string
				store func() store.TaskStore
				// presentOn is the marker this state DOES carry as a REGION
				// on the htmx path, which is the path where all four render
				// as region fragments and could be confused.
				presentOn string
				// presentOnFullPage is the same for a browser. Empty means
				// the state is not a region on that path at all: a
				// full-page scope error renders the shell's status page,
				// not the tasks region, and the 404 is what distinguishes
				// it -- so only the absence assertions say anything there.
				presentOnFullPage string
			}{
				{
					name:              "empty",
					query:             "page_size=2",
					store:             func() store.TaskStore { return &pagedProductTasks{} },
					presentOn:         emptyMark,
					presentOnFullPage: emptyMark,
				},
				{
					name:              "read failed",
					query:             "page_size=2",
					store:             func() store.TaskStore { return &pagedProductTasks{err: context.DeadlineExceeded} },
					presentOn:         readMark,
					presentOnFullPage: readMark,
				},
				{
					name:              "out of product",
					query:             outOfProduct,
					store:             func() store.TaskStore { return &pagedProductTasks{rows: pagedRowFixture(7)} },
					presentOn:         scopeMark,
					presentOnFullPage: "",
				},
				{
					name:              "refused token",
					query:             "page_size=2&page_token=" + url.QueryEscape(foreign),
					store:             func() store.TaskStore { return &pagedProductTasks{rows: pagedRowFixture(7)} },
					presentOn:         pageMark,
					presentOnFullPage: pageMark,
				},
			} {
				t.Run(tc.name, func(t *testing.T) {
					mux := pagedTaskMux(t, tc.store())
					rec := path.get(t, mux, productTaskTasksURL(tc.query))

					assert.Equal(t, path.status[tc.name], rec.Code,
						"status is part of the answer: body: %s", rec.Body.String())
					body := rec.Body.String()
					present := tc.presentOn
					if path.name == "full page" {
						present = tc.presentOnFullPage
					}
					if present != "" {
						require.Contains(t, body, present, "this state carries its own marker")
					}

					for _, other := range allMarks {
						if other == present {
							continue
						}
						assert.NotContains(t, body, other,
							"%s must never render as %s: the four states are disjoint by construction",
							tc.name, markerNameOf(other))
					}
				})
			}
		})
	}
}

// TestTasksRecoveryLinkDropsOnlyTheToken covers the one recovery path the
// Implementation file does not reach: a URL whose ONLY query parameter is
// the refused token.
//
// Every case there carries page_size alongside, so the branch that returns
// the bare path when nothing else survives was never taken. It matters
// because a bare path is a different string from path+"?" -- "?page_token="
// dropped leaves an empty query, and rendering `.../tasks?` is a link that
// is not the page the operator was reading.
func TestTasksRecoveryLinkDropsOnlyTheToken(t *testing.T) {
	foreign := store.EncodeFilteredContinuationToken(uuid.New(), store.FilterSet{}, store.Cursor{
		SortKey: uuid.NewString(), ID: uuid.New(),
	})
	mux := pagedTaskMux(t, &pagedProductTasks{rows: pagedRowFixture(7)})

	rec := fetch(t, mux, productTaskTasksURL("page_token="+url.QueryEscape(foreign)))
	require.Equal(t, http.StatusBadRequest, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()

	recovery := pagingLinkHref(body, `data-krill="product-tasks-page-recovery"`)
	require.NotEmpty(t, recovery, "the refusal must always offer a way back")
	assert.Equal(t, "/products/"+productTaskProduct.String()+"/tasks", recovery,
		"with nothing but the token in the query, the way back is the bare path, not a dangling '?'")
	assert.NotContains(t, recovery, "?")

	// And it is a page that works, not just a well-formed string.
	back := fetch(t, mux, recovery)
	assert.Equal(t, http.StatusOK, back.Code, "body: %s", back.Body.String())
	assert.NotContains(t, back.Body.String(), pageMarkConst,
		"the way back must actually leave the refusal behind")
}

// TestTasksRefreshKeepsThePagePosition pins the one control an operator on
// page two can reach that paging added state to.
//
// Refresh re-reads r.URL.RequestURI(), so it carries the token and stays on
// the page the operator pressed it on. Had the region rebuilt the refresh
// path from the bare path, Refresh would silently teleport them to page one
// of a list they had walked forward through -- the same class of drift the
// footer links are built to avoid, on a control the FR does not name.
func TestTasksRefreshKeepsThePagePosition(t *testing.T) {
	// 20 rows, so the Testing lane the filter names spans more than one page
	// at page_size=2 -- a filtered set that fits in one page would leave the
	// Next link absent and assert nothing.
	mux := pagedTaskMux(t, &pagedProductTasks{rows: pagedRowFixture(20)})

	first := fetch(t, mux, productTaskTasksURL("page_size=2&lane=Testing"))
	require.Equal(t, http.StatusOK, first.Code, "body: %s", first.Body.String())
	firstRows := taskRowIDsIn(first.Body.String())
	require.Len(t, firstRows, 2)

	second := fetch(t, mux, pagingLinkHref(first.Body.String(), `data-krill="paging-next"`))
	require.Equal(t, http.StatusOK, second.Code, "body: %s", second.Body.String())
	require.NotEqual(t, firstRows, taskRowIDsIn(second.Body.String()), "sanity: the walk advanced")

	refresh := refreshHrefOf(t, regionHTML(t, second.Body.String()))
	require.NotEmpty(t, refresh, "the region offers no Refresh")
	assert.Contains(t, refresh, "page_token=",
		"Refresh on page two must re-read page two, not jump the operator back to page one")
	assert.Contains(t, refresh, "lane=Testing", "and it must keep their filter too")

	// Pressing it lands them on the same page, not a different one.
	again := fetch(t, mux, refresh)
	require.Equal(t, http.StatusOK, again.Code, "body: %s", again.Body.String())
	assert.Equal(t, taskRowIDsIn(second.Body.String()), taskRowIDsIn(again.Body.String()),
		"Refresh must reproduce the page it was pressed on, rows and order")
}

// TestTasksPagingControlsCarryNoClientSideScript is the structural guard
// against the bug class that has taken out two sibling tasks: a control
// wired to a script that binds to `document` or `document.body` before the
// region it lives in is in either.
//
// The footer here is plain server-rendered markup -- a real <a> and a real
// disabled <button> -- so there is nothing to bind late. Asserting that
// keeps it that way: a future "let's make Next an htmx swap" edit would
// reintroduce the window without anyone deciding to.
func TestTasksPagingControlsCarryNoClientSideScript(t *testing.T) {
	mux := pagedTaskMux(t, &pagedProductTasks{rows: pagedRowFixture(7)})

	rec := fetch(t, mux, productTaskTasksURL("page_size=2"))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())

	footer := pagingFooterHTML(t, regionHTML(t, rec.Body.String()))
	for _, forbidden := range []string{"<script", "onclick", "onchange", "onload", "hx-get", "hx-post", "hx-swap", "hx-target"} {
		assert.NotContains(t, footer, forbidden,
			"the pager is server-rendered markup: a page position is a URL, and a script binding to it is a bug waiting")
	}

	// The enabled direction really is a link and the inert one really is a
	// disabled button, so "no script" is not "no control".
	assert.NotEmpty(t, pagingLinkHref(footer, `data-krill="paging-next"`), "Next is a real link")
	assert.True(t, pagingControlIsDisabled(t, footer, `data-krill="paging-previous"`),
		"Previous is an inert control, not an anchor with an invented target")
}

const pageMarkConst = `data-krill="product-tasks-page-error"`

// pagingFooterHTML cuts the pager out of the region, so an assertion about
// it cannot be satisfied by a control elsewhere on the page -- the shell's
// chrome carries hx-get attributes of its own.
func pagingFooterHTML(t *testing.T, region string) string {
	t.Helper()
	const marker = `data-krill="task-paging"`
	start := strings.Index(region, marker)
	require.NotEqual(t, -1, start, "the region rendered no footer: %s", region)
	at := strings.LastIndex(region[:start], "<div")
	require.NotEqual(t, -1, at, "the footer has no opening element")

	depth := 0
	for i := at; i < len(region); i++ {
		switch {
		case strings.HasPrefix(region[i:], "<div"):
			depth++
		case strings.HasPrefix(region[i:], "</div>"):
			depth--
			if depth == 0 {
				return region[at : i+len("</div>")]
			}
		}
	}
	t.Fatalf("the footer never closes: %s", region[at:])
	return ""
}

// refreshHrefOf is the hx-get the region's Refresh control carries, read
// forward from the marker (templ emits data-krill before the hx-get that
// follows it on the same tag) and unescaped: the attribute carries a
// multi-parameter query, so it arrives as "a&amp;b" and handing THAT to a
// request builder would produce a parameter named "amp;b".
func refreshHrefOf(t *testing.T, region string) string {
	t.Helper()
	at := strings.Index(region, `data-krill="refresh"`)
	require.NotEqual(t, -1, at, "the region offers no Refresh: %s", region)
	open := strings.LastIndex(region[:at], "<button")
	require.NotEqual(t, -1, open, "Refresh is not a button")
	frag := region[open:]
	if end := strings.Index(frag, "</button>"); end != -1 {
		frag = frag[:end]
	}
	hrefAt := strings.Index(frag, `hx-get="`)
	if hrefAt == -1 {
		return ""
	}
	rest := frag[hrefAt+len(`hx-get="`):]
	return html.UnescapeString(rest[:strings.Index(rest, `"`)])
}

func markerNameOf(mark string) string {
	return strings.TrimSuffix(strings.TrimPrefix(mark, `data-krill="`), `"`)
}
