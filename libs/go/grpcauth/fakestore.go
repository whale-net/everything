package grpcauth

import (
	"context"
	"errors"
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
	// TODO(implementation): store material, reset status to active.
	return errors.New("grpcauth: FakeStore.Persist not implemented")
}

// TokenMaterial implements Store.
func (f *FakeStore) TokenMaterial(ctx context.Context, subject, grant string) (TokenMaterial, error) {
	// TODO(implementation): status check first, then return material.
	return TokenMaterial{}, errors.New("grpcauth: FakeStore.TokenMaterial not implemented")
}

// Status implements Store.
func (f *FakeStore) Status(ctx context.Context, subject, grant string) (GrantStatus, error) {
	// TODO(implementation): plain read, ErrGrantNotFound for unknown keys.
	return "", errors.New("grpcauth: FakeStore.Status not implemented")
}

// MarkNeedsReauth implements Store.
func (f *FakeStore) MarkNeedsReauth(ctx context.Context, subject, grant string) error {
	// TODO(implementation): unconditional transition to needs_reauth.
	return errors.New("grpcauth: FakeStore.MarkNeedsReauth not implemented")
}

// Revoke implements Store.
func (f *FakeStore) Revoke(ctx context.Context, subject, grant string) error {
	// TODO(implementation): unconditional transition to revoked.
	return errors.New("grpcauth: FakeStore.Revoke not implemented")
}
