package main

// The detail's interventions through the REAL route: the same POST
// /ops/tasks/{id}/{verb} the Needs attention rows use, driven with the form
// the detail page actually rendered, against a store whose state moves the
// way a real intervention moves it (FR af61631d).
//
// What this file adds over task_detail_actions_test.go is the ROUND TRIP: the
// response is re-derived from a fresh read of the task, so the header, the
// rail and the callout the operator is left looking at describe the state
// their write produced -- not the state the page was loaded with, and not
// anything the request carried.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/ui/pages"
)

// ---------------------------------------------------------------------------
// markup helpers this target does not share with ui_lib_test
// ---------------------------------------------------------------------------

// swapRegionBetween is regionBetween for this target, which does not compile
// the detail page's test file.
func swapRegionBetween(t *testing.T, html, start, end string) string {
	t.Helper()
	from := strings.Index(html, start)
	require.GreaterOrEqual(t, from, 0, "the rendering carries no %s region:\n%s", start, html)
	rest := html[from:]
	to := strings.Index(rest, end)
	require.GreaterOrEqual(t, to, 0, "the %s region is never closed:\n%s", start, html)
	return rest[:to]
}

// swapOfferedVerbs is the verbs a region's controls post, in document order,
// read off the hx-post each control carries.
func swapOfferedVerbs(t *testing.T, region string) []string {
	t.Helper()
	const open = `hx-post="/ops/tasks/`
	var verbs []string
	rest := region
	for {
		i := strings.Index(rest, open)
		if i < 0 {
			return verbs
		}
		rest = rest[i+len(open):]
		j := strings.Index(rest, `"`)
		require.GreaterOrEqual(t, j, 0, "an hx-post attribute is never closed:\n%s", region)
		path := rest[:j]
		verbs = append(verbs, path[strings.LastIndex(path, "/")+1:])
		rest = rest[j:]
	}
}

// headLine is the first line of a rendering, for a failure message that does
// not dump a whole page.
func headLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// the fakes: one task's state, before and after a write
// ---------------------------------------------------------------------------

// detailSwapStore is the read surface a detail-page intervention needs, with
// a state that MOVES: task is what a read answers now, and after is what a
// read answers once the write has landed. The two differing is the whole
// point -- a response that echoed the read taken for the page would answer
// with the pre-write state, and these tests exist to catch that.
type detailSwapStore struct {
	store.TaskStore

	mu    sync.Mutex
	task  store.Task
	after *store.Task
	// others are tasks the store can also resolve, so a return_to pointing
	// at another task's detail resolves to a renderable page rather than
	// falling down the "could not be re-read" branch and passing for the
	// wrong reason.
	others map[uuid.UUID]store.Task

	claim store.Claim
	esc   store.EscalationEvent
}

func (s *detailSwapStore) current() store.Task {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.task
}

// applyAfter moves the store to the post-write state: what the intervention
// just did.
func (s *detailSwapStore) applyAfter() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.after != nil {
		s.task, s.after = *s.after, nil
	}
}

// setState replaces the current state outright, for a case that needs the
// state to move without the write landing.
func (s *detailSwapStore) setState(t store.Task) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.task = t
}

func (s *detailSwapStore) GetTaskByID(_ context.Context, id uuid.UUID) (store.Task, error) {
	s.mu.Lock()
	other, ok := s.others[id]
	t := s.task
	s.mu.Unlock()
	if ok {
		return other, nil
	}
	if t.ID != id {
		return store.Task{}, store.ErrNotFound
	}
	return t, nil
}

func (s *detailSwapStore) ListDependencies(context.Context, uuid.UUID, uuid.UUID) ([]store.TaskDependency, error) {
	return nil, nil
}

func (s *detailSwapStore) ListNotesForTask(context.Context, uuid.UUID, uuid.UUID) ([]store.Note, error) {
	return nil, nil
}

func (s *detailSwapStore) GetClaimByID(_ context.Context, id uuid.UUID) (store.Claim, error) {
	if s.claim.ID == id {
		return s.claim, nil
	}
	return store.Claim{}, store.ErrNotFound
}

func (s *detailSwapStore) LatestClaimForTask(context.Context, uuid.UUID, uuid.UUID) (store.Claim, bool, error) {
	return store.Claim{}, false, nil
}

