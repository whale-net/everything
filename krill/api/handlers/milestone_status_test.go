// Unit tests for SetMilestoneStatusHandler/GetMilestoneStatusHandler/
// GetMilestoneStatusHistoryHandler (milestone_status.go, issue #2685's
// Testing section): POST /milestones/{id}/status is gated like every
// other write endpoint (FR3) -- NFR4's "a handler call without a session
// is 401 and writes nothing" -- while the two GET endpoints are ungated
// reads. Also issue #2963's transition edge table: the pure
// ValidateMilestoneStatusTransition over every ordered pair of the eight
// values, and the handler-level half (an illegal edge is a 409 that never
// reaches RecordTransition; a self-transition is a 200 no-op that writes no
// history row). No Postgres dependency -- fakeMilestoneStatusStore
// (fake_milestone_status_store_test.go) stands in for
// store.MilestoneStatusEventStore (krill/store/
// milestone_status_integration_test.go covers the real store behavior
// against Postgres).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"fmt"
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

// doMilestoneStatusPostRequest mounts handler behind
// handlers.RequireSession(sessions) at "{id}/status" -- exactly like
// routes.go's "POST /milestones/{id}/status" -- so r.PathValue("id") is
// populated the same way a real request would populate it.
// sessionIDHeader == "" omits the X-Krill-Session-Id header entirely (the
// "no session id" rejection case). method lets a test drive a non-POST
// verb at the same path (proving no other verb reaches this handler).
func doMilestoneStatusPostRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, method, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/status", gated)

	req := httptest.NewRequest(method, "/probe/"+id+"/status", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doMilestoneStatusGetRequest drives one of the two ungated GET endpoints
// directly (no RequireSession wrapping, mirroring routes.go), with
// r.PathValue("id") populated via a bare mux mount. pattern/requestPath
// let a test drive either "GET /probe/{id}/status" or
// "GET /probe/{id}/status/history" against the same helper.
func doMilestoneStatusGetRequest(t *testing.T, handler http.HandlerFunc, pattern, requestPath string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc(pattern, handler)

	req := httptest.NewRequest(http.MethodGet, requestPath, nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestSetMilestoneStatusHandler_NoSessionID_Rejected401_NoStoreCall is
// issue #2685's Testing item 7's handler-level half (NFR4): a request with
// no X-Krill-Session-Id header is rejected with 401 by the write gate
// (FR3) before it ever reaches RecordTransition -- the LB4 subject pair
// always comes from a real session, never a caller-supplied field, and an
// unauthenticated caller writes nothing.
func TestSetMilestoneStatusHandler_NoSessionID_Rejected401_NoStoreCall(t *testing.T) {
	sessions := newFakeSessionStore()
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, "", id.String(), http.MethodPost,
		`{"status": "planned"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, statuses.recordCalled, "an ungated request must never reach RecordTransition -- NFR4/FR3")
}

// TestSetMilestoneStatusHandler_ValidSession_RecordsTransitionWithPathIDAndSubjects
// proves the success path: a valid session lets the request through, and
// the handler passes the path's {id}, the body's status/note, and the
// session's scope/subject pair to RecordTransition unchanged. The fake's
// currentStatus is seeded to "in design" so the requested "designed" is a
// legal edge out of it -- an unseeded fake is "not started", from which
// "designed" is deliberately not reachable.
func TestSetMilestoneStatusHandler_ValidSession_RecordsTransitionWithPathIDAndSubjects(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusInDesign}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "designed", "note": "kicking off"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, statuses.recordCalled)
	assert.Equal(t, scopeID, statuses.gotScopeID)
	assert.Equal(t, id, statuses.gotMilestoneID)
	assert.Equal(t, store.MilestoneStatusDesigned, statuses.gotStatus)
	require.NotNil(t, statuses.gotNote)
	assert.Equal(t, "kicking off", *statuses.gotNote)

	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.NotEmpty(t, resp.ID)
}

// TestSetMilestoneStatusHandler_InvalidStatus_Returns400_NoStoreCall
// proves FR8's fixed eight-value set is enforced before RecordTransition
// is ever called -- a ninth, made-up value is a 400, not a store call
// (the DB CHECK, migrations 012 + 019, is the other, store-level half of
// this same guarantee -- see krill/store/
// milestone_status_integration_test.go's TestMilestoneStatusStore_
// AllEightStatuses_NinthRejectedByDBCheck).
func TestSetMilestoneStatusHandler_InvalidStatus_Returns400_NoStoreCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "bogus-status"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, statuses.recordCalled, "an invalid status must never reach RecordTransition")
}

// allMilestoneStatusEdges is the complete approved transition table
// (issue #2963) as (from, to) pairs, including the terminal rows' absence
// of successors by way of the exhaustive illegalEdges list below. Every
// pair here must be accepted; every pair of distinct values NOT here must
// be rejected.
var allMilestoneStatusEdges = [][2]store.MilestoneStatus{
	{store.MilestoneStatusNotStarted, store.MilestoneStatusInDesign},
	{store.MilestoneStatusNotStarted, store.MilestoneStatusAbandoned},
	{store.MilestoneStatusInDesign, store.MilestoneStatusDesigned},
	{store.MilestoneStatusInDesign, store.MilestoneStatusAbandoned},
	{store.MilestoneStatusDesigned, store.MilestoneStatusPlanned},
	{store.MilestoneStatusDesigned, store.MilestoneStatusInDesign},
	{store.MilestoneStatusDesigned, store.MilestoneStatusAbandoned},
	{store.MilestoneStatusPlanned, store.MilestoneStatusInProgress},
	{store.MilestoneStatusPlanned, store.MilestoneStatusAbandoned},
	{store.MilestoneStatusInProgress, store.MilestoneStatusPartiallyComplete},
	{store.MilestoneStatusInProgress, store.MilestoneStatusShipped},
	{store.MilestoneStatusInProgress, store.MilestoneStatusAbandoned},
	{store.MilestoneStatusPartiallyComplete, store.MilestoneStatusInProgress},
	{store.MilestoneStatusPartiallyComplete, store.MilestoneStatusShipped},
	{store.MilestoneStatusPartiallyComplete, store.MilestoneStatusAbandoned},
}

// TestValidateMilestoneStatusTransition_AcceptsEveryLegalEdge is issue
// #2963's acceptance criterion, table half: every edge the design lists is
// accepted. The three the design calls out by name -- the
// "designed -> in design" rework edge, the "in progress <-> partially
// complete" loop, and "abandoned" reachable from every non-terminal row --
// are all present in allMilestoneStatusEdges, so this table covers them.
func TestValidateMilestoneStatusTransition_AcceptsEveryLegalEdge(t *testing.T) {
	for _, edge := range allMilestoneStatusEdges {
		from, to := edge[0], edge[1]
		assert.NoError(t, handlers.ValidateMilestoneStatusTransition(from, to),
			"%q -> %q is a legal edge and must be accepted", from, to)
	}
}

// TestValidateMilestoneStatusTransition_RejectsEveryIllegalEdge is the
// other half: every ordered pair of the eight values that is not in the
// table is rejected, including the two the design singles out as
// deliberately not legal ("planned -> in design") and as the canonical
// example ("not started -> shipped"), and every pair out of either
// terminal row.
func TestValidateMilestoneStatusTransition_RejectsEveryIllegalEdge(t *testing.T) {
	all := []store.MilestoneStatus{
		store.MilestoneStatusNotStarted,
		store.MilestoneStatusInDesign,
		store.MilestoneStatusDesigned,
		store.MilestoneStatusPlanned,
		store.MilestoneStatusInProgress,
		store.MilestoneStatusShipped,
		store.MilestoneStatusPartiallyComplete,
		store.MilestoneStatusAbandoned,
	}
	legal := map[[2]store.MilestoneStatus]bool{}
	for _, e := range allMilestoneStatusEdges {
		legal[e] = true
	}

	for _, from := range all {
		for _, to := range all {
			if from == to || legal[[2]store.MilestoneStatus{from, to}] {
				continue
			}
			err := handlers.ValidateMilestoneStatusTransition(from, to)
			require.Error(t, err, "%q -> %q is not in the edge table and must be rejected", from, to)
			assert.ErrorIs(t, err, handlers.ErrIllegalMilestoneStatusTransition)
			assert.Contains(t, err.Error(), fmt.Sprintf("%q -> %q", from, to), "the error must name the illegal edge")
		}
	}
}

// TestValidateMilestoneStatusTransition_TerminalRowsNameNoAlternatives
// pins the error's two shapes: a non-terminal row's rejection names the
// legal alternatives, while a terminal row's says so outright rather than
// rendering an empty list.
func TestValidateMilestoneStatusTransition_TerminalRowsNameNoAlternatives(t *testing.T) {
	fromPlanned := handlers.ValidateMilestoneStatusTransition(store.MilestoneStatusPlanned, store.MilestoneStatusShipped)
	require.Error(t, fromPlanned)
	assert.Contains(t, fromPlanned.Error(), `"in progress"`, "a non-terminal row's rejection must list its legal alternatives")
	assert.Contains(t, fromPlanned.Error(), `"abandoned"`)

	fromShipped := handlers.ValidateMilestoneStatusTransition(store.MilestoneStatusShipped, store.MilestoneStatusInProgress)
	require.Error(t, fromShipped)
	assert.Contains(t, fromShipped.Error(), "terminal", "a terminal row's rejection must say it is terminal")
}

// TestSetMilestoneStatusHandler_IllegalTransition_Returns409NamingEdgeAnd
// Alternatives proves the edge table is enforced on the HTTP write path
// itself, not just by the pure function above: an edge outside the table
// is a 409 naming the illegal edge and the legal alternatives, and
// RecordTransition is never reached.
func TestSetMilestoneStatusHandler_IllegalTransition_Returns409NamingEdgeAndAlternatives(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusNotStarted}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "shipped"}`)

	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	var body struct {
		Error string `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.Contains(t, body.Error, `"not started" -> "shipped"`, "the 409 body must name the illegal edge")
	assert.Contains(t, body.Error, `"in design"`, "the 409 body must list the legal alternatives")
	assert.Contains(t, body.Error, `"abandoned"`)
	assert.False(t, statuses.recordCalled, "an illegal edge must never reach RecordTransition -- the history is append-only, so it cannot be taken back")
}

// TestSetMilestoneStatusHandler_PlannedToInDesignRejected proves the one
// edge the design calls out as deliberately NOT legal: un-designing a
// planned container is not the same operation as reworking a designed one.
func TestSetMilestoneStatusHandler_PlannedToInDesignRejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusPlanned}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "in design"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.False(t, statuses.recordCalled)
}

// TestSetMilestoneStatusHandler_SelfTransition_IsNoOp_WritesNoHistory is
// issue #2963's second FR: re-setting a milestone to the status it already
// holds is not an edge at all -- it is accepted, returns 200 (nothing was
// created), and never reaches RecordTransition, so no history row is
// written. The id in the body is the latest existing transition's, so a
// caller still gets something stable back.
func TestSetMilestoneStatusHandler_SelfTransition_IsNoOp_WritesNoHistory(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	existing := store.MilestoneStatusEvent{ID: uuid.New(), Status: store.MilestoneStatusPlanned}
	statuses := &fakeMilestoneStatusStore{
		currentStatus: store.MilestoneStatusPlanned,
		transitions:   []store.MilestoneStatusEvent{existing},
	}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "planned"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, statuses.recordCalled, "a self-transition must write no history row")

	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, existing.ID.String(), resp.ID, "the no-op returns the latest existing transition's id")
}

// TestSetMilestoneStatusHandler_SelfTransitionOnFreshContainer_Returns
// EmptyID covers the derived-"not started" corner: a container with no
// history re-affirmed as "not started" is still a no-op, and there is no
// transition row to point at, so the id is empty rather than a nil UUID
// dressed up as a real one.
func TestSetMilestoneStatusHandler_SelfTransitionOnFreshContainer_ReturnsEmptyID(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "not started"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.False(t, statuses.recordCalled)

	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Empty(t, resp.ID)
}

// TestSetMilestoneStatusHandler_AcceptedTransition_AppendsExactlyOneRow
// is the "exactly one history row" half of the acceptance criteria, as
// far as the handler can see it: one accepted edge produces exactly one
// RecordTransition call carrying the new status, and nothing else touches
// the store.
func TestSetMilestoneStatusHandler_AcceptedTransition_AppendsExactlyOneRow(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusInProgress}
	id := uuid.New()

	rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), http.MethodPost,
		`{"status": "partially complete"}`)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.True(t, statuses.recordCalled)
	assert.Equal(t, store.MilestoneStatusPartiallyComplete, statuses.gotStatus)
}

// TestSetMilestoneStatusHandler_WrongMethod_Returns405_NoStoreCall is
// part of issue #2685's NFR2 structural proof: doMilestoneStatusPostRequest
// mounts SetMilestoneStatusHandler behind the exact single-method pattern
// routes.go uses ("POST /milestones/{id}/status", no PUT/PATCH/DELETE
// pattern registered anywhere alongside it) -- so a PUT, PATCH, or DELETE
// at this path (the verbs an update-or-delete path would use) never even
// reaches the handler, and SetMilestoneStatusHandler's own belt-and-braces
// r.Method check (milestone_status.go) backs that up a second way.
// Neither ever dispatches to RecordTransition.
func TestSetMilestoneStatusHandler_WrongMethod_Returns405_NoStoreCall(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			statuses := &fakeMilestoneStatusStore{}
			id := uuid.New()

			rec := doMilestoneStatusPostRequest(t, handlers.SetMilestoneStatusHandler(statuses), sessions, sessionIDStr, id.String(), method,
				`{"status": "planned"}`)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "milestone_status_event has no update/delete path (NFR2) -- %s must never be accepted", method)
			assert.False(t, statuses.recordCalled)
		})
	}
}

// TestGetMilestoneStatusHandler_ReturnsCurrentStatus proves the ungated
// read endpoint reports whatever CurrentStatus derives.
func TestGetMilestoneStatusHandler_ReturnsCurrentStatus(t *testing.T) {
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusShipped}
	id := uuid.New()

	rec := doMilestoneStatusGetRequest(t, handlers.GetMilestoneStatusHandler(statuses), "GET /probe/{id}/status", "/probe/"+id.String()+"/status")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp struct {
		Status string `json:"status"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "shipped", resp.Status)
}

// TestGetMilestoneStatusHistoryHandler_ReturnsChronologicalTransitions
// proves the ungated history endpoint (FR12) serializes ListTransitions'
// answer, in order, with each entry's subject pair and timestamp.
func TestGetMilestoneStatusHistoryHandler_ReturnsChronologicalTransitions(t *testing.T) {
	id := uuid.New()
	acting := store.Subject{Iss: "https://issuer.example.com", Sub: "agent-1", Kind: store.SubjectKindService}
	onBehalfOf := store.Subject{Iss: "https://issuer.example.com", Sub: "human-1", Kind: store.SubjectKindHuman}
	now := time.Now().UTC()

	statuses := &fakeMilestoneStatusStore{transitions: []store.MilestoneStatusEvent{
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusPlanned, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now},
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusInProgress, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now.Add(time.Minute)},
		{ID: uuid.New(), MilestoneID: id, Status: store.MilestoneStatusShipped, CreatedByActing: acting, CreatedByOnBehalfOf: onBehalfOf, CreatedAt: now.Add(2 * time.Minute)},
	}}

	rec := doMilestoneStatusGetRequest(t, handlers.GetMilestoneStatusHistoryHandler(statuses), "GET /probe/{id}/status/history", "/probe/"+id.String()+"/status/history")

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.MilestoneStatusHistoryResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Transitions, 3)
	assert.Equal(t, "planned", resp.Transitions[0].Status)
	assert.Equal(t, "in progress", resp.Transitions[1].Status)
	assert.Equal(t, "shipped", resp.Transitions[2].Status)
	for _, tr := range resp.Transitions {
		assert.Equal(t, "agent-1", tr.Acting.Sub)
		assert.Equal(t, "human-1", tr.OnBehalfOf.Sub)
	}
}
