package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

// ---------------------------------------------------------------------------
// The failure paths an htmx caller can reach
//
// Every path here is reachable by an hx-* attribute, and htmx does not
// swap on a non-2xx -- so a bare http.Error leaves the operator with an
// unchanged page and no explanation. The claimed view's case is the worst
// of them: its poll re-requests this route every few seconds, so a
// database blip would freeze the console silently while the poll kept
// firing.
// ---------------------------------------------------------------------------

// failingOpsTasks makes every List call fail, standing in for a store
// outage rather than an empty result.
type failingOpsTasks struct {
	store.TaskStore
	err error
}

// CountEscalatedTasks fails with everything else, which is the point: the
// chrome renders its badge from this read, and a page must still render
// when it cannot.
func (f failingOpsTasks) CountEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (int, error) {
	return 0, f.err
}

func (f failingOpsTasks) ListClaimedTasks(context.Context, store.ListClaimedTasksParams) (store.Page[store.ClaimedTaskRow], error) {
	return store.Page[store.ClaimedTaskRow]{}, f.err
}

func (f failingOpsTasks) ListEscalatedTasks(context.Context, store.ListEscalatedTasksParams) (store.Page[store.EscalatedTaskRow], error) {
	return store.Page[store.EscalatedTaskRow]{}, f.err
}

func (f failingOpsTasks) ListCancelledTasks(context.Context, store.ListCancelledTasksParams) (store.Page[store.CancelledTaskRow], error) {
	return store.Page[store.CancelledTaskRow]{}, f.err
}

func (f failingOpsTasks) ListOpenNotes(context.Context, store.ListOpenNotesParams) (store.Page[store.OpenNoteRow], error) {
	return store.Page[store.OpenNoteRow]{}, f.err
}

// failingOpsScopes makes the scope lookup itself fail, which is the other
// way a view cannot be built at all.
type failingOpsScopes struct {
	store.ScopeStore
}

func (failingOpsScopes) GetSole(context.Context) (store.Scope, error) {
	return store.Scope{}, errors.New("scope lookup unavailable")
}

// TestOpsQueryFailureIsVisibleToHtmx pins the htmx half of
// writeOpsQueryError: 200, the message inline, and no empty view
// masquerading as a successful read.
func TestOpsQueryFailureIsVisibleToHtmx(t *testing.T) {
	boom := errors.New("connection refused")
	app := &App{
		spec:   emptyScopeSpecReader{},
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  failingOpsTasks{err: boom},
	}

	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		target  string
	}{
		{"claimed", app.handleClaimedTasks, opsClaimedPath},
		{"escalated", app.handleEscalatedTasks, opsEscalatedPath},
		{"cancelled", app.handleCancelledTasks, opsCancelledPath},
		{"notes", app.handleOpenNotes, opsNotesPath},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := serveHX(tc.handler, tc.target)

			assert.Equal(t, http.StatusOK, rec.Code,
				"an htmx caller must get 200; a non-2xx is not swapped, so the operator would see nothing")
			out := rec.Body.String()
			assert.Contains(t, out, "Failed to load console data",
				"the failure must be stated inline, not swallowed")
			assert.Contains(t, out, "alert-error", "the message rides in the shared alert primitive")
			assert.NotContains(t, out, "connection refused",
				"the underlying cause is logged, not rendered: it can carry an internal address")
		})
	}
}

