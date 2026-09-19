// fakeAbandonStore backs abandon_test.go: AbandonHandler (abandon.go,
// issue #2688) depends only on store.AbandonStore, so handler-level tests
// never need a real Postgres (that is
// krill/store/abandon_integration_test.go's job).
package handlers_test

import (
	"context"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/store"
)

// fakeAbandonStore records the one Abandon call it sees (so a test can
// assert the write gate reached, or never reached, the store) and can be
// told to return a fixed result or fail with a fixed error.
type fakeAbandonStore struct {
	result store.AbandonResult
	err    error

	called        bool
	gotScopeID    uuid.UUID
	gotContainer  uuid.UUID
	gotNote       *string
	gotActing     store.Subject
	gotOnBehalfOf store.Subject
}

func (f *fakeAbandonStore) Abandon(ctx context.Context, scopeID, containerID uuid.UUID, note *string, acting, onBehalfOf store.Subject) (store.AbandonResult, error) {
	f.called = true
	f.gotScopeID = scopeID
	f.gotContainer = containerID
	f.gotNote = note
	f.gotActing, f.gotOnBehalfOf = acting, onBehalfOf
	return f.result, f.err
}

var _ store.AbandonStore = (*fakeAbandonStore)(nil)
