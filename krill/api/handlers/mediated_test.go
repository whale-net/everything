// Unit tests for ProposeEntitiesHandler (mediated.go, issue #2546's Testing
// section). No Postgres dependency -- fakeMediatedWriteStore
// (fake_mediated_store_test.go) stands in for store.MediatedWriteStore, so
// this package's tests can drive the handler's error mapping and gating
// wiring without a database (krill/store/mediated_integration_test.go
// covers the real-Postgres transactional write, including atomicity and
// FR9 slice visibility).
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

// doProposeEntitiesRequest wraps handler in handlers.RequireSession(sessions)
// (the exact wiring routes.go uses) and mounts it behind a "{id}" path
// pattern -- exactly like routes.go's "POST /design-sessions/{id}/propose"
// -- so r.PathValue("id") is populated the same way a real request would.
// sessionIDHeader == "" omits the X-Krill-Session-Id header entirely (the
// "no session id" rejection case).
func doProposeEntitiesRequest(t *testing.T, handler http.HandlerFunc, sessions store.SessionStore, sessionIDHeader, sessionPathID, body string) *httptest.ResponseRecorder {
	t.Helper()

	gated := handlers.RequireSession(sessions)(handler)
	mux := http.NewServeMux()
	mux.Handle("POST /design-sessions/{id}/propose", gated)

	req := httptest.NewRequest(http.MethodPost, "/design-sessions/"+sessionPathID+"/propose", bytes.NewBufferString(body))
	if sessionIDHeader != "" {
		req.Header.Set("X-Krill-Session-Id", sessionIDHeader)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// newMediatedTestSession mints a fakeSessionStore-backed session where
// acting != on-behalf-of (a producer-role Agent acting for a Requirement
// Contributor) -- the common case every mediated-intake test needs, unlike
// newTestSession's self-acting shape, which this endpoint always rejects
// (FR10).
func newMediatedTestSession(t *testing.T) (sessions *fakeSessionStore, scopeID uuid.UUID, sessionIDStr string, acting, onBehalfOf store.Subject) {
	t.Helper()
	sessions = newFakeSessionStore()
	scopeID = uuid.New()
	acting = store.Subject{Iss: "https://issuer.example.com", Sub: "producer-agent", Kind: store.SubjectKindService}
	onBehalfOf = store.Subject{Iss: "https://issuer.example.com", Sub: "requirement-contributor", Kind: store.SubjectKindHuman}
	sessionID := mustInitSession(t, sessions, scopeID, acting, onBehalfOf)
	return sessions, scopeID, sessionID.String(), acting, onBehalfOf
}

const validProposeBody = `{
	"verified_against": "abc123",
	"proposals": [
		{"kind": "feature", "parent_id": "` + "00000000-0000-0000-0000-000000000001" + `", "name": "A feature", "position": 1, "summary_line": "proposed a feature"}
	]
}`

// TestProposeEntitiesHandler_NoSessionID_Rejected proves the write gate
// sits in front of this endpoint: a request with no krill session id never
// reaches MediatedWriteStore.ProposeEntities.
func TestProposeEntitiesHandler_NoSessionID_Rejected(t *testing.T) {
	sessions := newFakeSessionStore()
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, "", sessionPathID, validProposeBody)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, uuid.Nil, mediated.gotProposal.ScopeID, "the store must never be called without a valid session")
}

