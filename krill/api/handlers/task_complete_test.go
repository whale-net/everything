// Unit tests for CompleteTaskHandler (task_complete.go, issue #2725's
// Testing section): POST /tasks/{id}/complete is gated like every other
// write endpoint (FR8, NFR6) -- session-derived scope_id/subject-pair
// pass-through, the path's {id} carried through as TaskID, the body's
// claim_id/verdict/summary carried through, a request body naming a lane
// rejected (FR8), method enforcement, and writeCompleteStoreError's
// mapping of CompleteTask's own named rejections (store.ErrClaimNotCurrent,
// store.ErrUnknownVerdict). No Postgres dependency -- fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore; the response
// document's single-type-check against a real GET /tasks/{id} lives in
// task_complete_integration_test.go instead, mirroring task_claim_test.go's
// own doc comment on the identical work.Assembler limitation.
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

// doCompleteTaskRequest mounts handler behind
// handlers.RequireSession(sessions) at "POST /probe/{id}/complete" --
// mirroring doClaimTaskRequest's own precedent (task_claim_test.go).
func doCompleteTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/complete", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/complete", strings.NewReader(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestCompleteTaskHandler_NoSessionHeader_Rejected proves NFR6's write
// gate: POST /tasks/{id}/complete with no X-Krill-Session-Id header is
// rejected before the store is ever called.
func TestCompleteTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCompleteTaskRequest(t, handlers.CompleteTaskHandler(tasks, assembler), sessions, "", uuid.New().String(),
		`{"claim_id":"`+uuid.New().String()+`","verdict":"pass"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCompleteParams.ScopeID, "a request with no session header must never reach the store")
}

// TestCompleteTaskHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before CompleteTask is ever called.
func TestCompleteTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCompleteTaskRequest(t, handlers.CompleteTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid",
		`{"claim_id":"`+uuid.New().String()+`","verdict":"pass"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCompleteParams.ScopeID, "a validation failure must never reach the store")
}

// TestCompleteTaskHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestCompleteTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/complete", nil)
	rec := httptest.NewRecorder()
	handlers.CompleteTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestCompleteTaskHandler_BodyNamingALane_Rejected is FR8's core
// guarantee at the HTTP layer: a request body carrying a "lane" field --
// naming or choosing a destination -- is rejected by decodeStrict's
// unknown-field check, never silently ignored, and never reaches the
// store.
func TestCompleteTaskHandler_BodyNamingALane_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doCompleteTaskRequest(t, handlers.CompleteTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
		`{"claim_id":"`+uuid.New().String()+`","verdict":"pass","lane":"Done"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotCompleteParams.ScopeID, "a request body naming a lane must never reach the store")
}

// TestCompleteTaskHandler_ParamsPassThroughFromSessionAndBody proves
// NFR6/NFR3: scope_id and both LB4 subjects come from the session,
// TaskID from the path's {id}, and ClaimID/Verdict/Summary from the
// request body.
func TestCompleteTaskHandler_ParamsPassThroughFromSessionAndBody(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	claimID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doCompleteTaskRequest(t, handlers.CompleteTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String(),
		`{"claim_id":"`+claimID.String()+`","verdict":"fail","summary":"did not work"}`)

	assert.Equal(t, scopeID, tasks.gotCompleteParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotCompleteParams.TaskID, "TaskID must come from the path's {id}")
	assert.Equal(t, claimID, tasks.gotCompleteParams.ClaimID, "ClaimID must come from the request body")
	assert.Equal(t, store.VerdictFail, tasks.gotCompleteParams.Verdict, "Verdict must come from the request body")
	gotSummary := tasks.gotCompleteParams.Summary
	if assert.NotNil(t, gotSummary) {
		assert.Equal(t, "did not work", *gotSummary)
	}
	assert.NotEmpty(t, tasks.gotCompleteParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotCompleteParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestCompleteTaskHandler_StoreRejection_MappedToStatus proves
// writeCompleteStoreError maps CompleteTask's own named FR8 rejections
// onto the right status: ErrClaimNotCurrent (a stale or foreign claim) to
// 409, ErrUnknownVerdict to 400 -- never a 500 for either.
func TestCompleteTaskHandler_StoreRejection_MappedToStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		want int
	}{
		"claim not current": {store.ErrClaimNotCurrent, http.StatusConflict},
		"unknown verdict":   {store.ErrUnknownVerdict, http.StatusBadRequest},
		"not found":         {store.ErrNotFound, http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{completeErr: tc.err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doCompleteTaskRequest(t, handlers.CompleteTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String(),
				`{"claim_id":"`+uuid.New().String()+`","verdict":"pass"}`)

			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}
