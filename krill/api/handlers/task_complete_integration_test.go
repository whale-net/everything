//go:build integration

// Real-Postgres coverage for CompleteTaskHandler's full success path
// (task_complete.go, issue #2725's Testing section) -- the branches
// task_complete_test.go's fakes cannot reach, since work.Assembler wraps a
// concrete *slice.Querier over a real *store.Store, not an interface a
// fake can stand in for (mirrors task_claim_integration_test.go's own doc
// comment on the identical limitation): a genuine 200 whose payload
// reflects the new lane and cleared claim state, the complete response's
// document shape matching a subsequent GET /tasks/{id} exactly (FR4
// single-type check, LB7/NFR4), and that the completed task is claimable
// again through the real claim endpoint.
//
// Run it explicitly (requires a working Docker daemon):
//
//	bazel test //krill/api/handlers:task_complete_integration_test --test_output=all
package handlers_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
	"github.com/whale-net/everything/krill/work"
)

// newTaskCompleteHTTPMux wires the same three routes routes.go mounts for
// this capability -- claim, complete, and the by-id GET -- onto one mux,
// so a test can drive a full claim -> complete -> GET round trip exactly
// like a real caller would.
func newTaskCompleteHTTPMux(s *store.Store, sessions store.SessionStore, assembler *work.Assembler) *http.ServeMux {
	mux := http.NewServeMux()
	gate := handlers.RequireSession(sessions)
	mux.Handle("POST /tasks/{id}/claim", gate(handlers.ClaimTaskHandler(s.Tasks(), assembler)))
	mux.Handle("POST /tasks/{id}/complete", gate(handlers.CompleteTaskHandler(s.Tasks(), assembler)))
	mux.HandleFunc("GET /tasks/{id}", handlers.GetTaskPayloadHandler(s.Tasks(), assembler))
	return mux
}

// claimViaHTTP drives POST /tasks/{id}/claim through mux and returns the
// decoded payload, so this file's own complete-path tests can obtain a
// real claim id without reaching into store.TaskStore directly.
func claimViaHTTP(t *testing.T, mux *http.ServeMux, taskID uuid.UUID, sessionID uuid.UUID) work.Payload {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/claim", nil)
	req.Header.Set("X-Krill-Session-Id", sessionID.String())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	return payload
}

// completeViaHTTP drives POST /tasks/{id}/complete through mux with the
// given claim id and verdict, returning the raw recorder so callers can
// assert on status code as well as body.
func completeViaHTTP(t *testing.T, mux *http.ServeMux, taskID, claimID uuid.UUID, sessionID uuid.UUID, verdict string) *httptest.ResponseRecorder {
	t.Helper()
	body := `{"claim_id":"` + claimID.String() + `","verdict":"` + verdict + `"}`
	req := httptest.NewRequest(http.MethodPost, "/tasks/"+taskID.String()+"/complete", strings.NewReader(body))
	req.Header.Set("X-Krill-Session-Id", sessionID.String())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestCompleteTaskHandler_Success_ReturnsPayloadWithNewLaneAndClearedClaim
// is issue #2725's Testing section exercised through the real HTTP
// handler: claiming then completing with `pass` returns a 200 whose
// payload carries the new lane and no current claim (FR8).
func TestCompleteTaskHandler_Success_ReturnsPayloadWithNewLaneAndClearedClaim(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, featureID := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := newTaskCompleteHTTPMux(s, sessions, assembler)

	claimPayload := claimViaHTTP(t, mux, taskID, uuid.UUID(sessionID))
	require.NotNil(t, claimPayload.Task.CurrentClaim)
	claimID := claimPayload.Task.CurrentClaim.ClaimID

	rec := completeViaHTTP(t, mux, taskID, claimID, uuid.UUID(sessionID), "pass")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, taskID, payload.Task.ID)
	require.Len(t, payload.Slice.Features, 1)
	assert.Equal(t, featureID, payload.Slice.Features[0].ID)
	assert.Equal(t, "Implementation", payload.Task.CurrentLane, "pass from Scaffold in the full sequence must land on Implementation")
	assert.Nil(t, payload.Task.CurrentClaim, "a completed task must be left unclaimed")
}

// TestCompleteTaskHandler_CompleteResponseMatchesSubsequentGet is issue
// #2725's Testing section (FR4 single-type check): the complete response
// and a subsequent GET /tasks/{id} return the exact same document -- one
// payload type, never a completion-specific response shape (NFR4/LB7).
func TestCompleteTaskHandler_CompleteResponseMatchesSubsequentGet(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, _ := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := newTaskCompleteHTTPMux(s, sessions, assembler)

	claimPayload := claimViaHTTP(t, mux, taskID, uuid.UUID(sessionID))
	claimID := claimPayload.Task.CurrentClaim.ClaimID

	completeRec := completeViaHTTP(t, mux, taskID, claimID, uuid.UUID(sessionID), "pass")
	require.Equal(t, http.StatusOK, completeRec.Code, completeRec.Body.String())

	getReq := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID.String(), nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	var completePayload, getPayload work.Payload
	require.NoError(t, json.Unmarshal(completeRec.Body.Bytes(), &completePayload))
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &getPayload))

	assert.Equal(t, completePayload, getPayload, "the complete response and a subsequent GET /tasks/{id} must return the exact same document")
}

