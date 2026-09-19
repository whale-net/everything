// fakeRecutStore/fakeBacklogQuerier back recut_test.go: MoveScopeHandler/
// GetBacklogHandler (recut.go, issue #2687) depend only on
// store.RecutStore and the package's own unexported backlogQuerier
// interface, so handler-level tests never need a real Postgres or
// //krill/slice (that is krill/store/recut_integration_test.go's and
// krill/slice's own integration test's job).
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
	"github.com/whale-net/everything/krill/store"
)

// fakeRecutStore records the one MoveScope call it sees (so a test can
// assert the write gate reached, or never reached, the store) and can be
// told to fail with a fixed error. GetOrCreateBacklog/ListBacklog are
// never called by this package's handlers directly (GetBacklogHandler
// depends on backlogQuerier instead), so they are unused stubs here.
type fakeRecutStore struct {
	moveScopeErr error

	moveScopeCalled bool
	gotScopeID      uuid.UUID
	gotEntityIDs    []uuid.UUID
	gotFrom         uuid.UUID
	gotTo           uuid.UUID
	gotActing       store.Subject
	gotOnBehalfOf   store.Subject
}

func (f *fakeRecutStore) GetOrCreateBacklog(ctx context.Context, scopeID, productID uuid.UUID, acting, onBehalfOf store.Subject) (store.MilestoneRef, error) {
	return store.MilestoneRef{}, nil
}

func (f *fakeRecutStore) ListBacklog(ctx context.Context, scopeID, productID uuid.UUID) ([]uuid.UUID, error) {
	return nil, nil
}

func (f *fakeRecutStore) MoveScope(ctx context.Context, scopeID uuid.UUID, entityIDs []uuid.UUID, fromContainerID, toContainerID uuid.UUID, acting, onBehalfOf store.Subject) error {
	f.moveScopeCalled = true
	f.gotScopeID = scopeID
	f.gotEntityIDs = entityIDs
	f.gotFrom, f.gotTo = fromContainerID, toContainerID
	f.gotActing, f.gotOnBehalfOf = acting, onBehalfOf
	return f.moveScopeErr
}

var _ store.RecutStore = (*fakeRecutStore)(nil)

// fakeBacklogQuerier stands in for //krill/slice's GetBacklog
// (backlogQuerier, recut.go's unexported narrowing of *slice.Querier), so
// TestGetBacklogHandler_* never needs a real store/querier pair.
type fakeBacklogQuerier struct {
	doc slice.Document
	err error
}

func (f *fakeBacklogQuerier) GetBacklog(ctx context.Context, productID uuid.UUID) (slice.Document, error) {
	return f.doc, f.err
}
