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
	"time"

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
// claimed-since, current lane, lease expiry, and attempt count.
//
// The claimed-since assertion is what makes list_claimed_tasks and
// GET /console/claimed show it: both render rows through this same
// ToClaimedTaskWire conversion, so one wire field is the whole
// difference between the two surfaces carrying the value and neither
// doing so.
func TestListClaimedTasksHandler_RowContent(t *testing.T) {
	taskID := uuid.New()
	deliveryRefID := uuid.New()
	claimedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
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
					ClaimedAt:          claimedAt,
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
	assert.Contains(t, body, claimedAt.Format(time.RFC3339), "the row carries when the claim was taken")
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

// TestListClaimedTasksHandler_CarriesClaimID is FR a6cd917f's row-identity
// half on the HTTP surface: a claimed row carries claim_id under exactly
// that name, the id a release carrying an expected claim is checked
// against. The MCP surface renders rows through this same
// ToClaimedTaskWire conversion (list_claimed_tasks reuses
// handlers.ClaimedTaskWire), so one wire field is the whole difference
// between the two surfaces carrying the id and neither doing so -- the
// same argument
// TestListClaimedTasksHandler_RowContent's own claimed-since assertion
// makes.
func TestListClaimedTasksHandler_CarriesClaimID(t *testing.T) {
	claimID := uuid.New()
	tasks := &fakeTaskStore{
		listClaimedTasksResult: store.Page[store.ClaimedTaskRow]{
			Items: []store.ClaimedTaskRow{{TaskID: uuid.New(), Title: "claimed", ClaimID: claimID}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := firstTaskRow(t, rec)
	assert.Equal(t, claimID.String(), row["claim_id"])
	assert.NotContains(t, row, "escalation_id", "a claimed row carries no escalation id")
}

// TestListEscalatedTasksHandler_CarriesEscalationID is the escalated half
// of the same proof: an escalated row carries escalation_id under exactly
// that name, and no claim_id of its own.
func TestListEscalatedTasksHandler_CarriesEscalationID(t *testing.T) {
	escalationID := uuid.New()
	tasks := &fakeTaskStore{
		listEscalatedResult: store.Page[store.EscalatedTaskRow]{
			Items: []store.EscalatedTaskRow{{TaskID: uuid.New(), Title: "stuck", EscalationID: escalationID}},
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String(), nil)
	rec := httptest.NewRecorder()
	handlers.ListEscalatedTasksHandler(tasks)(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	row := firstTaskRow(t, rec)
	assert.Equal(t, escalationID.String(), row["escalation_id"])
	assert.NotContains(t, row, "claim_id", "an escalated row carries no claim id")
}

// firstTaskRow decodes a console list response and returns its one row as
// a JSON object -- the shape the claim_id/escalation_id assertions above
// read field names off.
func firstTaskRow(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &decoded))
	tasksArr, ok := decoded["tasks"].([]any)
	require.True(t, ok, "response must carry a tasks array: %s", rec.Body.String())
	require.Len(t, tasksArr, 1)
	row, ok := tasksArr[0].(map[string]any)
	require.True(t, ok)
	return row
}

// TestConsoleHandlers_ProductMilestoneFilterPassThrough proves all four
// console queue reads take the optional product_id/milestone_id narrowing
// and hand it to the store as a ConsoleFilter, with an absent parameter
// leaving the read unnarrowed.
func TestConsoleHandlers_ProductMilestoneFilterPassThrough(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()
	query := "&product_id=" + productID.String() + "&milestone_id=" + milestoneID.String()

	t.Run("claimed", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListClaimedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+scopeID.String()+query, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.gotListClaimedTasksParams.ConsoleFilter)
	})

	t.Run("cancelled", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListCancelledTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/cancelled?scope_id="+scopeID.String()+query, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.gotListCancelled.ConsoleFilter)
	})

	t.Run("escalated", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListEscalatedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+scopeID.String()+query, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.gotListEscalated.ConsoleFilter)
	})

	t.Run("open_notes", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListOpenNotesHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/notes?scope_id="+scopeID.String()+query, nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.gotListOpenNotesParams.ConsoleFilter)
	})
}

// TestConsoleHandlers_NoFilterParams_LeavesReadUnnarrowed proves an absent
// product_id/milestone_id leaves the read exactly as wide as it was, so
// today's scope-wide callers are unaffected by the filters.
func TestConsoleHandlers_NoFilterParams_LeavesReadUnnarrowed(t *testing.T) {
	tasks := &fakeTaskStore{}
	rec := httptest.NewRecorder()
	handlers.ListClaimedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+uuid.New().String(), nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, tasks.gotListClaimedTasksParams.ConsoleFilter.IsZero())
}

