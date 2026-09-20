// Unit tests for AbandonTaskHandler (task_abandon.go, issue #2726's Testing
// section): POST /tasks/{id}/abandon is gated like every other write
// endpoint (FR9, NFR6) -- session-derived scope_id/subject-pair
// pass-through, the path's {id} carried through as TaskID, the body's
// claim_id/reason carried through, a request body naming a verdict
// rejected (FR9: "no verdict field exists on the abandon request"), method
// enforcement, and writeStoreError's mapping of AbandonClaim's own named
// rejections (store.ErrClaimNotCurrent, store.ErrAttemptCapExhausted,
// store.ErrNotFound). No Postgres dependency -- fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore; the response
// document's single-type-check against a real GET /tasks/{id} would live
// in a task_abandon_integration_test.go, mirroring task_complete_test.go's
// own doc comment on the identical work.Assembler limitation -- not added
// here since neither task_reclaim.go (the other #2726 sibling) needs one.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// doAbandonTaskRequest mounts handler behind handlers.RequireSession(sessions)
// at "POST /probe/{id}/abandon" -- mirroring doCompleteTaskRequest's own
// precedent (task_complete_test.go).
func doAbandonTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/abandon", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/abandon", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAbandonTaskHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /tasks/{id}/abandon with no X-Krill-Session-Id header is rejected
// before the store is ever called.
func TestAbandonTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, "", uuid.New().String(),
		`{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotAbandonParams.ScopeID, "a request with no session header must never reach the store")
}

// TestAbandonTaskHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before AbandonClaim is ever called.
func TestAbandonTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid",
		`{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotAbandonParams.ScopeID, "a validation failure must never reach the store")
}

// TestAbandonTaskHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestAbandonTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/abandon", nil)
	rec := httptest.NewRecorder()
	handlers.AbandonTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestAbandonTaskHandler_BodyNamingAVerdict_Rejected is FR9's core
// guarantee at the HTTP layer: a request body carrying a "verdict" field --
// reporting an outcome -- is rejected by decodeStrict's unknown-field
// check, never silently ignored, and never reaches the store.
func TestAbandonTaskHandler_BodyNamingAVerdict_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"claim_id":"`+uuid.New().String()+`","verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotAbandonParams.ScopeID, "a request body naming a verdict must never reach the store")
}

// TestAbandonTaskHandler_ParamsPassThroughFromSessionAndBody proves
// NFR6/NFR3: scope_id and both LB4 subjects come from the session, TaskID
// from the path's {id}, and ClaimID/Reason from the request body.
func TestAbandonTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	claimID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"claim_id":"`+claimID.String()+`","reason":"switching to a different task"}`)

	assert.Equal(t, scopeID, tasks.gotAbandonParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotAbandonParams.TaskID, "TaskID must come from the path's {id}")
	assert.Equal(t, claimID, tasks.gotAbandonParams.ClaimID, "ClaimID must come from the request body")
	gotReason := tasks.gotAbandonParams.Reason
	if assert.NotNil(t, gotReason) {
		assert.Equal(t, "switching to a different task", *gotReason)
	}
	assert.NotEmpty(t, tasks.gotAbandonParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotAbandonParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestAbandonTaskHandler_NoReason_Optional proves Reason is optional: an
// absent "reason" field passes through as nil, not a validation failure --
// the store is still reached (unlike the malformed-body/no-session cases
// above, which never reach it).
func TestAbandonTaskHandler_NoReason_Optional(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"claim_id":"`+uuid.New().String()+`"}`)

	assert.Equal(t, scopeID, tasks.gotAbandonParams.ScopeID, "the store must be reached with an absent (optional) reason")
	assert.Nil(t, tasks.gotAbandonParams.Reason, "an absent reason must pass through as nil")
}

// TestAbandonTaskHandler_StoreRejection_MappedToStatus proves writeStoreError
// maps AbandonClaim's own named rejections onto the right status:
// ErrClaimNotCurrent (a stale, foreign, or already-released claim) and
// ErrAttemptCapExhausted to 409, ErrNotFound to 400 -- never a 500 for any
// of them.
func TestAbandonTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"claim not current":     {store.ErrClaimNotCurrent, http.StatusConflict},
		"attempt cap exhausted": {store.ErrAttemptCapExhausted, http.StatusConflict},
		"not found":             {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{abandonErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doAbandonTaskRequest(t, handlers.AbandonTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
				`{"claim_id":"`+uuid.New().String()+`"}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
