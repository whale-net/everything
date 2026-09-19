// Unit tests for AbandonHandler (abandon.go, issue #2688's Testing
// section): POST /milestones/{id}/abandon is gated like every other write
// endpoint (FR6). No Postgres -- fakeAbandonStore (fake_abandon_store_test.go)
// stands in for store.AbandonStore (krill/store/abandon_integration_test.go
// covers the real behavior against Postgres).
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
	"github.com/whale-net/everything/krill/store"
)

// doAbandonRequest mounts handler behind handlers.RequireSession(sessions)
// at "/probe/milestones/{id}/abandon" -- exactly like routes.go's "POST
// /milestones/{id}/abandon" -- so a test proves the write gate is actually
// in front of the handler, not just that the handler works when called
// directly.
func doAbandonRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, method, id, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /probe/milestones/{id}/abandon", gated)

	req := httptest.NewRequest(method, "/probe/milestones/"+id+"/abandon", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// TestAbandonHandler_ValidSession_CallsStoreWithSubjectsAndReturnsResult
// proves the success path: a valid session lets the request through, the
// handler passes {id}/note/subjects unchanged to Abandon, and the
// response reflects the store's AbandonResult, including a nested
// milepebble cascade result.
func TestAbandonHandler_ValidSession_CallsStoreWithSubjectsAndReturnsResult(t *testing.T) {
	sessions, scopeID, sessionIDStr := newTestSession(t)
	containerID := uuid.New()
	backlogID := uuid.New()
	movedID := uuid.New()
	shippedID := uuid.New()
	milepebbleID := uuid.New()
	statusEventID := uuid.New()
	subject := store.Subject{Iss: "https://issuer.example.com", Sub: "actor", Kind: store.SubjectKindService}

	abandons := &fakeAbandonStore{result: store.AbandonResult{
		ContainerID:       containerID,
		BacklogID:         backlogID,
		MovedToBacklogIDs: []uuid.UUID{movedID},
		ShippedIDs:        []uuid.UUID{shippedID},
		StatusEvent: store.MilestoneStatusEvent{
			ID:                  statusEventID,
			MilestoneID:         containerID,
			Status:              store.MilestoneStatusAbandoned,
			CreatedByActing:     subject,
			CreatedByOnBehalfOf: subject,
		},
		MilepebbleResults: []store.AbandonResult{
			{
				ContainerID: milepebbleID,
				BacklogID:   backlogID,
				StatusEvent: store.MilestoneStatusEvent{
					ID:                  uuid.New(),
					MilestoneID:         milepebbleID,
					Status:              store.MilestoneStatusAbandoned,
					CreatedByActing:     subject,
					CreatedByOnBehalfOf: subject,
				},
			},
		},
	}}

	note := "stalled"
	rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, http.MethodPost, containerID.String(),
		`{"note": "`+note+`"}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.True(t, abandons.called)
	assert.Equal(t, scopeID, abandons.gotScopeID)
	assert.Equal(t, containerID, abandons.gotContainer)
	require.NotNil(t, abandons.gotNote)
	assert.Equal(t, note, *abandons.gotNote)

	var resp handlers.AbandonResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, containerID.String(), resp.ContainerID)
	assert.Equal(t, backlogID.String(), resp.BacklogID)
	assert.Equal(t, []string{movedID.String()}, resp.MovedToBacklogIDs)
	assert.Equal(t, []string{shippedID.String()}, resp.ShippedIDs)
	assert.Equal(t, "abandoned", resp.StatusEvent.Status)
	require.Len(t, resp.MilepebbleResults, 1)
	assert.Equal(t, milepebbleID.String(), resp.MilepebbleResults[0].ContainerID)
}

// TestAbandonHandler_NoNote_OmitsNote proves a request body with no note
// field passes a nil Note through to Abandon (note is optional).
func TestAbandonHandler_NoNote_OmitsNote(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	abandons := &fakeAbandonStore{}

	rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, http.MethodPost, uuid.New().String(), `{}`)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Nil(t, abandons.gotNote)
}

// TestAbandonHandler_ErrorMapping proves store.ErrCannotAbandonBacklog and
// store.ErrAlreadyAbandoned both map to 409, mirroring
// MoveScopeHandler's/MarkShippedHandler's own named-rejection handling.
func TestAbandonHandler_ErrorMapping(t *testing.T) {
	for name, err := range map[string]error{
		"ErrCannotAbandonBacklog": store.ErrCannotAbandonBacklog,
		"ErrAlreadyAbandoned":     store.ErrAlreadyAbandoned,
	} {
		t.Run(name, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			abandons := &fakeAbandonStore{err: err}

			rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, http.MethodPost, uuid.New().String(), `{}`)

			assert.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
		})
	}
}

// TestAbandonHandler_NotFound_Returns400 proves store.ErrNotFound (an
// unknown container) maps to 400 via writeStoreError, mirroring every
// other handler in this package.
func TestAbandonHandler_NotFound_Returns400(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	abandons := &fakeAbandonStore{err: store.ErrNotFound}

	rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, http.MethodPost, uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestAbandonHandler_WrongMethod_Returns405_NoStoreCall proves a non-POST
// verb never reaches Abandon.
func TestAbandonHandler_WrongMethod_Returns405_NoStoreCall(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodDelete, http.MethodPatch} {
		t.Run(method, func(t *testing.T) {
			sessions, _, sessionIDStr := newTestSession(t)
			abandons := &fakeAbandonStore{}

			rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, method, uuid.New().String(), `{}`)

			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code, "%s must never be accepted", method)
			assert.False(t, abandons.called)
		})
	}
}

// TestAbandonHandler_InvalidID_Returns400_NoStoreCall proves a non-UUID
// path {id} is rejected before Abandon is ever called.
func TestAbandonHandler_InvalidID_Returns400_NoStoreCall(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t)
	abandons := &fakeAbandonStore{}

	rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, sessionIDStr, http.MethodPost, "not-a-uuid", `{}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.False(t, abandons.called)
}

// TestAbandonHandler_NoSessionHeader_Returns401_NoStoreCall proves the
// write gate (RequireSession) actually sits in front of this handler.
func TestAbandonHandler_NoSessionHeader_Returns401_NoStoreCall(t *testing.T) {
	sessions, _, _ := newTestSession(t)
	abandons := &fakeAbandonStore{}

	rec := doAbandonRequest(t, handlers.AbandonHandler(abandons), sessions, "", http.MethodPost, uuid.New().String(), `{}`)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, rec.Body.String())
	assert.False(t, abandons.called)
}
