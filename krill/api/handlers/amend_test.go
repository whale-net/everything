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

// ── every spec-axis kind, and the two rules that hold across all of them ──

// amendEndpoint pairs each spec-axis kind's amend handler with a body that
// is valid for that kind and no other -- every endpoint decodes strictly
// against its own per-kind shape, so one table-driven test can cover all
// eight without a body that would be rejected as an unknown field.
type amendEndpoint struct {
	handler func(store.AmendStore) http.HandlerFunc
	body    string
}

var amendEndpoints = map[string]amendEndpoint{
	"product":               {handlers.AmendProductHandler, `{"name": "Amended", "vision": "new vision"}`},
	"feature set":           {handlers.AmendFeatureSetHandler, `{"name": "Amended", "description": "new description"}`},
	"feature":               {handlers.AmendFeatureHandler, `{"name": "Amended", "description": "new description"}`},
	"requirement":           {handlers.AmendRequirementHandler, `{"name": "Amended", "body": "new body"}`},
	"persona":               {handlers.AmendPersonaHandler, `{"name": "Amended", "description": "new description"}`},
	"non-goal":              {handlers.AmendNonGoalHandler, `{"name": "Amended", "body": "new body"}`},
	"load-bearing decision": {handlers.AmendLoadBearingDecisionHandler, `{"name": "Amended", "body": "new body"}`},
	"milestone":             {handlers.AmendMilestoneHandler, `{"name": "Amended", "outcome": "new outcome"}`},
}

// TestAmendHandlers_ValidSessionReachesStore proves each of the eight
// endpoints is gated like every other write path and passes the path's id
// plus the body's replacement content through to store.AmendStore
// unchanged.
func TestAmendHandlers_ValidSessionReachesStore(t *testing.T) {
	for kind, endpoint := range amendEndpoints {
		t.Run(kind, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			amend := &fakeAmendStore{}
			id := uuid.New()

			rec := doAmendRequest(t, endpoint.handler(amend), sessions, sessionIDStr, id.String(), endpoint.body)

			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Len(t, amend.calls, 1)
			assert.Equal(t, id, amend.calls[0].id)
			assert.Equal(t, "Amended", amend.calls[0].name)
		})
	}
}

// TestAmendHandlers_NoSessionID_Rejected proves every one of the eight is
// behind RequireSession (FR3): no session id means the store is never
// reached.
func TestAmendHandlers_NoSessionID_Rejected(t *testing.T) {
	for kind, endpoint := range amendEndpoints {
		t.Run(kind, func(t *testing.T) {
			sessions := newFakeSessionStore()
			amend := &fakeAmendStore{}

			rec := doAmendRequest(t, endpoint.handler(amend), sessions, "", uuid.New().String(), endpoint.body)

			assert.Equal(t, http.StatusUnauthorized, rec.Code)
			assert.Empty(t, amend.calls, "an ungated request must never reach AmendStore")
		})
	}
}

// TestAmendHandlers_RefuseReparentAndReKind is FR f0f6bc18 at the surface:
// a body carrying a parent or a kind is refused by name, with a 400, and
// never reaches the store -- whatever the caller was trying to move or
// re-kind.
func TestAmendHandlers_RefuseReparentAndReKind(t *testing.T) {
	for kind, endpoint := range amendEndpoints {
		for field, value := range map[string]string{
			"product_id":          uuid.NewString(),
			"feature_set_id":      uuid.NewString(),
			"feature_id":          uuid.NewString(),
			"parent_milestone_id": uuid.NewString(),
			"kind":                "milepebble",
		} {
			t.Run(kind+"/"+field, func(t *testing.T) {
				sessions, _, sessionIDStr := newTestSession(t)
				amend := &fakeAmendStore{}

				rec := doAmendRequest(t, endpoint.handler(amend), sessions, sessionIDStr, uuid.New().String(),
					`{"name": "Amended", "`+field+`": "`+value+`"}`)

				assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
				assert.Contains(t, rec.Body.String(), "amend cannot reparent or re-kind")
				assert.Contains(t, rec.Body.String(), field, "the refusal must name the field the caller tried to change")
				assert.Empty(t, amend.calls, "a refused reparent or re-kind must never reach AmendStore")
			})
		}
	}
}

// TestAmendHandlers_NameConflictReturns409 proves store.ErrNameConflict --
// an amend whose replacement name collides with a live sibling (FR
// b2767a89) -- maps to 409, the same conflict status a create's collision
// gets.
func TestAmendHandlers_NameConflictReturns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	amend := &fakeAmendStore{amendErr: store.ErrNameConflict}

	rec := doAmendRequest(t, handlers.AmendRequirementHandler(amend), sessions, sessionIDStr, uuid.New().String(),
		`{"name": "A Name Another Live Sibling Already Holds"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "already has this name")
}

// TestAmendPlacementChange_Refuse pins the store-side rule the surfaces
// share: no placement field set means no refusal, and the first one set is
// the one named.
func TestAmendPlacementChange_Refuse(t *testing.T) {
	none := store.AmendPlacementChange{}
	assert.NoError(t, none.Refuse("feature"), "an amend that changes nothing about placement is allowed")

	productID := uuid.NewString()
	changed := store.AmendPlacementChange{ProductID: &productID, Kind: strPtr("permanent")}
	err := changed.Refuse("feature")
	require.ErrorIs(t, err, store.ErrPlacementChange)
	assert.Contains(t, err.Error(), "cannot change product_id on amend", "the first offending field is the one named")
	assert.NotContains(t, err.Error(), "cannot change kind on amend")
}

func strPtr(s string) *string { return &s }
