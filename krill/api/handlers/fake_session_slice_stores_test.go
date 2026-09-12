// fakeDesignSessionStore, fakeRevisionEventStore, and
// fakeEntitySetQuerier are in-memory stand-ins for SessionSlice's three
// dependencies (session_slice.go, issue #2544): session_slice_test.go
// drives SessionSlice.handle against these instead of a real Postgres or
// *slice.Querier, mirroring fake_session_store_test.go's precedent for
// this package's other handler tests.
package handlers_test

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// fakeDesignSessionStore implements store.DesignSessionStore over an
// in-memory map. Only GetByID is exercised by session_slice_test.go; Open
// and ListByProduct are implemented so the type satisfies the interface.
type fakeDesignSessionStore struct {
	mu       sync.Mutex
	sessions map[uuid.UUID]store.DesignSession
}

func newFakeDesignSessionStore() *fakeDesignSessionStore {
	return &fakeDesignSessionStore{sessions: make(map[uuid.UUID]store.DesignSession)}
}

func (f *fakeDesignSessionStore) put(ds store.DesignSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[ds.ID] = ds
}

func (f *fakeDesignSessionStore) Open(ctx context.Context, scopeID, productID uuid.UUID, openingSubmission string, openedByKrillSessionID store.SessionID) (store.DesignSession, error) {
	ds := store.DesignSession{
		ID:                     uuid.New(),
		ScopeID:                scopeID,
		ProductID:              productID,
		OpeningSubmission:      openingSubmission,
		OpenedByKrillSessionID: openedByKrillSessionID,
	}
	f.put(ds)
	return ds, nil
}

func (f *fakeDesignSessionStore) GetByID(ctx context.Context, id uuid.UUID) (store.DesignSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ds, ok := f.sessions[id]
	if !ok {
		return store.DesignSession{}, store.ErrNotFound
	}
	return ds, nil
}

func (f *fakeDesignSessionStore) ListByProduct(ctx context.Context, productID uuid.UUID) ([]store.DesignSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	var out []store.DesignSession
	for _, ds := range f.sessions {
		if ds.ProductID == productID {
			out = append(out, ds)
		}
	}
	return out, nil
}

var _ store.DesignSessionStore = (*fakeDesignSessionStore)(nil)

// fakeRevisionEventStore implements store.RevisionEventStore over an
// in-memory, per-session slice. Only ListBySession is exercised by
// session_slice_test.go; Append is implemented so the type satisfies the
// interface (no seq_no/locking discipline needed here -- that is
// RevisionEventStore.Append's own concern, covered by
// revision_event_integration_test.go).
type fakeRevisionEventStore struct {
	mu     sync.Mutex
	events map[uuid.UUID][]store.RevisionEvent
}

func newFakeRevisionEventStore() *fakeRevisionEventStore {
	return &fakeRevisionEventStore{events: make(map[uuid.UUID][]store.RevisionEvent)}
}

func (f *fakeRevisionEventStore) seed(sessionID uuid.UUID, events ...store.RevisionEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[sessionID] = append(f.events[sessionID], events...)
}

func (f *fakeRevisionEventStore) Append(ctx context.Context, e store.NewRevisionEvent) (store.RevisionEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ev := store.RevisionEvent{
		ID:                 uuid.New(),
		ScopeID:            e.ScopeID,
		SessionID:          e.SessionID,
		SeqNo:              len(f.events[e.SessionID]) + 1,
		Acting:             e.Acting,
		OnBehalfOf:         e.OnBehalfOf,
		EventType:          e.EventType,
		EntityDeltas:       e.EntityDeltas,
		OpenQuestionsDelta: e.OpenQuestionsDelta,
		VerifiedAgainst:    e.VerifiedAgainst,
		SignoffStatus:      e.SignoffStatus,
	}
	f.events[e.SessionID] = append(f.events[e.SessionID], ev)
	return ev, nil
}

func (f *fakeRevisionEventStore) ListBySession(ctx context.Context, sessionID uuid.UUID) ([]store.RevisionEvent, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.RevisionEvent(nil), f.events[sessionID]...), nil
}

var _ store.RevisionEventStore = (*fakeRevisionEventStore)(nil)

// fakeEntitySetQuerier records every call it receives (session_slice_test.go
// asserts against calls to prove the handler unions/dedupes before ever
// calling the querier) and returns a fixed doc/err pair.
type fakeEntitySetQuerier struct {
	mu    sync.Mutex
	calls [][]uuid.UUID
	doc   slice.Document
	err   error
}

func (f *fakeEntitySetQuerier) GetEntitySetSlice(ctx context.Context, entityIDs []uuid.UUID) (slice.Document, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, entityIDs)
	return f.doc, f.err
}
