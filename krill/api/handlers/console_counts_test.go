// Unit tests for the console count surface (console_counts.go, FR
// c4ab6c68-3a20-4824-b29c-48922a209fd1): GET
// /console/{claimed,cancelled,escalated,notes}/count, GET /console/overview
// and GET /products/{id}/tasks/count. No Postgres -- fakeTaskStore
// (fake_task_store_test.go) records the params each count was handed and
// returns a scripted figure or error; the real COUNT(*) queries and their
// agreement with the lists live in krill/store/task_console_integration_test.go,
// krill/store/task_note_console_integration_test.go and
// krill/store/task_integration_test.go against Postgres, and the shared-SQL
// guard is //krill/store:console_count_clause_test.
//
// What this file pins, none of which the store tests can reach:
//
//   - each count endpoint reads the same query string its list endpoint
//     reads, so a filter added to the list cannot fail to reach its count
//     (the same store params from the same URL is the proof, and it holds
//     across every filter the four queues and the task read accept);
//   - page_size and page_token are accepted and ignored -- a count is of
//     the whole filtered set, so a caller that sends paging gets the same
//     figure, not a page's length;
//   - the {"count": n} body and the Overview's seven-field body, so a
//     console reading them cannot mistake one figure for another;
//   - a count the store could not compute is an error status carrying no
//     figure at all, never a 200 with count 0 -- and a narrowing the store
//     refuses (a filter outside the scope, a container outside the
//     product) is its own 4xx, never a zero that reads as "this queue is
//     empty" or "this milestone has no work".
package handlers_test

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// serveConsoleCount routes through a real ServeMux at the endpoint's own
// path, so the URL under test is the one routes.go mounts.
func serveConsoleCount(t *testing.T, handler http.HandlerFunc, path, query string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+path, handler)
	target := path
	if query != "" {
		target += "?" + query
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

// consoleCountQueues pairs each queue's count endpoint with its list
// endpoint, so one assertion can prove the two are asked the same question.
var consoleCountQueues = []struct {
	name        string
	countPath   string
	listPath    string
	countHandle func(store.TaskStore) http.HandlerFunc
	listHandle  func(store.TaskStore) http.HandlerFunc
}{
	{"claimed", "/console/claimed/count", "/console/claimed", handlers.CountClaimedTasksHandler, handlers.ListClaimedTasksHandler},
	{"cancelled", "/console/cancelled/count", "/console/cancelled", handlers.CountCancelledTasksHandler, handlers.ListCancelledTasksHandler},
	{"escalated", "/console/escalated/count", "/console/escalated", handlers.CountEscalatedTasksHandler, handlers.ListEscalatedTasksHandler},
	{"open_notes", "/console/notes/count", "/console/notes", handlers.CountOpenNotesHandler, handlers.ListOpenNotesHandler},
}

// ── wire shape ──────────────────────────────────────────────────────────────

// TestConsoleCountHandlers_WireShape is the per-queue body contract: one
// figure under one key, so a console reading it cannot read a page's
// length or a different queue's number out of the same document.
func TestConsoleCountHandlers_WireShape(t *testing.T) {
	for _, q := range consoleCountQueues {
		t.Run(q.name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			switch q.name {
			case "claimed":
				tasks.countClaimedTasksResult = 7
			case "cancelled":
				tasks.countCancelledTasksResult = 7
			case "escalated":
				tasks.countEscalatedTasksResult = 7
			case "open_notes":
				tasks.countOpenNotesResult = 7
			}

			rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath, "scope_id="+uuid.New().String())

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			// The raw body, not a decoded struct: the point is which JSON
			// keys are present at all.
			var doc map[string]any
			require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
			assert.Equal(t, map[string]any{"count": float64(7)}, doc)
		})
	}
}