// TestOpsQueryErrorNeverShowsStoreText is the promise an operator can
// actually check: whatever a console view's query fails with, the bytes
// that reach the browser never carry the store's own error string.
//
// The store's sentinels are package-qualified -- "krill/store: malformed
// continuation token" -- so handing err.Error() to the template puts an
// internal Go package path on the page and tells the operator nothing they
// can act on. A genuine failure is worse: it can carry a connection
// string or an internal address. Both are checked here on BOTH halves of
// the branch, since the htmx fragment and the in-shell page are separate
// writes of the same message.
//
// The sentinels arrive wrapped here, the way store/paging.go actually
// returns them, so a fix that only handled the bare sentinel would not
// pass.
func TestOpsQueryErrorNeverShowsStoreText(t *testing.T) {
	outage := errors.New("dial tcp 10.0.3.17:5432: connection refused")

	for _, tc := range []struct {
		name    string
		err     error
		status  int
		wantSay string
	}{
		{
			name:    "cross-scope token",
			err:     store.ErrTokenScopeMismatch,
			status:  http.StatusBadRequest,
			wantSay: "page_token",
		},
		{
			name:    "malformed token, wrapped as the store wraps it",
			err:     fmt.Errorf("%w: malformed cursor sort key", store.ErrInvalidContinuationToken),
			status:  http.StatusBadRequest,
			wantSay: "page_token",
		},
		{
			name:    "genuine store failure",
			err:     outage,
			status:  http.StatusInternalServerError,
			wantSay: "Failed to load console data",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, view := range []struct {
				name    string
				handler http.HandlerFunc
				path    string
			}{
				{"claimed", (&App{spec: emptyScopeSpecReader{}, scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: tc.err}}).handleClaimedTasks, opsClaimedPath},
				{"escalated", (&App{spec: emptyScopeSpecReader{}, scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: tc.err}}).handleEscalatedTasks, opsEscalatedPath},
				{"cancelled", (&App{spec: emptyScopeSpecReader{}, scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: tc.err}}).handleCancelledTasks, opsCancelledPath},
				{"notes", (&App{spec: emptyScopeSpecReader{}, scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: tc.err}}).handleOpenNotes, opsNotesPath},
			} {
				t.Run(view.name, func(t *testing.T) {
					// A token in the URI so the rejection is reachable
					// the way an operator reaches it -- paged forward,
					// then reloaded with a token this view cannot use.
					target := view.path + "?page_token=not-a-real-token"

					for _, mode := range []struct {
						name  string
						serve func(http.HandlerFunc, string) *httptest.ResponseRecorder
						htmx  bool
					}{
						{"browser", serve, false},
						{"htmx", serveHX, true},
					} {
						t.Run(mode.name, func(t *testing.T) {
							rec := mode.serve(view.handler, target)
							out := rec.Body.String()

							assert.NotContains(t, out, "krill/store:",
								"no package-qualified store text reaches the operator, on either half of the branch")
							assert.NotContains(t, out, "connection refused",
								"a store error can carry an internal address; it is logged, not rendered")
							assert.NotContains(t, out, "10.0.3.17",
								"nor the address it was carrying")
							assert.Contains(t, out, tc.wantSay,
								"the operator still gets a message they can act on")
							assert.Contains(t, out, "alert-error",
								"the message rides in the shared alert primitive")

							if mode.htmx {
								// The 200-re-render rule: htmx does not
								// swap on a non-2xx, so the status is
								// always 200 and the failure rides inside.
								assert.Equal(t, http.StatusOK, rec.Code)
							} else {
								assert.Equal(t, tc.status, rec.Code,
									"a caller error is a 400 and a genuine failure is a 500")
							}
						})
					}
				})
			}
		})
	}
}

// TestOpsQueryErrorKeepsTheNav is the other half of what the operator gets
// instead of a bare http.Error: a browser answer rendered through the
// shell, so the nav is still there on the page they most need to
// navigate away from. A text/plain body has no nav and no recovery link.
func TestOpsQueryErrorKeepsTheNav(t *testing.T) {
	app := &App{
		spec:   emptyScopeSpecReader{},
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  failingOpsTasks{err: store.ErrTokenScopeMismatch},
	}
	rec := serve(app.handleClaimedTasks, opsClaimedPath+"?page_size=25&page_token=not-a-real-token")

	out := rec.Body.String()
	assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"),
		"a rendered page, not http.Error's text/plain")
	assert.Contains(t, out, `data-krill="primary-nav"`,
		"the response renders through the shell, so the operator keeps the nav")
	assert.Contains(t, out, `data-krill="ops-query-error"`, "and it is the console's own error body")
	assert.Contains(t, out, "Ops console", "the nav link for the area the operator was on is still present")
	assert.Contains(t, out, `href="`+opsClaimedPath+`?page_size=25"`,
		"the recovery link is the view's own URI with the rejected token dropped -- following it is itself the fix, and it keeps the page_size the operator chose")
}

