// fakeDesignSessionStore/fakeRevisionEventStore are in-memory
// store.DesignSessionStore/store.RevisionEventStore implementations shared
// by design_session_test.go and revision_event_test.go (issue #2543's
// Testing section): OpenDesignSessionHandler/GetDesignSessionHandler/
// AppendRevisionEventHandler depend only on these interfaces, so
// handler-level tests never need a real Postgres (that is
// krill/store/design_session_integration_test.go's and
// krill/store/revision_event_integration_test.go's job). Each fake records
// the arguments its write method saw (for asserting scope_id and identity
// pass-through, FR1/FR2) and can be told to fail with a fixed error, so a
// test can drive writeStoreError's/writeRevisionEventError's mapping
// without a database at all.
package handlers_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeDesignSessionStore backs design_session_test.go.
type fakeDesignSessionStore struct {
	openErr error

	// gotScopeID/gotProductID/gotOpeningSubmission/gotOpenedBy record the
	// last Open call's arguments.
	gotScopeID                uuid.UUID
	gotProductID              uuid.UUID
	gotOpeningSubmission      string
	gotOpenedByKrillSessionID store.SessionID

	mu       sync.Mutex
	sessions map[uuid.UUID]store.DesignSession
}

func newFakeDesignSessionStore() *fakeDesignSessionStore {
	return &fakeDesignSessionStore{sessions: make(map[uuid.UUID]store.DesignSession)}
}

func (f *fakeDesignSessionStore) Open(ctx context.Context, scopeID, productID uuid.UUID, openingSubmission string, openedByKrillSessionID store.SessionID) (store.DesignSession, error) {
	f.gotScopeID, f.gotProductID, f.gotOpeningSubmission, f.gotOpenedByKrillSessionID = scopeID, productID, openingSubmission, openedByKrillSessionID
	if f.openErr != nil {
		return store.DesignSession{}, f.openErr
	}

	ds := store.DesignSession{
		ID:                     uuid.New(),
		ScopeID:                scopeID,
		ProductID:              productID,
		OpeningSubmission:      openingSubmission,
		OpenedByKrillSessionID: openedByKrillSessionID,
	}
	f.mu.Lock()
	f.sessions[ds.ID] = ds
	f.mu.Unlock()
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
	return nil, nil
}

// put seeds ds directly into the fake, bypassing Open -- used by
// GetDesignSessionHandler/AppendRevisionEventHandler tests that need a
// known design_session id to already exist.
func (f *fakeDesignSessionStore) put(ds store.DesignSession) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[ds.ID] = ds
}

// fakeRevisionEventStore backs revision_event_test.go. Append replicates
// store.validateNewRevisionEvent's FR3/FR4/entity_deltas rules (that
// function is unexported, so this is a deliberate small duplicate, not a
// call-through) so a handler-level test can drive both the 201 and 400
// outcomes AppendRevisionEventHandler's writeRevisionEventError maps,
// without a database.
type fakeRevisionEventStore struct {
	// appendErr, if set, short-circuits Append with this error -- used to
	// simulate an unknown design_session id (store.ErrNotFound, mirroring
	// the real store's FOR UPDATE lock miss) without needing this fake to
	// track cross-references to a design_session store.
	appendErr error

	// gotEvent records the last Append call's argument in full, so a test
	// can assert scope_id/identity/event fields were passed through from
	// the gating session and request body unchanged.
	gotEvent store.NewRevisionEvent

	mu     sync.Mutex
	events map[uuid.UUID][]store.RevisionEvent
}

func newFakeRevisionEventStore() *fakeRevisionEventStore {
	return &fakeRevisionEventStore{events: make(map[uuid.UUID][]store.RevisionEvent)}
}

// seed appends events directly into the fake, bypassing Append (and its
// FR3/FR4 validation) -- used by session_slice_test.go (issue #2544), which
// needs known events already in place and does not exercise Append's own
// validation rules.
func (f *fakeRevisionEventStore) seed(sessionID uuid.UUID, events ...store.RevisionEvent) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events[sessionID] = append(f.events[sessionID], events...)
}

func (f *fakeRevisionEventStore) Append(ctx context.Context, e store.NewRevisionEvent) (store.RevisionEvent, error) {
	f.gotEvent = e
	if f.appendErr != nil {
		return store.RevisionEvent{}, f.appendErr
	}
	if err := fakeValidateNewRevisionEvent(e); err != nil {
		return store.RevisionEvent{}, err
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	seqNo := len(f.events[e.SessionID]) + 1
	ev := store.RevisionEvent{
		ID:                 uuid.New(),
		ScopeID:            e.ScopeID,
		SessionID:          e.SessionID,
		SeqNo:              seqNo,
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
	return f.events[sessionID], nil
}

// fakeValidateNewRevisionEvent mirrors krill/store/revision_event.go's
// validateNewRevisionEvent (FR3's VerifiedAgainst rule, FR4's SignoffStatus
// rule, FR2's entity_deltas.change enum). Kept deliberately tiny and
// comment-linked so the two do not silently drift; the real rule is only
// ever enforced for real by the integration test against Postgres
// (krill/store/revision_event_integration_test.go) -- this copy exists so
// this package's handler tests can prove AppendRevisionEventHandler's error
// mapping without a database.
func fakeValidateNewRevisionEvent(e store.NewRevisionEvent) error {
	requiresVerifiedAgainst := e.EventType == store.EventTypeDraft || e.EventType == store.EventTypeReconciliation
	switch {
	case requiresVerifiedAgainst && (e.VerifiedAgainst == nil || *e.VerifiedAgainst == ""):
		return fmt.Errorf("%w: verified_against is required for event_type %q", store.ErrInvalidRevisionEvent, e.EventType)
	case !requiresVerifiedAgainst && e.VerifiedAgainst != nil:
		return fmt.Errorf("%w: verified_against must be nil for event_type %q", store.ErrInvalidRevisionEvent, e.EventType)
	}

	requiresSignoffStatus := e.EventType == store.EventTypeSignoff
	switch {
	case requiresSignoffStatus && e.SignoffStatus == nil:
		return fmt.Errorf("%w: signoff_status is required for event_type %q", store.ErrInvalidRevisionEvent, e.EventType)
	case !requiresSignoffStatus && e.SignoffStatus != nil:
		return fmt.Errorf("%w: signoff_status must be nil for event_type %q", store.ErrInvalidRevisionEvent, e.EventType)
	}
	if e.SignoffStatus != nil && *e.SignoffStatus != store.SignoffStatusApproved && *e.SignoffStatus != store.SignoffStatusChangesRequested {
		return fmt.Errorf("%w: signoff_status %q is not a recognized value", store.ErrInvalidRevisionEvent, *e.SignoffStatus)
	}

	for _, d := range e.EntityDeltas {
		if d.Change != store.EntityDeltaChangeCreated && d.Change != store.EntityDeltaChangeUpdated {
			return fmt.Errorf("%w: entity_deltas change %q is not created|updated", store.ErrInvalidRevisionEvent, d.Change)
		}
	}
	return nil
}