// TestProposeEntitiesHandler_EmptyProposals_Returns400 proves an empty
// proposals array is rejected before the store is ever meaningfully
// consulted for a create.
func TestProposeEntitiesHandler_EmptyProposals_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID,
		`{"verified_against": "abc123", "proposals": []}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestProposeEntitiesHandler_ActingEqualsOnBehalfOf_Returns400WithPlainMessage
// proves FR10: a gating session whose acting and on-behalf-of triples are
// identical is rejected with a message that plainly states a mediated write
// requires an agent acting on a contributor's behalf.
func TestProposeEntitiesHandler_ActingEqualsOnBehalfOf_Returns400WithPlainMessage(t *testing.T) {
	sessions, _, sessionIDStr := newTestSession(t) // self-acting: acting == on_behalf_of
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID, validProposeBody)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), "acting on a contributor's behalf")
}

// TestProposeEntitiesHandler_UnknownDesignSession_Returns404 proves an
// unknown design session id is a 404, not a 400 or 500.
func TestProposeEntitiesHandler_UnknownDesignSession_Returns404(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()
	mediated.proposeErr = fakeErrDesignSessionNotFound(uuid.MustParse(sessionPathID))

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID, validProposeBody)

	assert.Equal(t, http.StatusNotFound, rec.Code, rec.Body.String())
}

// TestProposeEntitiesHandler_DraftWithoutVerifiedAgainst_Returns400 proves
// FR3's rule reaches this endpoint too: draft without verified_against is
// rejected.
func TestProposeEntitiesHandler_DraftWithoutVerifiedAgainst_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID,
		`{"proposals": [{"kind": "feature", "parent_id": "00000000-0000-0000-0000-000000000001", "name": "A feature", "position": 1, "summary_line": "f"}]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestProposeEntitiesHandler_UnrecognizedKind_Returns400 proves a
// proposal's kind is validated against the two-value enum before the store
// is ever called.
func TestProposeEntitiesHandler_UnrecognizedKind_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID,
		`{"verified_against": "abc123", "proposals": [{"kind": "featureset", "parent_id": "00000000-0000-0000-0000-000000000001", "name": "X", "position": 1, "summary_line": "f"}]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
	assert.Equal(t, uuid.Nil, mediated.gotProposal.ScopeID, "an unrecognized kind must never reach the store")
}

// TestProposeEntitiesHandler_BothParentFieldsSet_Returns400 and
// TestProposeEntitiesHandler_NeitherParentFieldSet_Returns400 prove exactly
// one of parent_id/parent_proposal_index must be set.
func TestProposeEntitiesHandler_BothParentFieldsSet_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID,
		`{"verified_against": "abc123", "proposals": [{"kind": "feature", "parent_id": "00000000-0000-0000-0000-000000000001", "parent_proposal_index": 0, "name": "X", "position": 1, "summary_line": "f"}]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

func TestProposeEntitiesHandler_NeitherParentFieldSet_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID,
		`{"verified_against": "abc123", "proposals": [{"kind": "feature", "name": "X", "position": 1, "summary_line": "f"}]}`)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestProposeEntitiesHandler_BodyHasNoIdentityOrScopeField_Success proves
// this endpoint's gating invariant: scope_id and both identity triples come
// only from the gating session, never the body -- proposeEntitiesRequest
// declares no such field at all, so a well-formed body with none of them
// still succeeds, and the store receives the session's own values.
func TestProposeEntitiesHandler_BodyHasNoIdentityOrScopeField_Success(t *testing.T) {
	sessions, scopeID, sessionIDStr, acting, onBehalfOf := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID, validProposeBody)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, scopeID, mediated.gotProposal.ScopeID, "scope_id must come from the session, never the request body")
	assert.Equal(t, acting, mediated.gotProposal.Acting)
	assert.Equal(t, onBehalfOf, mediated.gotProposal.OnBehalfOf)
	assert.Equal(t, store.EventTypeDraft, mediated.gotProposal.EventType, "event_type is always draft on this path, never a caller-chosen value")
}

// TestProposeEntitiesHandler_UnknownParent_Returns400 proves an unknown or
// cross-scope proposal parent (store.ErrNotFound from the store's own
// currentRowExists check, distinct from an unknown design session) maps to
// 400, matching every other Create* endpoint's unknown-parent case.
func TestProposeEntitiesHandler_UnknownParent_Returns400(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	body := `{"verified_against": "abc123", "proposals": [{"kind": "feature", "parent_id": "` + unknownParentID.String() + `", "name": "X", "position": 1, "summary_line": "f"}]}`

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID, body)

	assert.Equal(t, http.StatusBadRequest, rec.Code, rec.Body.String())
}

// TestProposeEntitiesHandler_Success_ReturnsEntitiesAndSeqNo proves the
// response shape: a 201 body with the new revision_event's id/seq_no, and
// every created entity's kind and id in request order.
func TestProposeEntitiesHandler_Success_ReturnsEntitiesAndSeqNo(t *testing.T) {
	sessions, _, sessionIDStr, _, _ := newMediatedTestSession(t)
	mediated := newFakeMediatedWriteStore()
	sessionPathID := uuid.New().String()

	body := `{
		"verified_against": "abc123",
		"proposals": [
			{"kind": "feature", "parent_id": "00000000-0000-0000-0000-000000000001", "name": "A feature", "position": 1, "summary_line": "f"},
			{"kind": "requirement", "parent_proposal_index": 0, "requirement_kind": "FR", "name": "An FR", "position": 1, "summary_line": "fr"}
		]
	}`

	rec := doProposeEntitiesRequest(t, handlers.ProposeEntitiesHandler(mediated), sessions, sessionIDStr, sessionPathID, body)

	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var resp struct {
		RevisionEventID string `json:"revision_event_id"`
		SeqNo           int    `json:"seq_no"`
		Entities        []struct {
			Kind string `json:"kind"`
			ID   string `json:"id"`
		} `json:"entities"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.NotEmpty(t, resp.RevisionEventID)
	assert.Equal(t, 1, resp.SeqNo)
	require.Len(t, resp.Entities, 2)
	assert.Equal(t, "feature", resp.Entities[0].Kind)
	assert.Equal(t, "requirement", resp.Entities[1].Kind)
	assert.NotEmpty(t, resp.Entities[0].ID)
	assert.NotEmpty(t, resp.Entities[1].ID)
}
