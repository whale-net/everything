// Unit tests for MarkShippedHandler/GetDeliveryBreakdownHandler
// (delivery_shipment.go, issue #2686's Testing section): POST
// /milestones/{id}/shipped is gated like every other write endpoint (FR3),
// while GET /milestones/{id}/delivery is an ungated read, mirroring
// milestone_status_test.go's own split for the sibling FR8/FR9/FR12
// surface. No Postgres, no //krill/slice dependency --
// fakeDeliveryShipmentStore/fakeDeliveryBreakdownQuerier
// (fake_delivery_shipment_store_test.go) stand in for
// store.DeliveryShipmentStore and this package's own deliveryBreakdownQuerier
// (krill/store/delivery_shipment_integration_test.go and
// krill/slice/delivery_breakdown_integration_test.go cover the real
// behavior against Postgres).
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

// doMarkShippedRequest mounts handler behind
// handlers.RequireSession(sessions) at "{id}/shipped" -- exactly like
// routes.go's "POST /milestones/{id}/shipped" -- so r.PathValue("id") is
// populated the same way a real request would populate it.
func doMarkShippedRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, id, method, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/{id}/shipped", gated)

	req := httptest.NewRequest(method, "/probe/"+id+"/shipped", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// doGetDeliveryBreakdownRequest drives the ungated GET endpoint directly
// (no RequireSession wrapping, mirroring routes.go), with
// r.PathValue("id") populated via a bare mux mount.
func doGetDeliveryBreakdownRequest(t *testing.T, handler http.HandlerFunc, id string) *httptest.ResponseRecorder {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /probe/{id}/delivery", handler)

	req := httptest.NewRequest(http.MethodGet, "/probe/"+id+"/delivery", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestMarkShippedHandler_ValidSession_MarksShippedWithPathIDAndSubjects
// proves the success path: a valid session lets the request through, and
// the handler passes the path's {id}, the body's entity_id/note, and the
// session's scope/subject pair to MarkShipped unchanged.
func TestMarkShippedHandler_ValidSession_MarksShippedWithPathIDAndSubjects(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	shipments := &fakeDeliveryShipmentStore{}
	milestoneID := uuid.New()
	entityID := uuid.New()

	rec := doMarkShippedRequest(t, handlers.MarkShippedHandler(shipments), sessions, sessionIDStr, milestoneID.String(), http.MethodPost,
		`{"entity_id": "`+entityID.String()+`", "note": "shipped in v1.0"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, shipments.markShippedCalled)
	assert.Equal(t, scopeID, shipments.gotScopeID)
	assert.Equal(t, milestoneID, shipments.gotMilestoneID)
	assert.Equal(t, entityID, shipments.gotEntityID)
	require.NotNil(t, shipments.gotNote)
	assert.Equal(t, "shipped in v1.0", *shipments.gotNote)

	var resp idResponseBody
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, milestoneID.String(), resp.ID)
}

// TestMarkShippedHandler_EntityNotDelivered_Returns409 proves
// store.ErrEntityNotDelivered maps to 409, mirroring
// AddMilepebbleDeliversHandler's ErrMilepebbleDeliversNotSubset handling
// for the same "you cannot ship what the container does not name" shape
// of rejection (delivery_shipment.go's own doc comment).
func TestMarkShippedHandler_EntityNotDelivered_Returns409(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	shipments := &fakeDeliveryShipmentStore{markShippedErr: store.ErrEntityNotDelivered}
	milestoneID := uuid.New()
	entityID := uuid.New()

	rec := doMarkShippedRequest(t, handlers.MarkShippedHandler(shipments), sessions, sessionIDStr, milestoneID.String(), http.MethodPost,
		`{"entity_id": "`+entityID.String()+`"}`)

	assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
}

// TestMarkShippedHandler_WrongMethod_Returns405_NoStoreCall proves a
// non-POST verb never reaches MarkShipped, mirroring
// TestSetMilestoneStatusHandler_WrongMethod_Returns405_NoStoreCall.
func TestMarkShippedHandler_WrongMethod_Returns405_NoStoreCall(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			shipments := &fakeDeliveryShipmentStore{}
			milestoneID := uuid.New()

			rec := doMarkShippedRequest(t, handlers.MarkShippedHandler(shipments), sessions, sessionIDStr, milestoneID.String(), method,
				`{"entity_id": "`+uuid.New().String()+`"}`)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s must never be accepted", method)
			assert.False(t, shipments.markShippedCalled)
		})
	}
}

// TestMarkShippedHandler_InvalidMilestoneID_Returns400_NoStoreCall proves
// a non-UUID path {id} is rejected before MarkShipped is ever called.
func TestMarkShippedHandler_InvalidMilestoneID_Returns400_NoStoreCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	shipments := &fakeDeliveryShipmentStore{}

	rec := doMarkShippedRequest(t, handlers.MarkShippedHandler(shipments), sessions, sessionIDStr, "not-a-uuid", http.MethodPost,
		`{"entity_id": "`+uuid.New().String()+`"}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, shipments.markShippedCalled)
}

// TestGetDeliveryBreakdownHandler_ReturnsMixedBreakdown proves the ungated
// read endpoint combines the container's current status with the
// querier's shipped/unshipped Documents into FR10's acceptance shape.
func TestGetDeliveryBreakdownHandler_ReturnsMixedBreakdown(t *testing.T) {
	statuses := &fakeMilestoneStatusStore{currentStatus: store.MilestoneStatusPartiallyComplete}
	shippedFeature := slice.FeatureEntity{EntityRef: slice.EntityRef{ID: uuid.New()}, Name: "shipped-feature"}
	unshippedFeature := slice.FeatureEntity{EntityRef: slice.EntityRef{ID: uuid.New()}, Name: "unshipped-feature"}
	querier := &fakeDeliveryBreakdownQuerier{
		shipped:   slice.Document{Features: []slice.FeatureEntity{shippedFeature}},
		unshipped: slice.Document{Features: []slice.FeatureEntity{unshippedFeature}},
	}
	id := uuid.New()

	rec := doGetDeliveryBreakdownRequest(t, handlers.GetDeliveryBreakdownHandler(statuses, querier), id.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.DeliveryBreakdownResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "partially complete", resp.Status)
	require.Len(t, resp.Shipped.Features, 1)
	assert.Equal(t, shippedFeature.ID, resp.Shipped.Features[0].ID)
	require.Len(t, resp.Unshipped.Features, 1)
	assert.Equal(t, unshippedFeature.ID, resp.Unshipped.Features[0].ID)
}

// TestGetDeliveryBreakdownHandler_NonexistentID_ReturnsEmptyBreakdown
// proves a nonexistent id reads back as "not started" with an empty
// breakdown, mirroring TestGetMilestoneStatusHandler_ReturnsCurrentStatus's
// "absence of a row" posture (GetDeliveryBreakdownHandler's own doc
// comment).
func TestGetDeliveryBreakdownHandler_NonexistentID_ReturnsEmptyBreakdown(t *testing.T) {
	statuses := &fakeMilestoneStatusStore{}
	querier := &fakeDeliveryBreakdownQuerier{}
	id := uuid.New()

	rec := doGetDeliveryBreakdownRequest(t, handlers.GetDeliveryBreakdownHandler(statuses, querier), id.String())

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var resp handlers.DeliveryBreakdownResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, "not started", resp.Status)
	assert.Empty(t, resp.Shipped.Features)
	assert.Empty(t, resp.Unshipped.Features)
}

// TestGetDeliveryBreakdownHandler_InvalidMilestoneID_Returns400 proves a
// non-UUID path {id} is rejected before either dependency is ever called.
func TestGetDeliveryBreakdownHandler_InvalidMilestoneID_Returns400(t *testing.T) {
	statuses := &fakeMilestoneStatusStore{}
	querier := &fakeDeliveryBreakdownQuerier{}

	rec := doGetDeliveryBreakdownRequest(t, handlers.GetDeliveryBreakdownHandler(statuses, querier), "not-a-uuid")

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}