// TestConsoleOverviewHandler_WireShape is the Overview's seven-figure
// contract. Each key is present even at zero (a console shows "0
// escalated", not a missing field) and each carries its own figure, so a
// sub-line cannot be served the queue total by mistake.
func TestConsoleOverviewHandler_WireShape(t *testing.T) {
	tasks := &fakeTaskStore{countConsoleOverviewResult: store.ConsoleOverviewCounts{
		Escalated: 1, Claimed: 2, Cancelled: 3, OpenNotes: 4,
		EscalatedRecently: 5, ClaimsExpiringSoon: 6, OpenScopeNotes: 7,
	}}

	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(tasks), "/console/overview", "scope_id="+uuid.New().String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"escalated": 1, "claimed": 2, "cancelled": 3, "open_notes": 4,
		"escalated_recently": 5, "claims_expiring_soon": 6, "open_scope_notes": 7
	}`, rec.Body.String())
}

// TestConsoleOverviewHandler_AllZeroStillCarriesEveryFigure proves the keys
// survive a genuinely idle scope -- a console must be able to render "0 of
// everything" without nil-checking seven fields.
func TestConsoleOverviewHandler_AllZeroStillCarriesEveryFigure(t *testing.T) {
	tasks := &fakeTaskStore{}

	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(tasks), "/console/overview", "scope_id="+uuid.New().String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{
		"escalated": 0, "claimed": 0, "cancelled": 0, "open_notes": 0,
		"escalated_recently": 0, "claims_expiring_soon": 0, "open_scope_notes": 0
	}`, rec.Body.String())
}

// ── the count and its list are asked the same question ─────────────────────

// TestConsoleCountHandlers_SameQueryStringAsTheirList is the HTTP half of
// the drift-prevention criterion, and the reason each count endpoint exists
// beside a shared parser rather than beside its own: for the same URL, the
// store is handed the identical params from the count and from the list
// beside it. A filter added to the list's parser therefore reaches its
// count with no second edit; a count that grew its own parser would be the
// one thing that turns this red.
func TestConsoleCountHandlers_SameQueryStringAsTheirList(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	// Every parameter each of the four queues accepts, so the comparison
	// covers the whole filter set rather than the scope alone.
	queries := map[string]string{
		"scope_only":        "scope_id=" + scopeID.String(),
		"product":           "scope_id=" + scopeID.String() + "&product_id=" + productID.String(),
		"milestone":         "scope_id=" + scopeID.String() + "&milestone_id=" + milestoneID.String(),
		"product_milestone": "scope_id=" + scopeID.String() + "&product_id=" + productID.String() + "&milestone_id=" + milestoneID.String(),
		"with_paging": "scope_id=" + scopeID.String() + "&product_id=" + productID.String() +
			"&milestone_id=" + milestoneID.String() + "&page_size=1&page_token=abc",
		"with_reason": "scope_id=" + scopeID.String() + "&reason=manual",
	}

	for _, q := range consoleCountQueues {
		for name, query := range queries {
			t.Run(q.name+"/"+name, func(t *testing.T) {
				fromCount := &fakeTaskStore{}
				fromList := &fakeTaskStore{}

				countRec := serveConsoleCount(t, q.countHandle(fromCount), q.countPath, query)
				listRec := serveConsoleCount(t, q.listHandle(fromList), q.listPath, query)
				require.Equal(t, http.StatusOK, countRec.Code, countRec.Body.String())
				require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())

				assertSameConsoleFilters(t, q.name, fromList, fromCount)
			})
		}
	}
}

// consoleScopeAndFilter is the narrowing half of a queue's params -- the
// part a count and its list must agree on, whatever the caller sent.
type consoleScopeAndFilter struct {
	ScopeID       uuid.UUID
	ConsoleFilter store.ConsoleFilter
	Reason        *store.EscalationReason
	Page          store.PageParams
}

// consoleParamsFromCount and consoleParamsFromList read back the params
// each endpoint handed the store, per queue, so the comparison above is
// queue-agnostic.
func consoleParamsFromCount(t *testing.T, queue string, f *fakeTaskStore) consoleScopeAndFilter {
	t.Helper()
	switch queue {
	case "claimed":
		p := f.gotCountClaimedTasksParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	case "cancelled":
		p := f.gotCountCancelledTasksParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	case "escalated":
		p := f.gotCountEscalatedTasksParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, p.Reason, p.Page}
	default:
		p := f.gotCountOpenNotesParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	}
}

func consoleParamsFromList(t *testing.T, queue string, f *fakeTaskStore) consoleScopeAndFilter {
	t.Helper()
	switch queue {
	case "claimed":
		p := f.gotListClaimedTasksParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	case "cancelled":
		p := f.gotListCancelled
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	case "escalated":
		p := f.gotListEscalated
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, p.Reason, p.Page}
	default:
		p := f.gotListOpenNotesParams
		return consoleScopeAndFilter{p.ScopeID, p.ConsoleFilter, nil, p.Page}
	}
}

