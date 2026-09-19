// Unit tests for ClaimTaskHandler (task_claim.go, issue #2722's Testing
// section): POST /tasks/{id}/claim is gated like every other write endpoint
// (FR3, NFR6) -- session-derived scope_id/subject-pair pass-through, the
// path's {id} carried through as TaskID, method enforcement, and
// writeStoreError's mapping of ClaimTask's named FR3/FR5/FR7 rejections
// (store.ErrTaskAlreadyClaimed/ErrDependenciesUnsatisfied/
// ErrAttemptCapExhausted) onto 409. No Postgres dependency --
// fakeTaskStore (fake_task_store_test.go) stands in for store.TaskStore;
// krill/store/task_claim_integration_test.go covers the real store-level
// branch coverage (including the real-Postgres concurrency race).
//
// The handler always calls assembler.Assemble after a successful
// ClaimTask, and work.Assembler wraps a concrete *slice.Querier over a
// real *store.Store, not an interface a fake can stand in for (mirroring
// task_payload_test.go's own doc comment on this exact limitation) -- so
// the genuine 200-with-payload success path, and the claim-response-vs-
// subsequent-GET shape-equality check, live in
// task_claim_integration_test.go instead. What this file proves about the
// success path is narrower but still real: fakeTaskStore.ClaimTask is
// actually invoked with the session- and path-derived params the gate is
// supposed to enforce, before Assemble ever runs.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// doClaimTaskRequest mounts handler behind handlers.RequireSession(sessions)
// at "POST /probe/{id}/claim" -- exactly like routes.go's own wiring --
// mirroring doTaskDependenciesPostRequest's precedent.
func doClaimTaskRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/claim", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/claim", nil)
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestClaimTaskHandler_NoSessionHeader_Rejected proves NFR6's write gate:
// POST /tasks/{id}/claim with no X-Krill-Session-Id header is rejected
// before the store is ever called.
func TestClaimTaskHandler_NoSessionHeader_Rejected(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doClaimTaskRequest(t, handlers.ClaimTaskHandler(tasks, assembler), sessions, "", uuid.New().String())

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotClaimParams.ScopeID, "a request with no session header must never reach the store")
}

// TestClaimTaskHandler_InvalidID_Returns400 proves a malformed {id} is
// rejected before ClaimTask is ever called.
func TestClaimTaskHandler_InvalidID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doClaimTaskRequest(t, handlers.ClaimTaskHandler(tasks, assembler), sessions, sessionIDStr, "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, tasks.gotClaimParams.ScopeID, "a validation failure must never reach the store")
}

// TestClaimTaskHandler_WrongMethod_Returns405 proves GET (or any verb
// other than POST) against this handler is rejected.
func TestClaimTaskHandler_WrongMethod_Returns405(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String()+"/claim", nil)
	rec := httptest.NewRecorder()
	handlers.ClaimTaskHandler(tasks, assembler)(rec, req)

	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, rec.Body.String())
}

// TestClaimTaskHandler_ParamsPassThroughFromSessionAndPath proves NFR6/
// NFR3: scope_id and both LB4 subjects come from the session, the session
// id from the session, and TaskID from the path's {id} -- never the
// request body (this endpoint has none). The final HTTP status is not
// asserted here: fakeTaskStore.ClaimTask succeeds, but the handler's
// follow-up assembler.Assemble call cannot build a real payload without
// Postgres (see this file's own doc comment) -- task_claim_integration_
// test.go proves the genuine 200 path. What matters here is that ClaimTask
// itself was called with exactly the right params before that happens.
func TestClaimTaskHandler_ParamsPassThroughFromSessionAndPath(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	taskID := uuid.New()
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	doClaimTaskRequest(t, handlers.ClaimTaskHandler(tasks, assembler), sessions, sessionIDStr, taskID.String())

	assert.Equal(t, scopeID, tasks.gotClaimParams.ScopeID, "NFR6: scope_id must come from the session, not the request body")
	assert.Equal(t, taskID, tasks.gotClaimParams.TaskID, "TaskID must come from the path's {id}")
	assert.NotEqual(t, uuid.Nil, uuid.UUID(tasks.gotClaimParams.SessionID), "the session id must come from the session")
	assert.NotEmpty(t, tasks.gotClaimParams.Acting.Sub, "NFR3: the acting subject must come from the session")
	assert.NotEmpty(t, tasks.gotClaimParams.OnBehalfOf.Sub, "NFR3: the on-behalf-of subject must come from the session")
}

// TestClaimTaskHandler_StoreRejection_Returns409 proves writeStoreError
// maps ClaimTask's own named FR3/FR5/FR7 rejections (store.
// ErrTaskAlreadyClaimed/ErrDependenciesUnsatisfied/ErrAttemptCapExhausted)
// onto a 409 -- the task exists, but is not claimable right now, never a
// 400 or a 500.
func TestClaimTaskHandler_StoreRejection_Returns409(t *testing.T) {
	for name, err := range map[string]error{
		"already claimed":          store.ErrTaskAlreadyClaimed,
		"dependencies unsatisfied": store.ErrDependenciesUnsatisfied,
		"attempt cap exhausted":    store.ErrAttemptCapExhausted,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			tasks := &fakeTaskStore{claimErr: err}
			assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

			rec := doClaimTaskRequest(t, handlers.ClaimTaskHandler(tasks, assembler), sessions, sessionIDStr, uuid.New().String())

			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		})
	}
}
