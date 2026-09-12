// fakeMediatedWriteStore is an in-memory store.MediatedWriteStore backing
// mediated_test.go (issue #2546's Testing section): ProposeEntitiesHandler
// depends only on this interface, so handler-level tests never need a real
// Postgres (that is krill/store/mediated_integration_test.go's job). It
// replicates just enough of the real store's validation (empty proposal
// list, acting == on-behalf-of, an unknown parent) to drive
// writeMediatedProposalError's mapping without a database -- same
// deliberate small duplicate fake_design_session_store_test.go's
// fakeRevisionEventStore already does for RevisionEventStore.
package handlers_test

import (
	"context"
	"fmt"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeMediatedWriteStore backs mediated_test.go.
type fakeMediatedWriteStore struct {
	// proposeErr, if set, short-circuits ProposeEntities with this error --
	// used to simulate an unknown design_session id (store.ErrNotFound)
	// without needing this fake to track a design_session store.
	proposeErr error

	// gotProposal records the last ProposeEntities call's argument in full,
	// so a test can assert scope_id/identity/proposals were passed through
	// from the gating session and request body unchanged.
	gotProposal store.MediatedProposal

	mu    sync.Mutex
	calls int
}

func newFakeMediatedWriteStore() *fakeMediatedWriteStore {
	return &fakeMediatedWriteStore{}
}

func (f *fakeMediatedWriteStore) ProposeEntities(ctx context.Context, in store.MediatedProposal) (store.RevisionEvent, []store.ProposedEntity, error) {
	f.gotProposal = in

	if f.proposeErr != nil {
		return store.RevisionEvent{}, nil, f.proposeErr
	}
	if len(in.Proposals) == 0 {
		return store.RevisionEvent{}, nil, store.ErrEmptyMediatedProposal
	}
	if in.Acting.Iss == in.OnBehalfOf.Iss && in.Acting.Sub == in.OnBehalfOf.Sub && in.Acting.Kind == in.OnBehalfOf.Kind {
		return store.RevisionEvent{}, nil, store.ErrMediatedIdentitySame
	}
	if in.EventType != store.EventTypeDraft {
		return store.RevisionEvent{}, nil, store.ErrInvalidRevisionEvent
	}
	if in.VerifiedAgainst == nil || *in.VerifiedAgainst == "" {
		return store.RevisionEvent{}, nil, store.ErrInvalidRevisionEvent
	}

	entities := make([]store.ProposedEntity, len(in.Proposals))
	deltas := make([]store.EntityDelta, len(in.Proposals))
	for i, p := range in.Proposals {
		if p.ParentID != nil && *p.ParentID == unknownParentID {
			return store.RevisionEvent{}, nil, store.ErrNotFound
		}
		id := uuid.New()
		entities[i] = store.ProposedEntity{Kind: p.Kind, ID: id}
		deltas[i] = store.EntityDelta{EntityID: id, Change: store.EntityDeltaChangeCreated, SummaryLine: p.SummaryLine}
	}

	f.mu.Lock()
	f.calls++
	seqNo := f.calls
	f.mu.Unlock()

	ev := store.RevisionEvent{
		ID:              uuid.New(),
		ScopeID:         in.ScopeID,
		SessionID:       in.SessionID,
		SeqNo:           seqNo,
		Acting:          in.Acting,
		OnBehalfOf:      in.OnBehalfOf,
		EventType:       in.EventType,
		EntityDeltas:    deltas,
		VerifiedAgainst: in.VerifiedAgainst,
	}
	return ev, entities, nil
}

// unknownParentID is a sentinel a test can use as a proposal's ParentID to
// force fakeMediatedWriteStore.ProposeEntities to return store.ErrNotFound,
// mirroring the real store's currentRowExists miss.
var unknownParentID = uuid.New()

// fakeErrDesignSessionNotFound mirrors krill/store's own
// errParentNotFound("design_session", id) message shape (errors.go) --
// writeMediatedProposalError (mediated.go) string-matches on "design_session"
// in the error text to distinguish an unknown design session (404) from an
// unknown/cross-scope proposal parent (400), so this fake must produce the
// same wrapped shape the real store's appendRevisionEventTx does, not a bare
// store.ErrNotFound.
func fakeErrDesignSessionNotFound(id uuid.UUID) error {
	return fmt.Errorf("%w: no current design_session row for id %s", store.ErrNotFound, id)
}
