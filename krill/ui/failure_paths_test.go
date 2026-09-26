package main

import (
	"context"
	"errors"
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

// TestOpsQueryFailureDistinguishesCallerErrorFromOutage is the other half:
// a cross-scope or malformed continuation token is the caller's own
// mistake, so the no-JS branch keeps its 400 and says what was wrong --
// while a genuine outage stays a 500 with a generic message.
func TestOpsQueryFailureDistinguishesCallerErrorFromOutage(t *testing.T) {
	caller := &App{
		scopes: fakeOpsScopes{scope: opsTestScope},
		tasks:  failingOpsTasks{err: store.ErrInvalidContinuationToken},
	}
	rec := serve(caller.handleClaimedTasks, opsClaimedPath+"?page_token=not-a-real-token")
	assert.Equal(t, http.StatusBadRequest, rec.Code, "a bad token is the caller's error, not an outage")
	assert.Contains(t, rec.Body.String(), "token", "the caller is told which token was wrong")

	outage := &App{
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
	app := &App{scopes: failingOpsScopes{}, tasks: &fakeOpsTasks{}}

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
	app := &App{scopes: fakeOpsScopes{scope: opsTestScope}, tasks: failingOpsTasks{err: errors.New("boom")}}

	req := httptest.NewRequest(http.MethodGet, opsClaimedPath, nil)
	rec := httptest.NewRecorder()
	app.renderInterventionResults(rec, req, opsClaimedPath, "")

	out := rec.Body.String()
	assert.NotContains(t, out, "No claimed tasks.",
		"a read failure must never render as an empty view -- the operator would read it as the write having emptied the console")
	assert.Contains(t, out, "could not be reloaded")
	assert.Contains(t, out, "alert-error")
}