func (s *detailSwapStore) GetEscalationEventByID(_ context.Context, id uuid.UUID) (store.EscalationEvent, error) {
	if s.esc.ID == id {
		return s.esc, nil
	}
	return store.EscalationEvent{}, store.ErrNotFound
}

func (s *detailSwapStore) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, nil
}

// detailSwapSpec is the spec read surface the detail composes. Everything
// this file's assertions do not touch is inherited from the embedded nil
// interface, so a read nobody modelled panics rather than answering a
// fabricated page.
type detailSwapSpec struct {
	specReadClient
	pid, mid uuid.UUID
}

func (s *detailSwapSpec) Products(context.Context) ([]store.Product, error) {
	return []store.Product{{ID: s.pid, Name: "Detail product"}}, nil
}

func (s *detailSwapSpec) Product(_ context.Context, id uuid.UUID) (store.Product, error) {
	if id != s.pid {
		return store.Product{}, store.ErrNotFound
	}
	return store.Product{ID: s.pid, Name: "Detail product"}, nil
}

func (s *detailSwapSpec) Delivery(_ context.Context, id uuid.UUID, _ []store.MilestoneStatus) (slice.DeliveryListing, error) {
	if id != s.pid {
		return slice.DeliveryListing{}, store.ErrNotFound
	}
	return slice.DeliveryListing{Milestones: []slice.MilestoneListingEntry{{ID: s.mid, Name: "Plain"}}}, nil
}

func (s *detailSwapSpec) MilestoneDeliversSlice(context.Context, uuid.UUID) (slice.Document, error) {
	return slice.Document{}, nil
}

// ---------------------------------------------------------------------------
// the fixture: the detail page and the intervention routes on one app
// ---------------------------------------------------------------------------

// detailSwapFixture is the GET detail route and the four intervention routes
// on ONE app, with a fake api the write actually reaches. newDetailFixture
// has the spec but no write client; newHtmxInterventionApp has the write
// client but no spec. A detail intervention needs both, which is why this
// fixture exists rather than reusing either.
type detailSwapFixture struct {
	pid, mid uuid.UUID
	// taskID is the task the page shows and the interventions act on.
	taskID uuid.UUID
	// otherID is a second, unrelated task the store can render, so a
	// return_to naming it resolves to a page rather than a failed read.
	otherID uuid.UUID

	store   *detailSwapStore
	spec    *detailSwapSpec
	app     *App
	mux     *http.ServeMux
	api     *fakeAPI
	session *http.Cookie

	// claimID is the claim the pre-write read holds.
	claimID uuid.UUID
}

// newDetailSwapFixture wires a claimed task (one attempt in) whose escalation
// leaves it escalated, unclaimed and at a DIFFERENT attempt count -- a value
// that appears in no request, so a response showing it can only have re-read
// the store.
func newDetailSwapFixture(t *testing.T) *detailSwapFixture {
	t.Helper()

	api := newFakeAPI(t)
	idp := newFakeIDP(t, uuid.NewString())
	authenticator, sessionCookie := newSignedInOperator(t, idp)
	app := newSignedInApp(t, authenticator, idp.server.URL, api.server.URL)

	f := &detailSwapFixture{
		pid:     uuid.New(),
		mid:     uuid.New(),
		taskID:  uuid.New(),
		otherID: uuid.New(),
		api:     api,
		app:     app,
		session: sessionCookie,
	}

	f.claimID = uuid.New()
	escalationID := uuid.New()
	f.store = &detailSwapStore{
		task: store.Task{
			ID: f.taskID, MilestoneID: f.mid, Title: "the detail's task",
			CurrentLane: store.LaneImplementation, AttemptCount: 1,
			CurrentClaimID: &f.claimID,
		},
		after: &store.Task{
			ID: f.taskID, MilestoneID: f.mid, Title: "the detail's task",
			CurrentLane: store.LaneImplementation, AttemptCount: 7,
			CurrentEscalationID: &escalationID,
		},
		others: map[uuid.UUID]store.Task{
			f.otherID: {
				ID: f.otherID, MilestoneID: f.mid,
				Title: "the other task", CurrentLane: store.LaneImplementation,
			},
		},
		claim: store.Claim{ID: f.claimID, TaskID: f.taskID, SessionID: store.SessionID(uuid.New())},
		esc:   store.EscalationEvent{ID: escalationID, TaskID: f.taskID, Reason: store.EscalationReasonManual},
	}
	f.spec = &detailSwapSpec{pid: f.pid, mid: f.mid}
	app.spec = f.spec
	app.tasks = f.store

	mux := newInterventionMux(app)
	mux.HandleFunc("GET /products/{pid}/tasks/{tid}", app.handleProductTaskDetail)
	f.mux = mux

	// The write lands: the store moves to the state the intervention
	// produced, so the response's re-read sees it. This stands in for the
	// store transition krill performs server-side.
	api.onRequest(func(req recordedRequest) (int, string) {
		if req.Path == "/sessions/init" {
			return 0, ""
		}
		f.store.applyAfter()
		return 0, ""
	})

	return f
}

