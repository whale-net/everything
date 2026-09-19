// Unit tests for MoveScopeHandler/GetBacklogHandler (recut.go, issue
// #2687's Testing section): POST /delivery/move is gated like every other
// write endpoint (FR5), while GET /products/{id}/backlog is an ungated
// read, mirroring delivery_shipment_test.go's own split for the sibling
// FR9/FR10 surface. No Postgres, no //krill/slice dependency --
// fakeRecutStore/fakeBacklogQuerier (fake_recut_store_test.go) stand in
// for store.RecutStore and this package's own backlogQuerier
// (krill/store/recut_integration_test.go and krill/slice's own
// integration test cover the real behavior against Postgres).
package handlers_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// doMoveScopeRequest mounts handler behind
// handlers.RequireSession(sessions) at "/delivery/move" -- exactly like
// routes.go's "POST /delivery/move" -- so a test proves the write gate is
// actually in front of the handler, not just that the handler works when
// called directly.
func doMoveScopeRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, method, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/delivery/move", gated)

	req := httptest.NewRequest(method, "/probe/delivery/move", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doGetBacklogRequest drives the ungated GET endpoint directly (no
// RequireSession wrapping, mirroring routes.go), with r.PathValue("id")
// populated via a bare mux mount.
func doGetBacklogRequest(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/products/{id}/backlog", handler)

	req := httptest.NewRequest(http.MethodGet, "/probe/products/"+id+"/backlog", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestMoveScopeHandler_ValidSession_MovesWithBodyAndSubjects proves the
// success path: a valid session lets the request through, and the handler
// passes the body's entity_ids/from/to and the session's scope/subject
// pair to MoveScope unchanged.
func TestMoveScopeHandler_ValidSession_MovesWithBodyAndSubjects(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	recut := &fakeRecutStore{}
	entityID := uuid.New()
	from := uuid.New()
	to := uuid.New()

	rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, sessionIDStr, http.MethodPost,
		`{"entity_ids": ["`+entityID.String()+`"], "from": "`+from.String()+`", "to": "`+to.String()+`"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, recut.moveScopeCalled)
	assert.Equal(t, scopeID, recut.gotScopeID)
	assert.Equal(t, []uuid.UUID{entityID}, recut.gotEntityIDs)
	assert.Equal(t, from, recut.gotFrom)
	assert.Equal(t, to, recut.gotTo)

	var resp handlers.MoveScopeResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []string{entityID.String()}, resp.EntityIDs)
	assert.Equal(t, from.String(), resp.From)
	assert.Equal(t, to.String(), resp.To)
}

// TestMoveScopeHandler_ErrorMapping proves every named MoveScope
// rejection (store.ErrEntityNotInContainer, store.ErrEntityShipped,
// store.ErrMilepebbleDeliversNotSubset) maps to 409, mirroring
// MarkShippedHandler's own ErrEntityNotDelivered handling for the same
// "you cannot move what the container does not name/allow" shape of
// rejection (recut.go's own doc comment).
func TestMoveScopeHandler_ErrorMapping(t *testing.T) {
	for name, err := range map[string]error{
		"ErrEntityNotInContainer":       store.ErrEntityNotInContainer,
		"ErrEntityShipped":              store.ErrEntityShipped,
		"ErrMilepebbleDeliversNotSubset": store.ErrMilepebbleDeliversNotSubset,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			recut := &fakeRecutStore{moveScopeErr: err}

			rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, sessionIDStr, http.MethodPost,
				`{"entity_ids": ["`+uuid.New().String()+`"], "from": "`+uuid.New().String()+`", "to": "`+uuid.New().String()+`"}`)

			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		})
	}
}

// TestMoveScopeHandler_WrongMethod_Returns405_NoStoreCall proves a
// non-POST verb never reaches MoveScope.
func TestMoveScopeHandler_WrongMethod_Returns405_NoStoreCall(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			recut := &fakeRecutStore{}

			rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, sessionIDStr, method,
				`{"entity_ids": ["`+uuid.New().String()+`"], "from": "`+uuid.New().String()+`", "to": "`+uuid.New().String()+`"}`)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s must never be accepted", method)
			assert.False(t, recut.moveScopeCalled)
		})
	}
}

// TestMoveScopeHandler_EmptyEntityIDs_Returns400_NoStoreCall proves an
// empty entity_ids list is rejected before MoveScope is ever called.
func TestMoveScopeHandler_EmptyEntityIDs_Returns400_NoStoreCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	recut := &fakeRecutStore{}

	rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, sessionIDStr, http.MethodPost,
		`{"entity_ids": [], "from": "`+uuid.New().String()+`", "to": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, recut.moveScopeCalled)
}

// TestMoveScopeHandler_InvalidUUIDFields_Returns400_NoStoreCall proves a
// non-UUID entity id, from, or to is rejected before MoveScope is ever
// called.
func TestMoveScopeHandler_InvalidUUIDFields_Returns400_NoStoreCall(t *testing.T) {
	valid := uuid.New().String()
	for name, body := range map[string]string{
		"bad entity id": `{"entity_ids": ["not-a-uuid"], "from": "` + valid + `", "to": "` + valid + `"}`,
		"bad from":      `{"entity_ids": ["` + valid + `"], "from": "not-a-uuid", "to": "` + valid + `"}`,
		"bad to":        `{"entity_ids": ["` + valid + `"], "from": "` + valid + `", "to": "not-a-uuid"}`,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			recut := &fakeRecutStore{}

			rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, sessionIDStr, http.MethodPost, body)

			assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
			assert.False(t, recut.moveScopeCalled)
		})
	}
}

// TestMoveScopeHandler_NoSessionHeader_Returns401_NoStoreCall proves the
// write gate (RequireSession) actually sits in front of this handler.
func TestMoveScopeHandler_NoSessionHeader_Returns401_NoStoreCall(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	recut := &fakeRecutStore{}

	rec := doMoveScopeRequest(t, handlers.MoveScopeHandler(recut), sessions, "", http.MethodPost,
		`{"entity_ids": ["`+uuid.New().String()+`"], "from": "`+uuid.New().String()+`", "to": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.False(t, recut.moveScopeCalled)
}

// TestGetBacklogHandler_ReturnsDocument proves the ungated read endpoint
// returns the querier's Document as-is.
func TestGetBacklogHandler_ReturnsDocument(t *testing.T) {
	backlogged := slice.FeatureEntity{EntityRef: slice.EntityRef{ID: uuid.New()}, Name: "backlogged-feature"}
	querier := &fakeBacklogQuerier{doc: slice.Document{Features: []slice.FeatureEntity{backlogged}}}
	productID := uuid.New()

	rec := doGetBacklogRequest(t, handlers.GetBacklogHandler(querier), productID.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp slice.Document
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Len(t, resp.Features, 1)
	assert.Equal(t, backlogged.ID, resp.Features[0].ID)
}

// TestGetBacklogHandler_NotFound_Returns404 proves store.ErrNotFound maps
// to 404.
func TestGetBacklogHandler_NotFound_Returns404(t *testing.T) {
	querier := &fakeBacklogQuerier{err: store.ErrNotFound}

	rec := doGetBacklogRequest(t, handlers.GetBacklogHandler(querier), uuid.New().String())

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestGetBacklogHandler_InvalidProductID_Returns400 proves a non-UUID
// path {id} is rejected before the querier is ever called.
func TestGetBacklogHandler_InvalidProductID_Returns400(t *testing.T) {
	querier := &fakeBacklogQuerier{}

	rec := doGetBacklogRequest(t, handlers.GetBacklogHandler(querier), "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
