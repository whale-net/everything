// Unit tests for GetTaskPayloadHandler (task_payload.go, issue #2721's
// Testing section): GET /tasks/{id} is mounted with no RequireSession
// wrapper (routes.go), so these tests drive it directly with no session
// header and never expect a 401 -- mirroring
// task_dependency_test.go's doTaskDependenciesGetRequest precedent for the
// package's other ungated read endpoint. fakeTaskStore
// (fake_task_store_test.go) stands in for store.TaskStore for the two
// branches that never need to reach a real assembled slice (bad id,
// not-found), so none of this needs Postgres.
//
// The handler's own error-mapping distinguishes two GetTaskByID calls:
// the handler's own outer lookup (used to resolve {id}'s scope before
// calling Assemble) and the one work.Assembler.Assemble performs
// internally against its own tasks field -- TestGetTaskPayloadHandler_
// AssembleNotFound_Returns404 below exercises the second by giving the
// assembler a different fake than the handler's own outer tasks argument,
// proving both paths map to 404 without ever needing a real
// slice.Querier (Assemble returns before it would call one -- see
// krill/work/payload_test.go's own doc comment for why that's safe here
// too).
//
// The full success path -- a real 200 carrying the actual payload
// document, proving the read is ungated and returns the milepebble's real
// spec-slice content -- needs a real assembled slice (work.Assembler wraps
// a concrete *slice.Querier over *store.Store, not an interface a fake can
// stand in for), so it lives in task_payload_integration_test.go instead,
// alongside the claimed-by-someone-else/previously-abandoned note this
// issue's Testing section defers to #2722/#2726.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// doGetTaskPayloadRequest mounts handler at "GET /probe/{id}" with no
// session-gating wrapper at all -- GET /tasks/{id} is ungated (NFR6's
// gate is write-only) -- and deliberately sends no X-Krill-Session-Id
// header, mirroring doTaskDependenciesGetRequest's precedent.
func doGetTaskPayloadRequest(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}", handler)

	req := httptest.NewRequest(http.MethodGet, "/probe/"+id, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestGetTaskPayloadHandler_InvalidID_Returns400 proves a malformed {id}
// is rejected before any store is ever touched.
func TestGetTaskPayloadHandler_InvalidID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doGetTaskPayloadRequest(t, handlers.GetTaskPayloadHandler(tasks, assembler), "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestGetTaskPayloadHandler_UnknownTaskID_Returns404 proves an id naming
// no task row at all -- the handler's own outer GetTaskByID lookup, used
// to resolve {id}'s scope -- is reported as 404, never a 500 or an empty
// 200, and never even reaches Assemble.
func TestGetTaskPayloadHandler_UnknownTaskID_Returns404(t *testing.T) {
	tasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}
	// slice.NewQuerier(nil) is safe: the outer lookup fails before the
	// handler ever calls assembler.Assemble.
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	rec := doGetTaskPayloadRequest(t, handlers.GetTaskPayloadHandler(tasks, assembler), uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestGetTaskPayloadHandler_NoSessionHeader_StillReachesTheStore proves
// GET /tasks/{id} is genuinely ungated (FR10, NFR6): a request with no
// X-Krill-Session-Id header at all still reaches the outer store lookup
// (proven here by the outer fake's error surfacing as 404, not 401) --
// exactly the read this issue's Testing section describes as "succeeds
// with no session header".
func TestGetTaskPayloadHandler_NoSessionHeader_StillReachesTheStore(t *testing.T) {
	tasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}
	assembler := work.NewAssembler(tasks, slice.NewQuerier(nil))

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}", handlers.GetTaskPayloadHandler(tasks, assembler))

	req := httptest.NewRequest(http.MethodGet, "/probe/"+uuid.New().String(), nil)
	require.Empty(t, req.Header.Get("X-Krill-Session-Id"), "this request must carry no session header")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	assert.NotEqual(t, http.StatusUnauthorized, rec.Code, "an ungated read must never 401 for a missing session header")
	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestGetTaskPayloadHandler_AssembleNotFound_Returns404 proves the
// handler maps Assemble's own not-found rejection (e.g. NFR1's
// cross-scope case, krill/work/payload_test.go's own coverage) onto 404
// too, not just the handler's own outer lookup -- the assembler here is
// deliberately built over a *different* fake than the handler's own
// `tasks` argument, so the outer lookup succeeds (letting the handler
// call Assemble) while Assemble's internal lookup is the one that fails.
func TestGetTaskPayloadHandler_AssembleNotFound_Returns404(t *testing.T) {
	taskID := uuid.New()
	scopeID := uuid.New()

	outerTasks := &fakeTaskStore{getTaskByIDResult: store.Task{ID: taskID, ScopeID: scopeID}}
	assemblerTasks := &fakeTaskStore{getTaskByIDErr: store.ErrNotFound}
	assembler := work.NewAssembler(assemblerTasks, slice.NewQuerier(nil))

	rec := doGetTaskPayloadRequest(t, handlers.GetTaskPayloadHandler(outerTasks, assembler), taskID.String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}
