// fakeSessionStore is an in-memory store.SessionStore shared by
// session_test.go and gate_test.go: gate.go and session.go depend only on
// the SessionStore interface, so handler-level tests never need a real
// Postgres (that's krill/store/session_integration_test.go's job).
package handlers_test

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeSessionStore implements store.SessionStore over an in-memory map, and
// optionally injects a fixed error from InitSession for the handler's
// error-path coverage.
type fakeSessionStore struct {
	mu       sync.Mutex
	sessions map[store.SessionID]store.Session
	initErr  error
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: make(map[store.SessionID]store.Session)}
}

func (f *fakeSessionStore) InitSession(ctx context.Context, scopeID uuid.UUID, acting, onBehalfOf store.Subject, whagentSessionID *string) (store.SessionID, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.initErr != nil {
		return store.SessionID{}, f.initErr
	}

	id := store.SessionID(uuid.New())
	f.sessions[id] = store.Session{
		ID:               id,
		ScopeID:          scopeID,
		Acting:           acting,
		OnBehalfOf:       onBehalfOf,
		WhagentSessionID: whagentSessionID,
	}
	return id, nil
}

func (f *fakeSessionStore) GetSession(ctx context.Context, id store.SessionID) (store.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	sess, ok := f.sessions[id]
	if !ok {
		return store.Session{}, store.ErrSessionNotFound
	}
	return sess, nil
}
