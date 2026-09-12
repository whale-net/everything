// fakeEntitySetQuerier is an in-memory stand-in for SessionSlice's third
// dependency (session_slice.go, issue #2544): session_slice_test.go drives
// SessionSlice.handle against this instead of a real *slice.Querier.
// fakeDesignSessionStore and fakeRevisionEventStore (session_slice_test.go's
// other two dependencies) are already declared in
// fake_design_session_store_test.go (issue #2543) and reused here as-is;
// fakeRevisionEventStore's seed method (added there for this test) seeds
// events directly, bypassing Append's validation, mirroring
// fakeDesignSessionStore's own put precedent.
package handlers_test

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/whale-net/everything/krill/slice"
)

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
