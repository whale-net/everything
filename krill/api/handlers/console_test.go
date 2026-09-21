// Unit tests for ListClaimedTasksHandler (console.go, issue #2916's Testing
// section, FR4), ListCancelledTasksHandler (console.go, issue #2873's
// Testing section, FR10), and ListEscalatedTasksHandler (console.go, issue
// #2875's Testing section, FR5): GET /console/claimed, GET /console/cancelled,
// and GET /console/escalated are all ungated with scope_id as a required
// query parameter, page_size/page_token pass-through, and
// writeConsoleQueryError's mapping of a token error to 400.
// ListEscalatedTasksHandler additionally gets its
// own no-inline-history regression guard at the wire boundary (FR5/NFR6/
// #2851 Assumption 11), mirroring
// TestTaskStore_EscalatedTaskRow_NoInlineHistoryFields
// (krill/store/task_console_integration_test.go) at the struct-shape
// level. No Postgres dependency -- fakeTaskStore (fake_task_store_test.go)
// stands in for store.TaskStore; the row-content/paging proofs against a
// real query live in krill/store/task_console_integration_test.go.
package handlers_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// TestListClaimedTasksHandler_MissingScopeID_Returns400 proves scope_id is
// required -- there is no path entity to resolve it from (FR4, issue
// #2916).
func TestListClaimedTasksHandler_MissingScopeID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/claimed", nil)
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListClaimedTasksHandler_ParamsPassThrough proves scope_id/page_size/
// page_token all pass through to the store call unchanged.
func TestListClaimedTasksHandler_ParamsPassThrough(t *testing.T) {
	scopeID := uuid.New()
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+scopeID.String()+"&page_size=10&page_token=abc", nil)
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotListClaimedTasksParams.ScopeID)
	assert.Equal(t, 10, tasks.gotListClaimedTasksParams.Page.PageSize)
	assert.Equal(t, "abc", tasks.gotListClaimedTasksParams.Page.ContinuationToken)
}

// TestListClaimedTasksHandler_TokenError_Returns400 proves
// writeConsoleQueryError maps a token error (a stale, cross-scope, or
// forged continuation token) to 400, never a 500.
func TestListClaimedTasksHandler_TokenError_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{listClaimedTasksErr: store.ErrTokenScopeMismatch}

	req := httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListClaimedTasksHandler_RowContent proves the wire shape carries
