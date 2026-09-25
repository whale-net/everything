// Handler-level coverage for the ops console's four read views, proving
// each matches its MCP/console query's data and paging behavior --
// including the cross-scope-token-rejection case (FR 2ba8a8e1) where a
// continuation token minted under one scope must be refused, never served
// as an empty or wrong-scope page.
//
// The store is faked with a narrow implementation that reproduces the real
// store's contract exactly where it matters to these views: it runs the
// genuine store.DecodeContinuationToken gate against the request's scope,
// so the cross-scope 400 is proven end-to-end through the view rather
// than asserted on a stub. ListClaimedTasks is implemented here (returning
// real rows) even though the production TaskStore.ListClaimedTasks is
// still a not-implemented stub pending issue #2916 -- these tests exercise
// the view's behavior, not the store's.
package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// fake store
// ---------------------------------------------------------------------------

// fakeOpsTasks is a narrow store.TaskStore: it embeds the (large) interface
// so only the four List methods the read views call are implemented. Every
// other method would nil-panic if a view ever called it, which is the point
// -- these tests must not pass by depending on store surface the views do
// not use.
//
// Token handling mirrors the real store: any caller-supplied continuation
// token is decoded against the query's own scope via the genuine
// store.DecodeContinuationToken, so a cross-scope or malformed token is
// rejected here exactly where the real store rejects it.
type fakeOpsTasks struct {
	store.TaskStore

	claimedPage   store.Page[store.ClaimedTaskRow]
	escalatedPage store.Page[store.EscalatedTaskRow]
	cancelledPage store.Page[store.CancelledTaskRow]
	notesPage     store.Page[store.OpenNoteRow]

	// recorded is the PageParams the most recent List call received, so a
	// test can assert the view passed page_size/page_token straight through.
	recorded store.PageParams
}

// checkToken runs the real store's scope-qualified decode gate.
func (f *fakeOpsTasks) checkToken(scopeID uuid.UUID, page store.PageParams) error {
	f.recorded = page
	if page.ContinuationToken == "" {
		return nil
	}
	_, err := store.DecodeContinuationToken(scopeID, page.ContinuationToken)
	return err
}

func (f *fakeOpsTasks) ListClaimedTasks(_ context.Context, p store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	if err := f.checkToken(p.ScopeID, p.Page); err != nil {
		return store.Page[store.ClaimedTaskRow]{}, err
	}
	return f.claimedPage, nil
}

func (f *fakeOpsTasks) ListEscalatedTasks(_ context.Context, p store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	if err := f.checkToken(p.ScopeID, p.Page); err != nil {
		return store.Page[store.EscalatedTaskRow]{}, err
	}
	return f.escalatedPage, nil
}

func (f *fakeOpsTasks) ListCancelledTasks(_ context.Context, p store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	if err := f.checkToken(p.ScopeID, p.Page); err != nil {
		return store.Page[store.CancelledTaskRow]{}, err
	}
	return f.cancelledPage, nil
}

func (f *fakeOpsTasks) ListOpenNotes(_ context.Context, p store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	if err := f.checkToken(p.ScopeID, p.Page); err != nil {
		return store.Page[store.OpenNoteRow]{}, err
	}
	return f.notesPage, nil
}

// fakeOpsScopes is the deployment's sole scope row (krill's seeder
// guarantees exactly one). It embeds the interface and implements GetSole,
// the only method the read views' soleScopeID calls.
type fakeOpsScopes struct {
	store.ScopeStore
	scope store.Scope
}

func (f fakeOpsScopes) GetSole(context.Context) (store.Scope, error) { return f.scope, nil }

// opsTestScope is the sole scope these views read under.
var opsTestScope = store.Scope{ID: uuid.MustParse("11111111-2222-3333-4444-555555555555")}

func newOpsApp(tasks *fakeOpsTasks) *App {
	return &App{
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  tasks,
	}
}