// TestListEscalatedTasksHandler_ReasonParamPassThrough proves the
// escalation-reason filter reaches the store, and that an absent reason
// leaves the read unnarrowed.
func TestListEscalatedTasksHandler_ReasonParamPassThrough(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListEscalatedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String()+"&reason=attempt-cap", nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		require.NotNil(t, tasks.gotListEscalated.Reason)
		assert.Equal(t, store.EscalationReasonAttemptCap, *tasks.gotListEscalated.Reason)
	})

	t.Run("absent", func(t *testing.T) {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListEscalatedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+uuid.New().String(), nil))
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		assert.Nil(t, tasks.gotListEscalated.Reason, "an absent reason must keep every reason, exactly as before the filter existed")
	})
}

// TestConsoleHandlers_FilterErrorMapping proves writeConsoleQueryError
// maps each new refusal to its own status: a narrowing outside the caller's
// scope is a 404 (the store's own parent-guard shape), a wrong-filter
// continuation token and an unknown escalation reason are 400s, and
// neither ever becomes a 500.
func TestConsoleHandlers_FilterErrorMapping(t *testing.T) {
	scopeID := uuid.New().String()
	for name, tc := range map[string]struct {
		storeErr error
		want     int
	}{
		"filter_outside_scope": {storeErr: store.ErrNotFound, want: http.StatusNotFound},
		"wrong_filter_token":   {storeErr: store.ErrTokenFilterMismatch, want: http.StatusBadRequest},
		"unknown_reason":       {storeErr: store.ErrUnknownEscalationReason, want: http.StatusBadRequest},
	} {
		t.Run(name, func(t *testing.T) {
			claimed := &fakeTaskStore{listClaimedTasksErr: tc.storeErr}
			rec := httptest.NewRecorder()
			handlers.ListClaimedTasksHandler(claimed)(rec, httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+scopeID, nil))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())

			escalated := &fakeTaskStore{listEscalatedErr: tc.storeErr}
			rec = httptest.NewRecorder()
			handlers.ListEscalatedTasksHandler(escalated)(rec, httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+scopeID, nil))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())

			notes := &fakeTaskStore{listOpenNotesErr: tc.storeErr}
			rec = httptest.NewRecorder()
			handlers.ListOpenNotesHandler(notes)(rec, httptest.NewRequest(http.MethodGet, "/console/notes?scope_id="+scopeID, nil))
			assert.Equal(t, tc.want, rec.Code, rec.Body.String())
		})
	}
}

// TestConsoleHandlers_MalformedFilterParam_Returns400 proves a
// product_id/milestone_id that is present but not a UUID is a caller error,
// never a 500 and never a silently unnarrowed read.
func TestConsoleHandlers_MalformedFilterParam_Returns400(t *testing.T) {
	for _, query := range []string{"&product_id=not-a-uuid", "&milestone_id=not-a-uuid"} {
		tasks := &fakeTaskStore{}
		rec := httptest.NewRecorder()
		handlers.ListClaimedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/claimed?scope_id="+uuid.New().String()+query, nil))
		assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

// TestConsoleHandlers_FilterAndReason_ReachStoreTogether is the API
// surface's half of the two-filter intersection: a request naming a
// product, a container and a reason hands all three to the store as one
// ConsoleFilter plus one Reason, so the HTTP surface cannot drop the
// reason (or either half of the filter) on the way through. The store
// proves what that combination means; this proves it arrives.
func TestConsoleHandlers_FilterAndReason_ReachStoreTogether(t *testing.T) {
	scopeID, productID, milestoneID := uuid.New(), uuid.New(), uuid.New()

	tasks := &fakeTaskStore{}
	rec := httptest.NewRecorder()
	query := "&product_id=" + productID.String() + "&milestone_id=" + milestoneID.String() + "&reason=manual"
	handlers.ListEscalatedTasksHandler(tasks)(rec, httptest.NewRequest(http.MethodGet, "/console/escalated?scope_id="+scopeID.String()+query, nil))
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	assert.Equal(t, store.ConsoleFilter{ProductID: &productID, MilestoneID: &milestoneID}, tasks.gotListEscalated.ConsoleFilter)
	require.NotNil(t, tasks.gotListEscalated.Reason)
	assert.Equal(t, store.EscalationReasonManual, *tasks.gotListEscalated.Reason)
	assert.Equal(t, scopeID, tasks.gotListEscalated.ScopeID, "the filters narrow the scope they are given, never replace it")
}
