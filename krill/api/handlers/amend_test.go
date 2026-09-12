// Unit tests for AmendRequirementHandler/AmendLoadBearingDecisionHandler
// (amend.go, issue #2493's Testing section): amend is one of the six write
// paths FR3 gates -- a request with no session id must never reach
// AmendStore at all, and a request with a valid session id must reach it
// with the path's {id} and the request body's name/body passed through
// unchanged. No Postgres dependency -- fakeAmendStore (fake_entity_store_
// test.go) stands in for store.AmendStore (krill/store/amend_integration_
// test.go covers the real SCD2 close-and-open write against Postgres).
package handlers_test

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

// doAmendRequest wraps handler in handlers.RequireSession(sessions) (the
// exact wiring routes.go uses for both amend endpoints) and mounts it
// behind a "{id}" path pattern -- exactly like routes.go's
// "POST /requirements/{id}/amend" -- so r.PathValue("id") is populated the
// same way a real request would populate it. sessionIDHeader == "" omits
// the X-Krill-Session-Id header entirely (the "no session id" rejection
// case).
func doAmendRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/amend", gated)

	req := httptest.NewRequest(http.MethodPost, "/probe/"+id+"/amend", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAmendRequirementHandler_NoSessionID_Rejected proves amend is gated
// like every other write endpoint (FR3): no session id means AmendStore is
// never called.
func TestAmendRequirementHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	amend := &fakeAmendStore{}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendRequirementHandler(amend), sessions, "", id.String(),
		`{"name": "Amended"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, amend.gotRequirementID, "an ungated request must never reach AmendStore")
}

// TestAmendRequirementHandler_ValidSession_CallsStoreWithPathIDAndBody
// proves the success path: a valid session id lets the request through,
// and the handler passes the path's {id} and the body's name/body to
// AmendStore unchanged.
func TestAmendRequirementHandler_ValidSession_CallsStoreWithPathIDAndBody(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	amend := &fakeAmendStore{}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendRequirementHandler(amend), sessions, sessionIDStr, id.String(),
		`{"name": "Amended", "body": "new body"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, id, amend.gotRequirementID)
	assert.Equal(t, "Amended", amend.gotRequirementName)
	require.NotNil(t, amend.gotRequirementBody)
	assert.Equal(t, "new body", *amend.gotRequirementBody)
}

// TestAmendRequirementHandler_MissingName_Rejected proves the shared
// RequireNonEmpty("name", ...) validation applies to amend too.
func TestAmendRequirementHandler_MissingName_Rejected(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	amend := &fakeAmendStore{}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendRequirementHandler(amend), sessions, sessionIDStr, id.String(),
		`{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, amend.gotRequirementID, "a missing name must never reach AmendStore")
}

// TestAmendRequirementHandler_UnknownID_Returns400 proves store.ErrNotFound
// (amend of an id with no current row) maps through writeStoreError
// (types.go) exactly like every other write handler's not-found case --
// a 400, not a 404 (that mapping is history.go's writeHistoryError, a
// distinct read-side error mapper -- see its own doc comment).
func TestAmendRequirementHandler_UnknownID_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	amend := &fakeAmendStore{amendErr: store.ErrNotFound}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendRequirementHandler(amend), sessions, sessionIDStr, id.String(),
		`{"name": "Amended"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAmendLoadBearingDecisionHandler_NoSessionID_Rejected mirrors
// TestAmendRequirementHandler_NoSessionID_Rejected for the decision amend
// endpoint.
func TestAmendLoadBearingDecisionHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	amend := &fakeAmendStore{}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendLoadBearingDecisionHandler(amend), sessions, "", id.String(),
		`{"name": "Amended"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, amend.gotDecisionID, "an ungated request must never reach AmendStore")
}

// TestAmendLoadBearingDecisionHandler_ValidSession_CallsStoreWithPathIDAndBody
// mirrors TestAmendRequirementHandler_ValidSession_CallsStoreWithPathIDAndBody
// for the decision amend endpoint.
func TestAmendLoadBearingDecisionHandler_ValidSession_CallsStoreWithPathIDAndBody(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	amend := &fakeAmendStore{}
	id := uuid.New()

	rec := doAmendRequest(t, handlers.AmendLoadBearingDecisionHandler(amend), sessions, sessionIDStr, id.String(),
		`{"name": "Amended"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Equal(t, id, amend.gotDecisionID)
	assert.Equal(t, "Amended", amend.gotDecisionName)
}