// assertSameConsoleFilters is the drift guard proper: for one query string,
// the count and the list beside it must narrow identically, and the count
// must be the one that drops the page. A filter added to the list's parser
// reaches its count with no second edit; a count that dropped a filter, or
// inherited the list's page, is what turns this red.
func assertSameConsoleFilters(t *testing.T, queue string, fromList, fromCount *fakeTaskStore) {
	t.Helper()
	list, count := consoleParamsFromList(t, queue, fromList), consoleParamsFromCount(t, queue, fromCount)

	assert.Equal(t, list.ScopeID, count.ScopeID, "%s: the count and the list must narrow the same scope", queue)
	assert.Equal(t, list.ConsoleFilter, count.ConsoleFilter,
		"%s: the count and the list must take the same product/milestone narrowing -- a count that could not be narrowed answers a question nobody asked", queue)
	assert.Equal(t, list.Reason, count.Reason, "%s: the count and the list must take the same reason narrowing", queue)
	assert.Equal(t, store.PageParams{}, count.Page,
		"%s: a count is of the whole filtered set, so it must reach the store with no page even when the caller sent one", queue)
}

// TestConsoleOverviewHandler_AppliesOneQueryStringToEveryQueue proves the
// Overview narrows all four queues at once, the reason plus the
// product/milestone pair, so a console filtering its Overview filters the
// queue pages it links to identically.
func TestConsoleOverviewHandler_AppliesOneQueryStringToEveryQueue(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	tasks := &fakeTaskStore{}

	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(tasks), "/console/overview",
		"scope_id="+scopeID.String()+"&product_id="+productID.String()+"&milestone_id="+milestoneID.String()+"&reason=manual")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	got := tasks.gotCountConsoleOverviewParams
	filter := store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}
	manual := store.EscalationReasonManual
	assert.Equal(t, store.ListClaimedTasksParams{ScopeID: scopeID, ConsoleFilter: filter}, got.Claimed)
	assert.Equal(t, store.ListCancelledTasksParams{ScopeID: scopeID, ConsoleFilter: filter}, got.Cancelled)
	assert.Equal(t, store.ListOpenNotesParams{ScopeID: scopeID, ConsoleFilter: filter}, got.Notes)
	// The escalated figure takes the reason too, and only the escalated one:
	// the other three queues have no such filter to apply it to.
	assert.Equal(t, store.ListEscalatedTasksParams{ScopeID: scopeID, ConsoleFilter: filter, Reason: &manual}, got.Escalated)
}

// ── a count is of the whole set, never a page ───────────────────────────────

// TestConsoleCountHandlers_IgnorePaging proves the paging parameters change
// nothing a caller can observe: with page_size=1 the figure is the same
// whole-set total, never the one row a page would have held. The count
// endpoint reads no page at all -- its figure is whatever Count* returned,
// so a queue larger than one page cannot be reported as its first page.
func TestConsoleCountHandlers_IgnorePaging(t *testing.T) {
	scopeID := uuid.New().String()
	for _, q := range consoleCountQueues {
		t.Run(q.name, func(t *testing.T) {
			// A queue of seven behind a one-row page: the two figures a
			// count could plausibly confuse.
			tasks := &fakeTaskStore{}
			switch q.name {
			case "claimed":
				tasks.countClaimedTasksResult = 7
				tasks.listClaimedTasksResult = store.Page[store.ClaimedTaskRow]{Items: []store.ClaimedTaskRow{{TaskID: uuid.New()}}}
			case "cancelled":
				tasks.countCancelledTasksResult = 7
				tasks.listCancelledResult = store.Page[store.CancelledTaskRow]{Items: []store.CancelledTaskRow{{TaskID: uuid.New()}}}
			case "escalated":
				tasks.countEscalatedTasksResult = 7
				tasks.listEscalatedResult = store.Page[store.EscalatedTaskRow]{Items: []store.EscalatedTaskRow{{TaskID: uuid.New()}}}
			case "open_notes":
				tasks.countOpenNotesResult = 7
				tasks.listOpenNotesResult = store.Page[store.OpenNoteRow]{Items: []store.OpenNoteRow{{NoteID: uuid.New()}}}
			}

			rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath,
				"scope_id="+scopeID+"&page_size=1&page_token=some-token")

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"count": 7}`, rec.Body.String(),
				"a count is of the whole filtered set; the page a caller happened to request never narrows it")
			assert.Equal(t, store.PageParams{}, consoleParamsFromCount(t, q.name, tasks).Page,
				"the page must not even reach the store, rather than being passed down and relied on to be ignored")
		})
	}
}

// TestCountProductTasksHandler_WireShape is the "Y" in "Showing X of Y
// tasks": the same one-key body as the queue counts, over the same handler
// family, so a task page can print its total without a bespoke shape.
func TestCountProductTasksHandler_WireShape(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{countProductTasksResult: 42}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(productID, scopeID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks/count", nil))

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"count": 42}`, rec.Body.String())
}

