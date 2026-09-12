package grpcauth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestFakeStorePersistThenActive proves a fresh Persist makes the grant
// active and its material readable.
func TestFakeStorePersistThenActive(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	material := TokenMaterial{RefreshToken: "rt-1", ObtainedAt: time.Now()}

	if err := store.Persist(ctx, "alice", "grant-1", material); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	status, err := store.Status(ctx, "alice", "grant-1")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status = %q, want %q", status, GrantStatusActive)
	}

	got, err := store.TokenMaterial(ctx, "alice", "grant-1")
	if err != nil {
		t.Fatalf("TokenMaterial: %v", err)
	}
	if got.RefreshToken != material.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", got.RefreshToken, material.RefreshToken)
	}
}

// TestFakeStoreRevoke proves Revoke moves Status to revoked and TokenMaterial
// then refuses to hand back material at all.
func TestFakeStoreRevoke(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	material := TokenMaterial{RefreshToken: "rt-2", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "bob", "grant-2", material); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	if err := store.Revoke(ctx, "bob", "grant-2"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	status, err := store.Status(ctx, "bob", "grant-2")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != GrantStatusRevoked {
		t.Fatalf("Status = %q, want %q", status, GrantStatusRevoked)
	}

	if _, err := store.TokenMaterial(ctx, "bob", "grant-2"); !errors.Is(err, ErrGrantRevoked) {
		t.Fatalf("TokenMaterial error = %v, want ErrGrantRevoked", err)
	}
}

// TestFakeStoreMarkNeedsReauth proves MarkNeedsReauth makes TokenMaterial
// return ErrGrantNeedsReauth without touching stored material.
func TestFakeStoreMarkNeedsReauth(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	material := TokenMaterial{RefreshToken: "rt-3", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "carol", "grant-3", material); err != nil {
		t.Fatalf("Persist: %v", err)
	}

	if err := store.MarkNeedsReauth(ctx, "carol", "grant-3"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}

	if _, err := store.TokenMaterial(ctx, "carol", "grant-3"); !errors.Is(err, ErrGrantNeedsReauth) {
		t.Fatalf("TokenMaterial error = %v, want ErrGrantNeedsReauth", err)
	}
}

// TestFakeStorePersistAfterNeedsReauthClearsState proves FR11 re-consent
// semantics: a fresh Persist after needs_reauth clears the grant back to
// active, and TokenMaterial returns the new material rather than erroring.
func TestFakeStorePersistAfterNeedsReauthClearsState(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	original := TokenMaterial{RefreshToken: "rt-4-old", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "dave", "grant-4", original); err != nil {
		t.Fatalf("Persist: %v", err)
	}
	if err := store.MarkNeedsReauth(ctx, "dave", "grant-4"); err != nil {
		t.Fatalf("MarkNeedsReauth: %v", err)
	}

	reconsented := TokenMaterial{RefreshToken: "rt-4-new", ObtainedAt: time.Now()}
	if err := store.Persist(ctx, "dave", "grant-4", reconsented); err != nil {
		t.Fatalf("Persist (re-consent): %v", err)
	}

	status, err := store.Status(ctx, "dave", "grant-4")
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if status != GrantStatusActive {
		t.Fatalf("Status after re-consent = %q, want %q", status, GrantStatusActive)
	}

	got, err := store.TokenMaterial(ctx, "dave", "grant-4")
	if err != nil {
		t.Fatalf("TokenMaterial after re-consent: %v", err)
	}
	if got.RefreshToken != reconsented.RefreshToken {
		t.Fatalf("RefreshToken = %q, want %q", got.RefreshToken, reconsented.RefreshToken)
	}
}

// TestFakeStoreUnknownKeyNotFound proves every method reports ErrGrantNotFound
// for a (subject, grant) key that was never persisted.
func TestFakeStoreUnknownKeyNotFound(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()

	if _, err := store.Status(ctx, "nobody", "nothing"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Status error = %v, want ErrGrantNotFound", err)
	}
	if _, err := store.TokenMaterial(ctx, "nobody", "nothing"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("TokenMaterial error = %v, want ErrGrantNotFound", err)
	}
	if err := store.MarkNeedsReauth(ctx, "nobody", "nothing"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("MarkNeedsReauth error = %v, want ErrGrantNotFound", err)
	}
	if err := store.Revoke(ctx, "nobody", "nothing"); !errors.Is(err, ErrGrantNotFound) {
		t.Fatalf("Revoke error = %v, want ErrGrantNotFound", err)
	}
}

// TestFakeStoreCallCounters proves the per-method call counters actually
// count, so later tasks can assert "zero Keycloak calls"-style behaviour
// indirectly (e.g. Status was read without TokenMaterial ever being called).
func TestFakeStoreCallCounters(t *testing.T) {
	ctx := context.Background()
	store := NewFakeStore()
	material := TokenMaterial{RefreshToken: "rt-5", ObtainedAt: time.Now()}

	_ = store.Persist(ctx, "erin", "grant-5", material)
	_, _ = store.Status(ctx, "erin", "grant-5")
	_, _ = store.Status(ctx, "erin", "grant-5")

	calls := store.Calls()
	if calls.Persist != 1 {
		t.Errorf("Persist calls = %d, want 1", calls.Persist)
	}
	if calls.Status != 2 {
		t.Errorf("Status calls = %d, want 2", calls.Status)
	}
	if calls.TokenMaterial != 0 {
		t.Errorf("TokenMaterial calls = %d, want 0 (never called)", calls.TokenMaterial)
	}
}

// TestFakeStoreConformance is a compile-time-ish smoke test that FakeStore
// really does satisfy Store end to end through the interface type, not just
// via the var _ Store assertion in fakestore.go.
func TestFakeStoreConformance(t *testing.T) {
	var store Store = NewFakeStore()
	ctx := context.Background()

	if err := store.Persist(ctx, "frank", "grant-6", TokenMaterial{RefreshToken: "rt-6"}); err != nil {
		t.Fatalf("Persist via Store interface: %v", err)
	}
	if _, err := store.TokenMaterial(ctx, "frank", "grant-6"); err != nil {
		t.Fatalf("TokenMaterial via Store interface: %v", err)
	}
}