// title, delivery reference, claimant session id, both subject pairs,
// current lane, lease expiry, and attempt count.
func TestListClaimedTasksHandler_RowContent(t *testing.T) {
	taskID := uuid.New()
	deliveryRefID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "operator-1", Kind: store.SubjectKindHuman}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "swarm-1", Kind: store.SubjectKindService}

	tasks := &fakeTaskStore{
		listClaimedTasksResult: store.Page[store.ClaimedTaskRow]{
			Items: []store.ClaimedTaskRow{
				{
					TaskID: taskID,
					Title:  "claimed task",
					DeliveryRef: store.ClaimedTaskDeliveryRef{
						ID:    deliveryRefID,
						Kind:  store.MilestoneKindMilepebble,
						Title: "MP1",
					},
					ClaimantActing:     acting,
					ClaimantOnBehalfOf: onBehalfOf,
					CurrentLane:        store.LaneScaffold,
					AttemptCount:       2,
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, taskID.String())
	assert.Contains(t, body, "claimed task")
	assert.Contains(t, body, deliveryRefID.String())
	assert.Contains(t, body, "milepebble")
	assert.Contains(t, body, "operator-1")
	assert.Contains(t, body, "swarm-1")
	assert.Contains(t, body, "Scaffold")
}

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

// TestListEscalatedTasksHandler_MissingScopeID_Returns400 proves scope_id
// is required -- there is no path entity to resolve it from (FR5, issue
// #2875).
func TestListEscalatedTasksHandler_MissingScopeID_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated", nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListEscalatedTasksHandler_ParamsPassThrough proves scope_id/
// page_size/page_token all pass through to the store call unchanged.
func TestListEscalatedTasksHandler_ParamsPassThrough(t *testing.T) {
	scopeID := uuid.New()
	tasks := &fakeTaskStore{}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+scopeID.String()+"&page_size=10&page_token=abc", nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, tasks.gotListEscalated.ScopeID)
	assert.Equal(t, 10, tasks.gotListEscalated.Page.PageSize)
	assert.Equal(t, "abc", tasks.gotListEscalated.Page.ContinuationToken)
}

// TestListEscalatedTasksHandler_TokenError_Returns400 proves
// writeConsoleQueryError maps a token error (a stale, cross-scope, or
// forged continuation token) to 400, never a 500.
func TestListEscalatedTasksHandler_TokenError_Returns400(t *testing.T) {
	tasks := &fakeTaskStore{listEscalatedErr: store.ErrTokenScopeMismatch}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestListEscalatedTasksHandler_RowContent proves the wire shape carries
// title, delivery reference, reason, counter/cap, lane, escalated_at, both
// subject pairs, and the summary-history fields (FR5).
func TestListEscalatedTasksHandler_RowContent(t *testing.T) {
	taskID := uuid.New()
	deliveryRefID := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "operator-1", Kind: store.SubjectKindHuman}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "operator-1", Kind: store.SubjectKindHuman}
	counterVal := store.DefaultThrashCap
	capVal := store.DefaultThrashCap
	verdict := store.VerdictFail

	tasks := &fakeTaskStore{
		listEscalatedResult: store.Page[store.EscalatedTaskRow]{
			Items: []store.EscalatedTaskRow{
				{
					TaskID: taskID,
					Title:  "stuck task",
					DeliveryRef: store.EscalatedTaskDeliveryRef{
						ID:    deliveryRefID,
						Kind:  store.MilestoneKindMilepebble,
						Title: "MP1",
					},
					Reason:                store.EscalationReasonThrashCap,
					CounterValue:          &counterVal,
					CapValue:              &capVal,
					Lane:                  store.LaneImplementation,
					EscalatedByActing:     acting,
					EscalatedByOnBehalfOf: onBehalfOf,
					AttemptCount:          1,
					FailingVerdictCount:   3,
					MostRecentVerdict:     &verdict,
					NoteCount:             2,
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, taskID.String())
	assert.Contains(t, body, "stuck task")
	assert.Contains(t, body, deliveryRefID.String())
	assert.Contains(t, body, "thrash-cap")
	assert.Contains(t, body, "Implementation")
	assert.Contains(t, body, "fail")
	assert.Contains(t, body, "operator-1")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	tasksArr, ok := decoded["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasksArr, 1)
	row, ok := tasksArr[0].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, float64(store.DefaultThrashCap), row["counter_value"])
	assert.Equal(t, float64(store.DefaultThrashCap), row["cap_value"])
	assert.Equal(t, float64(3), row["failing_verdict_count"])
	assert.Equal(t, float64(2), row["note_count"])
}

// TestListEscalatedTasksHandler_NoInlineHistory_RegressionGuard is FR5/
// NFR6/#2851 Assumption 11's guard at the wire boundary: even given a row
// with a rich summary (nonzero attempt/failing-verdict/note counts), the
// JSON response never carries a per-attempt, per-verdict, or per-note
// array -- only the summary counts. Mirrors
// TestTaskStore_EscalatedTaskRow_NoInlineHistoryFields
// (krill/store/task_console_integration_test.go) at the struct-shape
// level, but checks the actual bytes an operator receives.
func TestListEscalatedTasksHandler_NoInlineHistory_RegressionGuard(t *testing.T) {
	counterVal := store.DefaultAttemptCap
	capVal := store.DefaultAttemptCap
	tasks := &fakeTaskStore{
		listEscalatedResult: store.Page[store.EscalatedTaskRow]{
			Items: []store.EscalatedTaskRow{
				{
					TaskID:              uuid.New(),
					Title:               "rich history",
					Reason:              store.EscalationReasonAttemptCap,
					CounterValue:        &counterVal,
					CapValue:            &capVal,
					AttemptCount:        3,
					FailingVerdictCount: 5,
					NoteCount:           7,
				},
			},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	tasksArr, ok := decoded["tasks"].([]any)
	require.True(t, ok)
	require.Len(t, tasksArr, 1)
	row, ok := tasksArr[0].(map[string]any)
	require.True(t, ok)

	for _, forbidden := range []string{"attempts", "verdicts", "notes", "attempt_history", "verdict_history", "note_history"} {
		assert.NotContains(t, row, forbidden, "the escalated-task row must never carry a %q array -- summary history only (FR5, NFR6, #2851 Assumption 11)", forbidden)
	}
	for key, val := range row {
		if arr, isArray := val.([]any); isArray {
			t.Fatalf("escalated-task row key %q unexpectedly holds an array (len %d) -- this row must carry summary counts only, never an inline history list", key, len(arr))
		}
	}
}