// serve issues one request to a read-view handler and returns the recorder.
// The auth gate is mounted by production (main.go) but is not what these
// tests cover, so the handler method is exercised directly; renderShell's
// nil-user guard renders an empty signed-in line without a session, which
// is irrelevant to the rendered view body these tests assert on.
func serve(h http.HandlerFunc, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func body(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	return rec.Body.String()
}

// aFixedTime is a stable, UTC instant so RFC3339 renderings are asserted
// literally rather than re-derived.
var aFixedTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

const aFixedTimeStr = "2026-01-02T03:04:05Z"

// ---------------------------------------------------------------------------
// 1. data parity -- each view renders the fields the console wire carries
// ---------------------------------------------------------------------------

func TestClaimedViewRendersConsoleFields(t *testing.T) {
	taskID := uuid.New()
	sessionID := uuid.New()
	tasks := &fakeOpsTasks{claimedPage: store.Page[store.ClaimedTaskRow]{
		Items: []store.ClaimedTaskRow{{
			TaskID:             taskID,
			Title:              "Fix the widget",
			DeliveryRef:        store.ClaimedTaskDeliveryRef{Kind: store.MilestoneKindMilestone, Title: "M6"},
			ClaimantSessionID:  store.SessionID(sessionID),
			ClaimantActing:     store.Subject{Iss: "https://kc", Sub: "alice", Kind: store.SubjectKindHuman},
			ClaimantOnBehalfOf: store.Subject{Iss: "https://svc", Sub: "swarm"},
			CurrentLane:        store.LaneTesting,
			LeaseExpiresAt:     aFixedTime,
			AttemptCount:       3,
		}},
	}}
	rec := serve(newOpsApp(tasks).handleClaimedTasks, opsClaimedPath)
	got := body(t, rec)

	// task id + title, delivery ref, claim session id, both claimant
	// subject pairs (acting shows its kind, on-behalf-of does not), lane,
	// lease expiry, and attempt count -- the full ClaimedTaskWire shape.
	assert.Contains(t, got, taskID.String())
	assert.Contains(t, got, "Fix the widget")
	assert.Contains(t, got, "milestone: M6")
	assert.Contains(t, got, sessionID.String(), "claimant's session id is rendered")
	assert.Contains(t, got, "https://kc alice (human)", "acting subject shows its kind")
	assert.Contains(t, got, "https://svc swarm", "on_behalf_of subject is rendered")
	assert.Contains(t, got, "Testing", "current lane is rendered")
	assert.Contains(t, got, aFixedTimeStr, "lease expiry is rendered")
	assert.Contains(t, got, "3", "attempt count is rendered")
}

func TestEscalatedViewRendersConsoleFields(t *testing.T) {
	taskID := uuid.New()
	counter, cap_ := 4, 5
	verdict := store.VerdictFail
	tasks := &fakeOpsTasks{escalatedPage: store.Page[store.EscalatedTaskRow]{
		Items: []store.EscalatedTaskRow{{
			TaskID:                taskID,
			Title:                 "Broken build",
			DeliveryRef:           store.EscalatedTaskDeliveryRef{Kind: store.MilestoneKindMilepebble, Title: "MP3"},
			Reason:                store.EscalationReasonThrashCap,
			CounterValue:          &counter,
			CapValue:              &cap_,
			Lane:                  store.LaneImplementation,
			EscalatedAt:           aFixedTime,
			EscalatedByActing:     store.Subject{Iss: "https://kc", Sub: "bob", Kind: store.SubjectKindHuman},
			EscalatedByOnBehalfOf: store.Subject{Iss: "https://svc", Sub: "watchdog"},
			AttemptCount:          4,
			FailingVerdictCount:   2,
			MostRecentVerdict:     &verdict,
			NoteCount:             1,
		}},
	}}
	rec := serve(newOpsApp(tasks).handleEscalatedTasks, opsEscalatedPath)
	got := body(t, rec)

	assert.Contains(t, got, taskID.String())
	assert.Contains(t, got, "Broken build")
	assert.Contains(t, got, "milepebble: MP3")
	assert.Contains(t, got, "thrash-cap", "escalation_reason is rendered")
	assert.Contains(t, got, "4/5", "counter/cap is rendered")
	assert.Contains(t, got, "Implementation", "lane is rendered")
	assert.Contains(t, got, aFixedTimeStr, "escalated-at is rendered")
	assert.Contains(t, got, "https://kc bob (human)", "escalating actor shows its kind")
	assert.Contains(t, got, "https://svc watchdog", "on_behalf_of subject is rendered")
	assert.Contains(t, got, "attempts 4 / failing 2 / notes 1", "summary counts are rendered")
	assert.Contains(t, got, "fail", "most-recent verdict is rendered")
}

func TestCancelledViewRendersConsoleFields(t *testing.T) {
	taskID := uuid.New()
	tasks := &fakeOpsTasks{cancelledPage: store.Page[store.CancelledTaskRow]{
		Items: []store.CancelledTaskRow{{
			TaskID:                taskID,
			Title:                 "Dead letter",
			DeliveryRef:           store.CancelledTaskDeliveryRef{Kind: store.MilestoneKindMilestone, Title: "M6"},
			CancelledByActing:     store.Subject{Iss: "https://kc", Sub: "carol", Kind: store.SubjectKindHuman},
			CancelledByOnBehalfOf: store.Subject{Iss: "https://svc", Sub: "operator"},
			CancelledAt:           aFixedTime,
		}},
	}}
	rec := serve(newOpsApp(tasks).handleCancelledTasks, opsCancelledPath)
	got := body(t, rec)

	assert.Contains(t, got, taskID.String())
	assert.Contains(t, got, "Dead letter")
	assert.Contains(t, got, "milestone: M6")
	assert.Contains(t, got, "https://kc carol (human)", "canceller shows its kind")
	assert.Contains(t, got, "https://svc operator", "on_behalf_of subject is rendered")
	assert.Contains(t, got, aFixedTimeStr, "cancelled-at is rendered")
}

func TestNotesViewRendersTargetID(t *testing.T) {
	taskID := uuid.New()
	noteID := uuid.New()
	entityID := uuid.New()
	tasks := &fakeOpsTasks{notesPage: store.Page[store.OpenNoteRow]{
		Items: []store.OpenNoteRow{
			{
				NoteID:      noteID,
				Kind:        store.NoteKindScopeNote,
				Body:        "needs a decision",
				CreatedAt:   aFixedTime,
				TaskContext: &store.OpenNoteTaskContext{TaskID: taskID, Title: "A task"},
			},
			{
				NoteID:        uuid.New(),
				Kind:          store.NoteKindComment,
				Body:          "spec comment",
				CreatedAt:     aFixedTime,
				EntityContext: &store.OpenNoteEntityContext{EntityKind: store.NoteEntityKindLoadBearingDecision, EntityID: entityID, Title: "A decision"},
			},
		},
	}}
	rec := serve(newOpsApp(tasks).handleOpenNotes, opsNotesPath)
	got := body(t, rec)

	// Both a task-targeted and an entity-targeted note name their target's
	// id alongside its title -- the FR that a note row identifies the exact
	// entity it points at.
	assert.Contains(t, got, noteID.String())
	assert.Contains(t, got, "scope-note", "note kind is rendered")
	assert.Contains(t, got, "needs a decision", "note body is rendered")
	assert.Contains(t, got, aFixedTimeStr, "note created-at is rendered")
	assert.Contains(t, got, taskID.String(), "task-targeted note names the target task id")
	assert.Contains(t, got, "A task")
	assert.Contains(t, got, entityID.String(), "entity-targeted note names the target entity id")
	assert.Contains(t, got, "A decision")
}

// ---------------------------------------------------------------------------
// 2. paging parity -- pass-through + next-page link
// ---------------------------------------------------------------------------

func TestViewPassesPagingParamsThroughAndLinksNextPage(t *testing.T) {
	// Two real continuation tokens minted under the view's own scope: the one
	// the request resumes with, and the one the store issues as NextToken for
	// the page returned. Both must survive the store's decode gate.
	incomingToken := store.EncodeContinuationToken(opsTestScope.ID, store.Cursor{SortKey: aFixedTimeStr, ID: uuid.New()})
	nextToken := store.EncodeContinuationToken(opsTestScope.ID, store.Cursor{SortKey: aFixedTimeStr, ID: uuid.New()})
	tasks := &fakeOpsTasks{claimedPage: store.Page[store.ClaimedTaskRow]{
		Items:     []store.ClaimedTaskRow{{TaskID: uuid.New(), Title: "row"}},
		NextToken: nextToken,
	}}
	rec := serve(newOpsApp(tasks).handleClaimedTasks, opsClaimedPath+"?page_size=2&page_token="+url.QueryEscape(incomingToken))
	got := body(t, rec)

	// The view passed the caller's page_size and page_token straight to the
	// store rather than re-deriving its own paging.
	assert.Equal(t, 2, tasks.recorded.PageSize, "page_size is passed through to the store")
	assert.Equal(t, incomingToken, tasks.recorded.ContinuationToken, "page_token is passed through to the store verbatim")

	// ...and rendered a next-page link carrying the page size forward and the
	// store-issued token as page_token.
	assert.Contains(t, got, "Next page")
	assert.Contains(t, got, "page_size=2")
	assert.Contains(t, got, url.QueryEscape(nextToken))
}

func TestViewOmitsNextLinkOnFinalPage(t *testing.T) {
	// A final page carries no NextToken; the view must show no next link.
	tasks := &fakeOpsTasks{claimedPage: store.Page[store.ClaimedTaskRow]{
		Items: []store.ClaimedTaskRow{{TaskID: uuid.New(), Title: "only row"}},
	}}
	rec := serve(newOpsApp(tasks).handleClaimedTasks, opsClaimedPath)
	got := body(t, rec)

	assert.NotContains(t, got, "Next page", "last page renders no next link")
	assert.Equal(t, 0, tasks.recorded.PageSize, "absent page_size is passed through as 0 (store default applies)")
}

func TestOpsNextHref(t *testing.T) {
	// opsNextHref is the view's link builder: empty on the last page, and on a
	// continuable page it carries the page size forward and the token.
	assert.Equal(t, "", opsNextHref(opsClaimedPath, "", 10))
	assert.Equal(t, opsClaimedPath+"?page_size=10&page_token=tok", opsNextHref(opsClaimedPath, "tok", 10))
	assert.Equal(t, opsClaimedPath+"?page_token=tok", opsNextHref(opsClaimedPath, "tok", 0))
}

// ---------------------------------------------------------------------------
// 3. cross-scope / malformed token rejection (FR 2ba8a8e1)
// ---------------------------------------------------------------------------

// TestViewRejectsCrossScopeAndMalformedTokens drives each of the four views
// with (a) a continuation token minted under a DIFFERENT scope and (b) a
// malformed token, and requires a 400 -- never a 200 that would silently
// serve an empty or wrong-scope page.
func TestViewRejectsCrossScopeAndMalformedTokens(t *testing.T) {
	otherScopeToken := store.EncodeContinuationToken(uuid.New(), store.Cursor{SortKey: aFixedTimeStr, ID: uuid.New()})

	views := map[string]struct {
		path    string
		handler func(*App) http.HandlerFunc
		tasks   *fakeOpsTasks
	}{
		"claimed": {
			path:    opsClaimedPath,
			handler: func(a *App) http.HandlerFunc { return a.handleClaimedTasks },
			tasks:   &fakeOpsTasks{claimedPage: store.Page[store.ClaimedTaskRow]{Items: []store.ClaimedTaskRow{{TaskID: uuid.New()}}}},
		},
		"escalated": {
			path:    opsEscalatedPath,
			handler: func(a *App) http.HandlerFunc { return a.handleEscalatedTasks },
			tasks:   &fakeOpsTasks{escalatedPage: store.Page[store.EscalatedTaskRow]{Items: []store.EscalatedTaskRow{{TaskID: uuid.New()}}}},
		},
		"cancelled": {
			path:    opsCancelledPath,
			handler: func(a *App) http.HandlerFunc { return a.handleCancelledTasks },
			tasks:   &fakeOpsTasks{cancelledPage: store.Page[store.CancelledTaskRow]{Items: []store.CancelledTaskRow{{TaskID: uuid.New()}}}},
		},
		"notes": {
			path:    opsNotesPath,
			handler: func(a *App) http.HandlerFunc { return a.handleOpenNotes },
			tasks:   &fakeOpsTasks{notesPage: store.Page[store.OpenNoteRow]{Items: []store.OpenNoteRow{{NoteID: uuid.New()}}}},
		},
	}

	for name, v := range views {
		t.Run(name+"/cross-scope-token", func(t *testing.T) {
			rec := serve(v.handler(newOpsApp(v.tasks)), v.path+"?page_token="+url.QueryEscape(otherScopeToken))
			assert.Equal(t, http.StatusBadRequest, rec.Code, "a token minted for another scope is the caller's error, not an empty page")
		})
		t.Run(name+"/malformed-token", func(t *testing.T) {
			rec := serve(v.handler(newOpsApp(v.tasks)), v.path+"?page_token=not-a-valid-token")
			assert.Equal(t, http.StatusBadRequest, rec.Code, "a malformed token is the caller's error, not an empty page")
		})
	}
}

// TestViewAcceptsSameScopeToken is the positive control for the rejection
// above: a token minted under the view's own scope resumes normally (200),
// so the 400 is specific to cross-scope/malformed tokens, not to tokens in
// general.
func TestViewAcceptsSameScopeToken(t *testing.T) {
	sameScopeToken := store.EncodeContinuationToken(opsTestScope.ID, store.Cursor{SortKey: aFixedTimeStr, ID: uuid.New()})
	tasks := &fakeOpsTasks{claimedPage: store.Page[store.ClaimedTaskRow]{
		Items: []store.ClaimedTaskRow{{TaskID: uuid.New(), Title: "resumed row"}},
	}}
	rec := serve(newOpsApp(tasks).handleClaimedTasks, opsClaimedPath+"?page_token="+url.QueryEscape(sameScopeToken))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, body(t, rec), "resumed row", "a valid same-scope token resumes and renders the page")
}

// TestViewRejectsInvalidPageSize covers the other caller-error the parse step
// owns: a non-numeric/negative page_size is a 400, matching the console
// query's parsePageSizeParam.
func TestViewRejectsInvalidPageSize(t *testing.T) {
	app := newOpsApp(&fakeOpsTasks{})
	rec := serve(app.handleClaimedTasks, opsClaimedPath+"?page_size=abc")
	assert.Equal(t, http.StatusBadRequest, rec.Code)

	rec = serve(app.handleClaimedTasks, opsClaimedPath+"?page_size=-1")
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}