// TestCountProductTasksHandler_SameParamsAsItsList is the product task
// read's own half of the drift-prevention criterion: the count and the list
// share one query-string parser, so for the same request they reach the
// store with identical scope, container, lane and only-stuck filters.
func TestCountProductTasksHandler_SameParamsAsItsList(t *testing.T) {
	productID, scopeID, containerID := uuid.New(), uuid.New(), uuid.New()

	for name, query := range map[string]string{
		"default":     "",
		"milestone":   "scope=milestone&container_id=" + containerID.String(),
		"milepebble":  "scope=milepebble&container_id=" + containerID.String(),
		"lane":        "lane=Testing",
		"only_stuck":  "only_stuck=true",
		"lane_stuck":  "lane=Done&only_stuck=true",
		"with_paging": "page_size=1&page_token=abc",
	} {
		t.Run(name, func(t *testing.T) {
			listStore := &fakeTaskStore{}
			countStore := &fakeTaskStore{}
			products := existingProduct(productID, scopeID)

			listMux := http.NewServeMux()
			listMux.HandleFunc("GET /products/{id}/tasks", handlers.ListProductTasksHandler(listStore, products))
			listRec := httptest.NewRecorder()
			listMux.ServeHTTP(listRec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks?"+query, nil))

			countMux := http.NewServeMux()
			countMux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(countStore, products))
			countRec := httptest.NewRecorder()
			countMux.ServeHTTP(countRec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks/count?"+query, nil))

			require.Equal(t, http.StatusOK, listRec.Code, listRec.Body.String())
			require.Equal(t, http.StatusOK, countRec.Code, countRec.Body.String())

			list, count := listStore.gotListProductTasksParams, countStore.gotCountProductTasksParams
			assert.Equal(t, list.ScopeID, count.ScopeID, "the total and the page beneath it narrow the same scope")
			assert.Equal(t, list.ProductID, count.ProductID)
			assert.Equal(t, list.Scope, count.Scope, "the same container selection -- a total of a different container reads as a different list's total")
			assert.Equal(t, list.Lane, count.Lane, "the same lane narrowing: a count that dropped the lane would report a wider total than the page above it")
			assert.Equal(t, list.OnlyStuck, count.OnlyStuck, "the same only-stuck narrowing, for the same reason")
			assert.Equal(t, store.PageParams{}, count.Page,
				"a count is of the whole filtered set, so it must reach the store with no page even when the caller sent one")
		})
	}
}

// ── validation ──────────────────────────────────────────────────────────────

// TestConsoleCountHandlers_MissingScopeID_Return400 proves scope_id is
// required on every count endpoint exactly as on every list endpoint --
// there is no path entity to resolve it from.
func TestConsoleCountHandlers_MissingScopeID_Return400(t *testing.T) {
	for _, q := range consoleCountQueues {
		t.Run(q.name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath, "")
			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, store.ListClaimedTasksParams{}, tasks.gotCountClaimedTasksParams,
				"a rejected request must never reach the store")
		})
	}
	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(&fakeTaskStore{}), "/console/overview", "")
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestConsoleCountHandlers_MalformedFilterParam_Returns400 proves a
// product_id/milestone_id that is present but not a UUID is refused rather
// than dropped -- a silently unnarrowed count would answer a wider question
// than the caller asked, which is the one failure a count cannot have.
func TestConsoleCountHandlers_MalformedFilterParam_Returns400(t *testing.T) {
	for _, q := range consoleCountQueues {
		for _, bad := range []string{"&product_id=not-a-uuid", "&milestone_id=not-a-uuid"} {
			t.Run(q.name+bad, func(t *testing.T) {
				tasks := &fakeTaskStore{}
				rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath, "scope_id="+uuid.New().String()+bad)
				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			})
		}
	}
}

