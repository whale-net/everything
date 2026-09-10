package grpcauth

import (
	"context"
	"sync"
)

// FakeStore is an in-memory Store for tests. It is exported (and lives in the
// library, not a _test.go file) so later credential-source tasks — and the
// Postgres implementation's conformance tests — can exercise the delegated
// grant flow without a database.
//
// It honours the same status-first semantics as a real Store: TokenMaterial
// consults the persisted GrantStatus before touching material, so a revoked or
// needs_reauth grant never yields a token. It also counts calls per method so
// tests can assert things like "no token was ever read" indirectly.
//
// All methods are safe for concurrent use.
type FakeStore struct {
	mu      sync.Mutex
	grants  map[fakeGrantKey]*fakeGrant
	calls   FakeStoreCalls
	failErr error
}

// compile-time conformance assertion
var _ Store = (*FakeStore)(nil)

// FakeStoreCalls counts how many times each Store method was invoked.
type FakeStoreCalls struct {
	Persist         int
	TokenMaterial   int
	Status          int
	MarkNeedsReauth int
	Revoke          int
}

type fakeGrantKey struct {
	subject string
	grant   string
}

type fakeGrant struct {
	status   GrantStatus
	material TokenMaterial
}

// NewFakeStore returns an empty in-memory Store.
func NewFakeStore() *FakeStore {
	return &FakeStore{grants: make(map[fakeGrantKey]*fakeGrant)}
}

// Calls returns a snapshot of the per-method call counters.
func (f *FakeStore) Calls() FakeStoreCalls {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// SetFailure makes every subsequent method return err, letting tests drive the
// transient-failure branch (pass NewTransientError(...)). Pass nil to clear.
func (f *FakeStore) SetFailure(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failErr = err
}

// Persist implements Store.
func (f *FakeStore) Persist(ctx context.Context, subject, grant string, material TokenMaterial) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.Persist++
	if f.failErr != nil {
		return f.failErr
	}
	key := fakeGrantKey{subject: subject, grant: grant}
	f.grants[key] = &fakeGrant{status: GrantStatusActive, material: material}
	return nil
}

// TokenMaterial implements Store.
func (f *FakeStore) TokenMaterial(ctx context.Context, subject, grant string) (TokenMaterial, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.TokenMaterial++
	if f.failErr != nil {
		return TokenMaterial{}, f.failErr
	}
	g, ok := f.grants[fakeGrantKey{subject: subject, grant: grant}]
	if !ok {
		return TokenMaterial{}, ErrGrantNotFound
	}
	switch g.status {
	case GrantStatusRevoked:
		return TokenMaterial{}, ErrGrantRevoked
	case GrantStatusNeedsReauth:
		return TokenMaterial{}, ErrGrantNeedsReauth
	}
	return g.material, nil
}

// Status implements Store.
func (f *FakeStore) Status(ctx context.Context, subject, grant string) (GrantStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.Status++
	if f.failErr != nil {
		return "", f.failErr
	}
	g, ok := f.grants[fakeGrantKey{subject: subject, grant: grant}]
	if !ok {
		return "", ErrGrantNotFound
	}
	return g.status, nil
}

// MarkNeedsReauth implements Store.
func (f *FakeStore) MarkNeedsReauth(ctx context.Context, subject, grant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.MarkNeedsReauth++
	if f.failErr != nil {
		return f.failErr
	}
	key := fakeGrantKey{subject: subject, grant: grant}
	g, ok := f.grants[key]
	if !ok {
		return ErrGrantNotFound
	}
	g.status = GrantStatusNeedsReauth
	return nil
}

// Revoke implements Store.
func (f *FakeStore) Revoke(ctx context.Context, subject, grant string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls.Revoke++
	if f.failErr != nil {
		return f.failErr
	}
	key := fakeGrantKey{subject: subject, grant: grant}
	g, ok := f.grants[key]
	if !ok {
		return ErrGrantNotFound
	}
	g.status = GrantStatusRevoked
	return nil
}