// TestCompleteTaskHandler_UnclaimedAfterComplete_ClaimableAgain is issue
// #2725's Testing section: after a successful complete, the task is
// unclaimed and claimable again (it has not reached Done) through the
// real claim endpoint -- not merely at the store layer.
func TestCompleteTaskHandler_UnclaimedAfterComplete_ClaimableAgain(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, _ := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := newTaskCompleteHTTPMux(s, sessions, assembler)

	firstClaim := claimViaHTTP(t, mux, taskID, uuid.UUID(sessionID))
	completeRec := completeViaHTTP(t, mux, taskID, firstClaim.Task.CurrentClaim.ClaimID, uuid.UUID(sessionID), "fail")
	require.Equal(t, http.StatusOK, completeRec.Code, completeRec.Body.String())

	secondSessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	secondClaim := claimViaHTTP(t, mux, taskID, uuid.UUID(secondSessionID))
	require.NotNil(t, secondClaim.Task.CurrentClaim)
	assert.NotEqual(t, firstClaim.Task.CurrentClaim.ClaimID, secondClaim.Task.CurrentClaim.ClaimID, "a completed (not Done) task must mint a genuinely new claim on re-claim")
}

// TestCompleteTaskHandler_ThrashCapTrip_SurfacesStateReasonAndHeldLane is
// issue #2870's Testing section (FR2, response contract): driving a task
// through repeated claim/complete `fail` cycles until the thrash cap trips
// returns a 200 whose payload carries state "escalated", escalation_reason
// "thrash-cap", and current_lane set to the *held* lane -- never the
// reverted one a plain fail would have produced below the cap.
func TestCompleteTaskHandler_ThrashCapTrip_SurfacesStateReasonAndHeldLane(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, _ := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := newTaskCompleteHTTPMux(s, sessions, assembler)

	claimAndCompleteViaHTTP := func(verdict string) *httptest.ResponseRecorder {
		sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
		require.NoError(t, err)
		claimPayload := claimViaHTTP(t, mux, taskID, uuid.UUID(sessionID))
		require.NotNil(t, claimPayload.Task.CurrentClaim)
		return completeViaHTTP(t, mux, taskID, claimPayload.Task.CurrentClaim.ClaimID, uuid.UUID(sessionID), verdict)
	}

	// The task starts on Scaffold with the standard five-lane sequence
	// (seedTaskClaimHTTPWorld). One pass then puts it on Implementation,
	// from which three fails -- interleaved with passes so no two are
	// ever consecutive -- trip the cap on the third.
	rec := claimAndCompleteViaHTTP("pass")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	for i := 0; i < store.DefaultThrashCap-1; i++ {
		rec = claimAndCompleteViaHTTP("fail")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
		rec = claimAndCompleteViaHTTP("pass")
		require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	}
	// One final fail from Implementation trips the cap (thrash_count
	// reaches DefaultThrashCap): NextLane would normally revert to
	// Scaffold, but the response must report the held lane instead.
	rec = claimAndCompleteViaHTTP("fail")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))

	assert.Equal(t, "escalated", payload.Task.State)
	require.NotNil(t, payload.Task.EscalationReason)
	assert.Equal(t, "thrash-cap", *payload.Task.EscalationReason)
	assert.Equal(t, "Implementation", payload.Task.CurrentLane, "the response must report the held lane, never the reverted one")
	assert.Nil(t, payload.Task.CurrentClaim, "a thrash-capped complete still closes its own claim like any ordinary complete")

	// A subsequent claim attempt must be refused now that the task is
	// escalated -- proving the response fields above match the store's
	// own resulting claimability, not just its own isolated view.
	rejectedSessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)
	_, err = s.Tasks().ClaimTask(ctx, store.ClaimTaskParams{
		ScopeID: scopeID, TaskID: taskID, SessionID: rejectedSessionID, Acting: self, OnBehalfOf: self,
	})
	assert.ErrorIs(t, err, store.ErrTaskEscalated)
}

// TestCompleteTaskHandler_NonCurrentClaim_Returns409 proves
// writeCompleteStoreError maps a stale/foreign claim id onto a 409
// through the real store, and that a subsequent GET /tasks/{id} shows the
// task untouched.
func TestCompleteTaskHandler_NonCurrentClaim_Returns409(t *testing.T) {
	ctx := context.Background()
	s, pool := newTaskClaimHTTPTestStore(t)
	scopeID := taskClaimHTTPTestScope(t, ctx, pool)
	self := taskClaimHTTPTestSubject()
	taskID, _ := seedTaskClaimHTTPWorld(t, ctx, s, scopeID, self)

	sessions := store.NewSessionStore(pool)
	sessionID, err := sessions.InitSession(ctx, scopeID, self, self, nil)
	require.NoError(t, err)

	assembler := work.NewAssembler(s.Tasks(), slice.NewQuerier(s))
	mux := newTaskCompleteHTTPMux(s, sessions, assembler)

	claimViaHTTP(t, mux, taskID, uuid.UUID(sessionID))

	rec := completeViaHTTP(t, mux, taskID, uuid.New(), uuid.UUID(sessionID), "pass")
	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())

	getReq := httptest.NewRequest(http.MethodGet, "/tasks/"+taskID.String(), nil)
	getRec := httptest.NewRecorder()
	mux.ServeHTTP(getRec, getReq)
	require.Equal(t, http.StatusOK, getRec.Code, getRec.Body.String())

	var payload work.Payload
	require.NoError(t, json.Unmarshal(getRec.Body.Bytes(), &payload))
	assert.Equal(t, "Scaffold", payload.Task.CurrentLane, "a rejected complete must never change current_lane")
	require.NotNil(t, payload.Task.CurrentClaim, "a rejected complete must leave the real current claim untouched")
	assert.False(t, payload.Task.CurrentClaim.Released)
}