// TestCountProductTasksHandler_BadQueryParams_Return400 proves the total
// beside a task page validates exactly as the page does, so a caller gets
// the same fixable error whichever of the two it asked.
func TestCountProductTasksHandler_BadQueryParams_Return400(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	for name, query := range map[string]string{
		"unknown scope":                "scope=everything",
		"milestone without container":  "scope=milestone",
		"milepebble without container": "scope=milepebble",
		"non-UUID container":           "scope=milestone&container_id=nope",
		"unknown lane":                 "lane=Nope",
		"non-boolean only_stuck":       "only_stuck=perhaps",
	} {
		t.Run(name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			mux := http.NewServeMux()
			mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(productID, scopeID)))
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks/count?"+query, nil))

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.Equal(t, store.ListProductTasksParams{}, tasks.gotCountProductTasksParams,
				"a rejected request must never reach the store")
		})
	}

	// A non-UUID {id} is refused before the product lookup, the same as on
	// the list endpoint above it.
	tasks := &fakeTaskStore{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(productID, scopeID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/not-a-uuid/tasks/count", nil))
	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// ── a failed or refused count is not a zero ─────────────────────────────────

// TestConsoleCountHandlers_FailedCountIsAnError proves FR c4ab6c68's
// fail-don't-degrade rule on every queue: a count the store could not
// compute answers with an error status and no figure at all. A 200
// carrying count 0 here is the exact failure the rule exists to prevent --
// it is indistinguishable from an idle queue, and an operator would read a
// broken read as "nothing needs attention".
func TestConsoleCountHandlers_FailedCountIsAnError(t *testing.T) {
	for _, q := range consoleCountQueues {
		t.Run(q.name, func(t *testing.T) {
			tasks := &fakeTaskStore{}
			switch q.name {
			case "claimed":
				tasks.countClaimedTasksErr = errors.New("connection reset by peer")
			case "cancelled":
				tasks.countCancelledTasksErr = errors.New("connection reset by peer")
			case "escalated":
				tasks.countEscalatedTasksErr = errors.New("connection reset by peer")
			case "open_notes":
				tasks.countOpenNotesErr = errors.New("connection reset by peer")
			}

			rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath, "scope_id="+uuid.New().String())

			assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
			assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
			assert.NotContains(t, rec.Body.String(), "count",
				"a failed count must carry no figure -- not even a zero")
			assert.NotContains(t, rec.Body.String(), "connection reset by peer",
				"a 500 must leak no store text")
		})
	}
}

// TestConsoleOverviewHandler_FailedCountIsAnError proves one uncountable
// figure fails the whole Overview rather than being reported as zero: a
// console taking the Overview alone would otherwise render a queue it could
// not measure as an empty one.
func TestConsoleOverviewHandler_FailedCountIsAnError(t *testing.T) {
	tasks := &fakeTaskStore{countConsoleOverviewErr: errors.New("connection reset by peer")}

	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(tasks), "/console/overview", "scope_id="+uuid.New().String())

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
	for _, figure := range []string{"escalated", "claimed", "cancelled", "open_notes", "open_scope_notes"} {
		assert.NotContains(t, rec.Body.String(), figure,
			"a failed Overview must carry no figures at all, not partial ones")
	}
}

// TestCountProductTasksHandler_FailedCountIsAnError is the same rule for
// the "Y" behind "Showing X of Y tasks": a total that could not be computed
// must not render as "Showing 3 of 0 tasks".
func TestCountProductTasksHandler_FailedCountIsAnError(t *testing.T) {
	productID, scopeID := uuid.New(), uuid.New()
	tasks := &fakeTaskStore{countProductTasksErr: errors.New("connection reset by peer")}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(productID, scopeID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+productID.String()+"/tasks/count", nil))

	assert.Equal(t, http.StatusInternalServerError, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "internal error"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "count")
}

