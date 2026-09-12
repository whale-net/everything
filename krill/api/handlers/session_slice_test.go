// External (black-box) tests for SessionSlice (session_slice.go, issue
// #2544's Testing section): the union-and-dedupe logic that bridges
// store.RevisionEventStore to krill/slice's GetEntitySetSlice, plus the
// handler's not-found/empty-session status mapping.
package handlers_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/whale-net/everything/krill/api/handlers"
	"github.com/whale-net/everything/krill/store"
)

func newSessionSliceTestServer(sessions *fakeDesignSessionStore, events *fakeRevisionEventStore, querier *fakeEntitySetQuerier) *httptest.Server {
	mux := http.NewServeMux()
	handlers.NewSessionSlice(sessions, events, querier).Register(mux)
	return httptest.NewServer(mux)
}

func revisionEventWithDeltas(sessionID uuid.UUID, ids ...uuid.UUID) store.RevisionEvent {
	deltas := make([]store.EntityDelta, len(ids))
	for i, id := range ids {
		deltas[i] = store.EntityDelta{EntityID: id, Change: store.EntityDeltaChangeCreated, SummaryLine: "did a thing"}
	}
	return store.RevisionEvent{ID: uuid.New(), SessionID: sessionID, EntityDeltas: deltas}
}

// TestSessionSlice_UnionsAndDedupesAcrossEvents proves a session with
// three events touching overlapping entity ids results in exactly one
// call to the querier, carrying the deduplicated union of every event's
// entity_deltas.
func TestSessionSlice_UnionsAndDedupesAcrossEvents(t *testing.T) {
	sessionID := uuid.New()
	idA, idB, idC := uuid.New(), uuid.New(), uuid.New()

	sessions := newFakeDesignSessionStore()
	sessions.put(store.DesignSession{ID: sessionID})

	events := newFakeRevisionEventStore()
	events.seed(sessionID,
		revisionEventWithDeltas(sessionID, idA, idB),
		revisionEventWithDeltas(sessionID, idB, idC), // idB overlaps with the first event
		revisionEventWithDeltas(sessionID, idA),      // idA overlaps with the first event
	)

	querier := &fakeEntitySetQuerier{}
	srv := newSessionSliceTestServer(sessions, events, querier)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/design-sessions/" + sessionID.String() + "/slice")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, querier.calls, 1, "the handler must call the querier exactly once, not once per event")
	assert.ElementsMatch(t, []uuid.UUID{idA, idB, idC}, querier.calls[0], "the querier must receive the deduplicated union of every event's entity_deltas")
}

// TestSessionSlice_UnknownSession_Returns404 proves a session id with no
// design_session row 404s before the querier is ever consulted.
func TestSessionSlice_UnknownSession_Returns404(t *testing.T) {
	sessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()
	querier := &fakeEntitySetQuerier{}
	srv := newSessionSliceTestServer(sessions, events, querier)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/design-sessions/" + uuid.NewString() + "/slice")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
	assert.Empty(t, querier.calls, "an unknown session must never reach the querier")
}

// TestSessionSlice_EmptySession_Returns200WithEmptyDocument proves a
// session with zero revision events still 200s, with an empty document,
// rather than erroring.
func TestSessionSlice_EmptySession_Returns200WithEmptyDocument(t *testing.T) {
	sessionID := uuid.New()

	sessions := newFakeDesignSessionStore()
	sessions.put(store.DesignSession{ID: sessionID})
	events := newFakeRevisionEventStore()
	querier := &fakeEntitySetQuerier{}
	srv := newSessionSliceTestServer(sessions, events, querier)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/design-sessions/" + sessionID.String() + "/slice")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, querier.calls, 1)
	assert.Empty(t, querier.calls[0], "a session with no events must call the querier with an empty id set")
}

// TestSessionSlice_InvalidID_Returns400 mirrors slice_internal_test.go's
// id-validation coverage for the other granularities.
func TestSessionSlice_InvalidID_Returns400(t *testing.T) {
	sessions := newFakeDesignSessionStore()
	events := newFakeRevisionEventStore()
	querier := &fakeEntitySetQuerier{}
	srv := newSessionSliceTestServer(sessions, events, querier)
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/design-sessions/not-a-uuid/slice")
	require.NoError(t, err)
	defer resp.Body.Close()

	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Empty(t, querier.calls)
}
