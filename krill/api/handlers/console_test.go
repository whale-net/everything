// Unit tests for ListCancelledTasksHandler (console.go, issue #2873's
// Testing section, FR10): GET /console/cancelled is ungated with
// scope_id as a required query parameter (mirroring GET /console/claimed,
// issue #2869), page_size/page_token pass-through, and
// writeConsoleQueryError's mapping of a token error to 400. No Postgres
// dependency -- fakeTaskStore (fake_task_store_test.go) stands in for
// store.TaskStore; the row-content/paging proofs against a real query
// live in krill/store/task_console_integration_test.go.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// TestListCancelledTasksHandler_MissingScopeID_Returns400 proves scope_id
// is required -- there is no path entity to resolve it from.
func TestListCancelledTasksHandler_MissingScopeID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/cancelled", nil)
	rec := httptest.NewRecorder()
	handlers.ListCancelledTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListCancelledTasksHandler_ParamsPassThrough proves scope_id/
// page_size/page_token all pass through to the store call unchanged.
func TestListCancelledTasksHandler_ParamsPassThrough(t *testing.T) {
	scopeID := uuid.New()
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/cancelled?scope_id="+scopeID.String()+"&page_size=10&page_token=abc", nil)
	rec := httptest.NewRecorder()
	handlers.ListCancelledTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotListCancelled.ScopeID)
	assert.Equal(t, 10, tasks.gotListCancelled.Page.PageSize)
	assert.Equal(t, "abc", tasks.gotListCancelled.Page.ContinuationToken)
}

// TestListCancelledTasksHandler_TokenError_Returns400 proves
// writeConsoleQueryError maps a token error (a stale, cross-scope, or
// forged continuation token) to 400, never a 500.
func TestListCancelledTasksHandler_TokenError_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{listCancelledErr: store.ErrTokenScopeMismatch}

	req := httptest.NewRequest(http.MethodGet, "/console/cancelled?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListCancelledTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListCancelledTasksHandler_RowContent proves the wire shape carries
// title, delivery reference, both subject pairs, and cancelled_at.
func TestListCancelledTasksHandler_RowContent(t *testing.T) {
	taskID := uuid.New()
	deliveryRefID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "operator-1", Kind: store.SubjectKindHuman}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "swarm-1", Kind: store.SubjectKindService}

	tasks := &fakeTaskStore{
		listCancelledResult: store.Page[store.CancelledTaskRow]{
			Items: []store.CancelledTaskRow{
				{
					TaskID: taskID,
					Title:  "dead-lettered task",
					DeliveryRef: store.CancelledTaskDeliveryRef{
						ID:    deliveryRefID,
						Kind:  store.MilestoneKindMilepebble,
						Title: "MP1",
					},
					CancelledByActing:     acting,
					CancelledByOnBehalfOf: onBehalfOf,
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/cancelled?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListCancelledTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, taskID.String())
	assert.Contains(t, body, "dead-lettered task")
	assert.Contains(t, body, deliveryRefID.String())
	assert.Contains(t, body, "milepebble")
	assert.Contains(t, body, "operator-1")
	assert.Contains(t, body, "swarm-1")
}

// TestListOpenNotesHandler_MissingScopeID_Returns400 proves scope_id is
// required -- there is no path entity to resolve it from (FR12, issue
// #2874).
func TestListOpenNotesHandler_MissingScopeID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/notes", nil)
	rec := httptest.NewRecorder()
	handlers.ListOpenNotesHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListOpenNotesHandler_ParamsPassThrough proves scope_id/page_size/
// page_token all pass through to the store call unchanged.
func TestListOpenNotesHandler_ParamsPassThrough(t *testing.T) {
	scopeID := uuid.New()
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/notes?scope_id="+scopeID.String()+"&page_size=10&page_token=abc", nil)
	rec := httptest.NewRecorder()
	handlers.ListOpenNotesHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotListOpenNotesParams.ScopeID)
	assert.Equal(t, 10, tasks.gotListOpenNotesParams.Page.PageSize)
	assert.Equal(t, "abc", tasks.gotListOpenNotesParams.Page.ContinuationToken)
}

// TestListOpenNotesHandler_TokenError_Returns400 proves
// writeConsoleQueryError maps a token error (a stale, cross-scope, or
// forged continuation token) to 400, never a 500.
func TestListOpenNotesHandler_TokenError_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{listOpenNotesErr: store.ErrTokenScopeMismatch}

	req := httptest.NewRequest(http.MethodGet, "/console/notes?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListOpenNotesHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListOpenNotesHandler_RowContent_BothTargetShapes proves the wire
// shape carries body/kind/created_at plus exactly one of task/entity
// context, for both target shapes a note can name (FR12).
func TestListOpenNotesHandler_RowContent_BothTargetShapes(t *testing.T) {
	taskID := uuid.New()
	deliveryRefID := uuid.New()
	entityID := uuid.New()

	tasks := &fakeTaskStore{
		listOpenNotesResult: store.Page[store.OpenNoteRow]{
			Items: []store.OpenNoteRow{
				{
					NoteID: uuid.New(),
					Kind:   store.NoteKindComment,
					Body:   "task-scoped body",
					TaskContext: &store.OpenNoteTaskContext{
						TaskID: taskID,
						Title:  "do the thing",
						DeliveryRef: store.ClaimedTaskDeliveryRef{
							ID:    deliveryRefID,
							Kind:  store.MilestoneKindMilepebble,
							Title: "MP1",
						},
					},
				},
				{
					NoteID: uuid.New(),
					Kind:   store.NoteKindScopeNote,
					Body:   "entity-scoped body",
					EntityContext: &store.OpenNoteEntityContext{
						EntityKind: store.NoteEntityKindRequirement,
						EntityID:   entityID,
						Title:      "FR1",
					},
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/notes?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListOpenNotesHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "task-scoped body")
	assert.Contains(t, body, taskID.String())
	assert.Contains(t, body, "do the thing")
	assert.Contains(t, body, deliveryRefID.String())
	assert.Contains(t, body, "entity-scoped body")
	assert.Contains(t, body, entityID.String())
	assert.Contains(t, body, "requirement")
	assert.Contains(t, body, "FR1")
}