// TestConsoleCountHandlers_RefusedNarrowingIsNotZero proves each store
// refusal keeps its own status on a count exactly as on a list: a filter
// outside the caller's scope is a 404, a wrong-filter continuation token
// and an unknown escalation reason are 400s, and a container outside the
// product is a 404 -- never a 200 carrying zero. A zero for a milestone the
// caller named wrongly reads as "this milestone has no work", which is the
// misreading LB1 exists to prevent.
func TestConsoleCountHandlers_RefusedNarrowingIsNotZero(t *testing.T) {
	for name, tc := range map[string]struct {
		storeErr error
		want     int
	}{
		"filter_outside_scope": {storeErr: store.ErrNotFound, want: http.StatusNotFound},
		"wrong_filter_token":   {storeErr: store.ErrTokenFilterMismatch, want: http.StatusBadRequest},
		"unknown_reason":       {storeErr: store.ErrUnknownEscalationReason, want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			for _, q := range consoleCountQueues {
				t.Run(q.name, func(t *testing.T) {
					tasks := &fakeTaskStore{}
					switch q.name {
					case "claimed":
						tasks.countClaimedTasksErr = tc.storeErr
					case "cancelled":
						tasks.countCancelledTasksErr = tc.storeErr
					case "escalated":
						tasks.countEscalatedTasksErr = tc.storeErr
					case "open_notes":
						tasks.countOpenNotesErr = tc.storeErr
					}

					rec := serveConsoleCount(t, q.countHandle(tasks), q.countPath, "scope_id="+uuid.New().String())

					assert.Equal(t, tc.want, rec.Code, rec.Body.String())
					assert.NotContains(t, rec.Body.String(), `"count"`,
						"a refused count must carry no figure at all")
				})
			}

			// The Overview refuses the same narrowing the same way, rather
			// than reporting four zeros.
			overview := &fakeTaskStore{countConsoleOverviewErr: tc.storeErr}
			rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(overview), "/console/overview", "scope_id="+uuid.New().String())
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

// TestCountProductTasksHandler_ContainerOutsideProduct_Returns404 is LB1 on
// the total: a container the product does not own is a distinct 404, not a
// total of zero and not another product's number.
func TestCountProductTasksHandler_ContainerOutsideProduct_Returns404(t *testing.T) {
	productID, scopeID, containerID := uuid.New(), uuid.New(), uuid.New()
	tasks := &fakeTaskStore{countProductTasksErr: store.ErrMilestoneOutsideProduct}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(productID, scopeID)))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/products/"+productID.String()+"/tasks/count?scope=milestone&container_id="+containerID.String(), nil))

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "milestone not found in this product"}`, rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "count")
}

// TestCountProductTasksHandler_UnknownProduct_Returns404 proves an unknown
// product is a distinct 404 rather than a total of zero, which would read
// as "this product has no tasks" for a product that does not exist.
func TestCountProductTasksHandler_UnknownProduct_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, &fakeProductTaskProductStore{getErr: store.ErrNotFound}))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+uuid.New().String()+"/tasks/count", nil))

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
	assert.JSONEq(t, `{"error": "product not found"}`, rec.Body.String())
	assert.Equal(t, store.ListProductTasksParams{}, tasks.gotCountProductTasksParams)
}

// TestConsoleCountHandlers_AreUngated pins NFR6's write-only gate on this
// surface: every count answers without a krill_session_id, exactly as the
// list endpoints they sit beside do. A count that required a session would
// be unreadable on the very console page it feeds.
func TestConsoleCountHandlers_AreUngated(t *testing.T) {
	scopeID := uuid.New().String()
	for _, q := range consoleCountQueues {
		t.Run(q.name, func(t *testing.T) {
			rec := serveConsoleCount(t, q.countHandle(&fakeTaskStore{}), q.countPath, "scope_id="+scopeID)
			assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		})
	}

	rec := serveConsoleCount(t, handlers.ConsoleOverviewHandler(&fakeTaskStore{}), "/console/overview", "scope_id="+scopeID)
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	tasks := &fakeTaskStore{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products/{id}/tasks/count", handlers.CountProductTasksHandler(tasks, existingProduct(uuid.New(), uuid.New())))
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/products/"+uuid.New().String()+"/tasks/count", nil))
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
}

// TestConsoleCountWire_IsTheDocumentBothSurfacesReturn pins the shared
// response type itself: the per-queue figure is one exported struct, so the
// MCP count tools beside these endpoints return this exact document rather
// than a mirror of it that could gain a field without them.
func TestConsoleCountWire_IsTheDocumentBothSurfacesReturn(t *testing.T) {
	var zero handlers.ConsoleCountWire
	assert.Equal(t, `{"count":0}`, mustMarshalJSON(t, zero))

	var overview handlers.ConsoleOverviewWire
	assert.Equal(t, `{"escalated":0,"claimed":0,"cancelled":0,"open_notes":0,"escalated_recently":0,"claims_expiring_soon":0,"open_scope_notes":0}`,
		mustMarshalJSON(t, overview), "every Overview figure is present at zero -- a console renders all seven without a nil check")
}

func mustMarshalJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	return string(raw)
}