// detailPath is the page's own address, the URL the detail route serves and
// the return_to every control on it carries.
func (f *detailSwapFixture) detailPath() string {
	return "/products/" + f.pid.String() + "/tasks/" + f.taskID.String()
}

// page is the detail section as the browser receives it: the bare fragment,
// which is what the page's first load and every swap agree on.
func (f *detailSwapFixture) page(t *testing.T) string {
	t.Helper()
	rec := hxGetWithSession(f.mux, f.detailPath(), f.session)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	return rec.Body.String()
}

// get is one direct GET through the fixture's mux with the operator's
// session attached.
func (f *detailSwapFixture) get(t *testing.T, target string) *httptest.ResponseRecorder {
	t.Helper()
	return hxGetWithSession(f.mux, target, f.session)
}

// hxGetWithSession is the GET an htmx control would issue, with the
// operator's session attached.
func hxGetWithSession(mux *http.ServeMux, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("HX-Request", "true")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// hxFormPostFromDetail is the form POST htmx issues for a control on the
// detail: an urlencoded body, HX-Request, and the detail section named as the
// swap target so the route can tell a detail origin from the confirm card's
// own form.
func hxFormPostFromDetail(mux *http.ServeMux, target string, form url.Values, targetID string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	req.Header.Set("HX-Target", targetID)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ---------------------------------------------------------------------------
// 1. escalating a claimed task: the fresh attempts, and no claim
// ---------------------------------------------------------------------------

// TestDetailEscalateShowsTheFreshAttemptsAndNoClaim is FR af61631d's most
// specific success assertion, driven through the real route with the form the
// page rendered.
//
// The store's post-write attempt count (7) appears in no request: the form
// carries the observed claim id and the return path, plus an optional reason.
// So a response showing "current attempt: 7" and no claim can only have
// re-read the task -- which is what the header, rail and callout swapping in
// place means.
func TestDetailEscalateShowsTheFreshAttemptsAndNoClaim(t *testing.T) {
	f := newDetailSwapFixture(t)

	// The page before the write, and the form IT rendered.
	before := f.page(t)
	assert.Contains(t, before, "current attempt: 1", "the page is served from the pre-write read")
	assert.Contains(t, before, "Claimed by", "and shows the claim the task holds")

	region := swapRegionBetween(t, before, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	action := formActionFor(t, region, actionEscalate)
	require.Equal(t, opsTaskActionBase+f.taskID.String()+"/"+actionEscalate, action)

	rec := hxFormPostFromDetail(f.mux, action, url.Values{
		"return_to":          {hiddenInputValue(t, region, "return_to")},
		expectedClaimIDParam: {hiddenInputValue(t, region, expectedClaimIDParam)},
		"reason":             {"needs a human"},
	}, pages.TaskDetailAnchor, f.session)

	require.Equal(t, http.StatusOK, rec.Code)
	got := rec.Body.String()

	// The region the operator's write swapped is the detail section, and the
	// fragment's root IS that region -- or the swap would delete the target
	// its own next action needs.
	assert.True(t, strings.HasPrefix(strings.TrimSpace(got), `<section id="`+pages.TaskDetailAnchor+`"`),
		"the answer must be the detail section itself; got: %s", headLine(got))

	// The success confirmation, out of band.
	assert.Contains(t, got, "Task escalated")
	assert.Contains(t, got, "hx-swap-oob")

	// The FR's assertion: the incremented attempts, from the fresh read --
	// and the claim gone with it.
	assert.Contains(t, got, "current attempt: 7",
		"escalating shows the attempts the write produced")
	assert.NotContains(t, got, "current attempt: 1",
		"and not the attempts the page was loaded with")
	assert.Contains(t, got, `data-krill-claim-id=""`,
		"the escalated task holds no claim, so the section names none")
	assert.NotContains(t, got, "Claimed by", "and the rail no longer attributes one")

	// The callout is re-derived too: escalating a claimed task does not
	// leave the claimed state's verbs on screen.
	swapped := swapRegionBetween(t, got, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	assert.Equal(t, []string{actionRequeue, actionCancel}, swapOfferedVerbs(t, swapped),
		"the callout must offer the escalated state's verbs, not the ones the page was loaded with")
	// And the reason popovers are rebuilt with it, one per fresh control. The
	// response replaced the whole section, so popovers left outside it would
	// be orphaned against ids the swap deleted -- the triggers re-rendered
	// here would open nothing.
	assert.Equal(t, 2, strings.Count(swapped, `data-krill="reason-popover"`),
		"the swapped callout must carry a reason popover for each of its fresh controls")

	// The intervention itself: the same route the rows use, carrying the id
	// the page observed and nothing about the operator.
	write := f.api.writeRequest(t)
	assert.Equal(t, "/tasks/"+f.taskID.String()+"/"+actionEscalate, write.Path)
	var body map[string]any
	require.NoError(t, json.Unmarshal(write.Body, &body))
	assert.Equal(t, f.claimID.String(), body[expectedClaimIDParam],
		"the write is guarded by the claim the page observed")
	assert.Equal(t, "needs a human", body["reason"],
		"the reason the detail's popover carries reaches the api's intervention body")
	for k := range body {
		assert.Contains(t, []string{"reason", expectedClaimIDParam, escalatedGuardField}, k,
			"a form carries the action's own argument and the observed guard; nothing else")
	}
	assertOperatorAttribution(t, f.api, "")
}

// ---------------------------------------------------------------------------
// 2. a refusal: inline in the detail, rebuilt from fresh state
// ---------------------------------------------------------------------------

// TestDetailRefusalStaysInlineAndCarriesTheFreshGuard raises the FR's guard
// clause at the detail's edge. The state moved since the page was read (the
// task now holds a different claim), so the write is refused; the refusal
// must ride inside the detail section as an alert -- never as a status code,
// an empty page or an assumed success -- and the controls it re-offers must
// carry the claim the task holds NOW, or a retry would be refused again
// against state the operator cannot see.
func TestDetailRefusalStaysInlineAndCarriesTheFreshGuard(t *testing.T) {
	f := newDetailSwapFixture(t)

	before := f.page(t)
	region := swapRegionBetween(t, before, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	action := formActionFor(t, region, actionRelease)
	stale := hiddenInputValue(t, region, expectedClaimIDParam)

	// The claim the operator saw is no longer the one the task holds.
	freshClaim := uuid.New()
	fresh := f.store.current()
	fresh.CurrentClaimID = &freshClaim
	f.store.setState(fresh)
	f.store.claim = store.Claim{ID: freshClaim, TaskID: f.taskID, SessionID: store.SessionID(uuid.New())}

	f.api.onRequest(guardLikeStore(map[string]string{expectedClaimIDParam: freshClaim.String()}))

	rec := hxFormPostFromDetail(f.mux, action, url.Values{
		"return_to":          {hiddenInputValue(t, region, "return_to")},
		expectedClaimIDParam: {stale},
	}, pages.TaskDetailAnchor, f.session)

	require.Equal(t, http.StatusOK, rec.Code, "a refusal is presented, not status-coded")
	got := rec.Body.String()

	assert.Contains(t, got, `id="`+pages.TaskDetailAnchor+`"`,
		"the refusal rides in the region the operator acted from")
	assert.Contains(t, got, `data-krill="task-action-error"`, "as an inline alert")
	assert.Contains(t, got, `role="alert"`)
	assert.Contains(t, got, "changed since the page was loaded",
		"the guard's own refusal reaches the operator in krill's own words")
	assert.NotContains(t, got, store.ErrObservedStateMismatch.Error(),
		"never as the store's package-qualified text (FR c69a42b4)")
	assert.NotContains(t, got, `data-krill="toast"`,
		"a refusal is not a success: no toast is raised")

	// The re-offered controls carry the fresh claim, never the refused one.
	rebuilt := swapRegionBetween(t, got, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	assert.Contains(t, rebuilt, `type="hidden" name="`+expectedClaimIDParam+`" value="`+freshClaim.String()+`"`,
		"the re-rendered controls are guarded against the state the operator now sees")
	assert.NotContains(t, rebuilt, `value="`+stale+`"`,
		"and never against the id the refused request carried")
}

// TestDetailCancelRefusalIsNotTheConfirmationCard pins the origin split the
// detail introduced. Every Cancel POSTs the same route, and the only thing
// that tells the confirm card's own form apart from a control on the page is
// the region htmx is swapping. A Cancel whose htmx half came from the detail
// must have its refusal re-derived INTO the detail; answering with the
// confirmation card would leave the operator looking at a fresh confirmation
// for a cancel they had already posted, on a page that no longer shows the
// task's state.
func TestDetailCancelRefusalIsNotTheConfirmationCard(t *testing.T) {
	f := newDetailSwapFixture(t)

	before := f.page(t)
	region := swapRegionBetween(t, before, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	posted := opsTaskActionBase + f.taskID.String() + "/" + actionCancel
	require.Contains(t, region, `hx-post="`+posted+`"`,
		"the Cancel control's htmx half posts the verb itself, not the confirm page")
	stale := hiddenInputValue(t, region, expectedClaimIDParam)

	// The claim the operator saw is no longer the one the task holds.
	freshClaim := uuid.New()
	fresh := f.store.current()
	fresh.CurrentClaimID = &freshClaim
	f.store.setState(fresh)
	f.store.claim = store.Claim{ID: freshClaim, TaskID: f.taskID, SessionID: store.SessionID(uuid.New())}
	f.api.onRequest(guardLikeStore(map[string]string{expectedClaimIDParam: freshClaim.String()}))

	rec := hxFormPostFromDetail(f.mux, posted, url.Values{
		"return_to":          {hiddenInputValue(t, region, "return_to")},
		expectedClaimIDParam: {stale},
	}, pages.TaskDetailAnchor, f.session)

	require.Equal(t, http.StatusOK, rec.Code)
	got := rec.Body.String()

	assert.Contains(t, got, `id="`+pages.TaskDetailAnchor+`"`,
		"the refusal re-derives the region the Cancel came from")
	assert.Contains(t, got, `data-krill="task-action-error"`)
	assert.Contains(t, got, "changed since the page was loaded")
	assert.NotContains(t, got, store.ErrObservedStateMismatch.Error(),
		"no store text reaches the browser (FR c69a42b4)")
	assert.NotContains(t, got, `id="`+pages.CancelConfirmAnchor+`"`,
		"a detail Cancel's refusal must not be answered with the card's own re-render")
}

// ---------------------------------------------------------------------------
// 3. a return_to naming another task is refused, not rendered
// ---------------------------------------------------------------------------
// TestDetailReturnToNamingAnotherTaskIsNotRendered pins the one way a
// detail-page response could describe a task the write never touched: the
// acting control's return_to is request-supplied, so a request can name a
// different task's detail while the write lands on its own. Both tasks
// resolve, so nothing but the guard would fail this. Rendering the other task
// would show the operator a page whose own controls post against a task they
// never chose, so the answer is the reload warning instead.
func TestDetailReturnToNamingAnotherTaskIsNotRendered(t *testing.T) {
	f := newDetailSwapFixture(t)

	before := f.page(t)
	region := swapRegionBetween(t, before, `data-krill="task-actions"`, `data-krill="task-detail-frame"`)
	action := formActionFor(t, region, actionEscalate)

	otherPath := "/products/" + f.pid.String() + "/tasks/" + f.otherID.String()
	// The other task renders -- so a response omitting it is the guard
	// working, not the other read failing.
	other := f.get(t, otherPath)
	require.Equal(t, http.StatusOK, other.Code)
	require.Contains(t, other.Body.String(), "the other task")

	rec := hxFormPostFromDetail(f.mux, action, url.Values{
		"return_to":          {otherPath},
		expectedClaimIDParam: {hiddenInputValue(t, region, expectedClaimIDParam)},
	}, pages.TaskDetailAnchor, f.session)

	require.Equal(t, http.StatusOK, rec.Code)
	got := rec.Body.String()

	assert.Contains(t, got, interventionReloadFailure,
		"a request naming another task is answered with the reload warning")
	assert.Contains(t, got, `data-krill="ops-inline-error"`)
	assert.NotContains(t, got, "the other task",
		"and never with the other task's detail, whose controls would post against a task the operator never chose")
	assert.NotContains(t, got, `id="`+pages.TaskDetailAnchor+`"`)
}