// TestOpsRecoveryPathDropsOnlyTheToken pins the recovery path's own rule:
// the rejected token goes, everything else the operator set stays, and a
// request with no token is passed through unchanged.
func TestOpsRecoveryPathDropsOnlyTheToken(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   string
	}{
		{opsClaimedPath + "?page_token=tok", opsClaimedPath},
		{opsClaimedPath + "?page_size=25&page_token=tok", opsClaimedPath + "?page_size=25"},
		{opsClaimedPath + "?page_token=tok&page_size=25", opsClaimedPath + "?page_size=25"},
		{opsClaimedPath + "?page_size=25", opsClaimedPath + "?page_size=25"},
		{opsClaimedPath, opsClaimedPath},
	} {
		t.Run(tc.target, func(t *testing.T) {
			got := opsRecoveryPath(httptest.NewRequest(http.MethodGet, tc.target, nil))
			assert.Equal(t, tc.want, got)
		})
	}

	assert.Equal(t, opsPath, opsRecoveryPath(nil), "a nil request recovers to the console root rather than panicking")
}

// TestOpsQueryFailureDistinguishesCallerErrorFromOutage is the other half:
// a cross-scope or malformed continuation token is the caller's own
// mistake, so the no-JS branch keeps its 400 and says what was wrong --
// while a genuine outage stays a 500 with a generic message.
func TestOpsQueryFailureDistinguishesCallerErrorFromOutage(t *testing.T) {
	caller := &App{
		spec:   emptyScopeSpecReader{},
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  failingOpsTasks{err: store.ErrInvalidContinuationToken},
	}
	rec := serve(caller.handleClaimedTasks, opsClaimedPath+"?page_token=not-a-real-token")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a bad token is the caller's error, not an outage")
	assert.Contains(t, rec.Body.String(), "token", "the caller is told which token was wrong")

	outage := &App{
		spec:   emptyScopeSpecReader{},
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  failingOpsTasks{err: errors.New("connection refused")},
	}
	rec = serve(outage.handleClaimedTasks, opsClaimedPath)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "an outage is not the caller's error")
	assert.NotContains(t, rec.Body.String(), "connection refused",
		"a store error can carry an internal address, so it is logged rather than rendered")
}

// TestOpsQueryFailureBeforeAViewExists covers the scope lookup failing, so
// the handler never got far enough to know which view it was building.
// OpsInlineError is view-agnostic for exactly that reason.
func TestOpsQueryFailureBeforeAViewExists(t *testing.T) {
	app := &App{spec: emptyScopeSpecReader{}, scopes: failingOpsScopes{}, tasks: &fakeOpsTasks{}}

	rec := serveHX(app.handleClaimedTasks, opsClaimedPath)

	assert.Equal(t, http.StatusOK, rec.Code)
	out := rec.Body.String()
	assert.Contains(t, out, `data-krill="ops-inline-error"`)
	assert.Contains(t, out, "Failed to load console data")
}

// TestInterventionReReadFailureNeverLooksEmpty guards the other direction:
// after a write that DID succeed, a failed re-read must not render a
// confident "No claimed tasks." -- indistinguishable from success, and it
// would also silently stop the poll.
func TestInterventionReReadFailureNeverLooksEmpty(t *testing.T) {
	app := &App{spec: emptyScopeSpecReader{}, scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: errors.New("boom")}}

	req := httptest.NewRequest(http.MethodGet, opsClaimedPath, nil)
	rec := httptest.NewRecorder()
	app.renderInterventionResults(rec, req, opsClaimedPath, "", "")

	out := rec.Body.String()
	assert.NotContains(t, out, "No claimed tasks.",
		"a read failure must never render as an empty view -- the operator would read it as the write having emptied the console")
	assert.Contains(t, out, "could not be reloaded")
	assert.Contains(t, out, "alert-error")
}
