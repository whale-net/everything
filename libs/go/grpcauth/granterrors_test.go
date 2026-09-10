package grpcauth

import (
	"errors"
	"fmt"
	"testing"
)

// TestSentinelWrappedMatch proves the standard wrapping idiom works for our
// sentinels: errors.Is sees through a %w wrap (FR8/FR9 mechanical-detection
// requirement).
func TestSentinelWrappedMatch(t *testing.T) {
	wrapped := fmt.Errorf("wrap: %w", ErrGrantRevoked)
	if !errors.Is(wrapped, ErrGrantRevoked) {
		t.Fatal("errors.Is did not see through %w wrap of ErrGrantRevoked")
	}
}

// TestSentinelsPairwiseDistinct is the NFR4 primitive: ErrGrantRevoked,
// ErrGrantNeedsReauth, ErrGrantNotFound, and a TransientError must be
// pairwise non-matching under errors.Is. In particular a transient failure
// must never look like a needs_reauth/revoked grant state, and vice versa.
func TestSentinelsPairwiseDistinct(t *testing.T) {
	transient := NewTransientError("op", errors.New("network blip"))

	cases := []struct {
		name string
		err  error
	}{
		{"revoked", ErrGrantRevoked},
		{"needs_reauth", ErrGrantNeedsReauth},
		{"not_found", ErrGrantNotFound},
		{"transient", transient},
	}

	targets := []struct {
		name   string
		target error
	}{
		{"revoked", ErrGrantRevoked},
		{"needs_reauth", ErrGrantNeedsReauth},
		{"not_found", ErrGrantNotFound},
		{"ErrTransient", ErrTransient},
	}

	for _, c := range cases {
		for _, tgt := range targets {
			matches := errors.Is(c.err, tgt.target)
			wantMatch := (c.name == tgt.name) || (c.name == "transient" && tgt.name == "ErrTransient")
			if matches != wantMatch {
				t.Errorf("errors.Is(%s, %s) = %v, want %v", c.name, tgt.name, matches, wantMatch)
			}
		}
	}
}

// TestTransientErrorUnwrapsCause proves TransientError exposes its wrapped
// cause via errors.Is/errors.As/Unwrap so callers can still get at the
// underlying failure when they need to (FR9).
func TestTransientErrorUnwrapsCause(t *testing.T) {
	cause := errors.New("dial tcp: connection refused")
	te := NewTransientError("TokenMaterial", cause)

	if !errors.Is(te, cause) {
		t.Fatal("errors.Is did not find the wrapped cause")
	}
	if !errors.Is(te, ErrTransient) {
		t.Fatal("TransientError does not match ErrTransient sentinel")
	}

	var target *TransientError
	if !errors.As(te, &target) {
		t.Fatal("errors.As did not retrieve the concrete *TransientError")
	}
	if target.Op != "TokenMaterial" {
		t.Fatalf("Op = %q, want %q", target.Op, "TokenMaterial")
	}
}

// TestIsTransient proves the IsTransient helper agrees with errors.Is against
// ErrTransient, for both a bare TransientError and one wrapped further.
func TestIsTransient(t *testing.T) {
	te := NewTransientError("Persist", errors.New("timeout"))

	if !IsTransient(te) {
		t.Fatal("IsTransient(TransientError) = false, want true")
	}
	if !IsTransient(fmt.Errorf("context: %w", te)) {
		t.Fatal("IsTransient(wrapped TransientError) = false, want true")
	}
	if IsTransient(ErrGrantRevoked) {
		t.Fatal("IsTransient(ErrGrantRevoked) = true, want false")
	}
	if IsTransient(ErrGrantNeedsReauth) {
		t.Fatal("IsTransient(ErrGrantNeedsReauth) = true, want false")
	}
	if IsTransient(nil) {
		t.Fatal("IsTransient(nil) = true, want false")
	}
}

// TestTransientErrorNilSafety proves the (*TransientError) methods do not
// panic on a nil receiver, matching the defensive guards in the source.
func TestTransientErrorNilSafety(t *testing.T) {
	var te *TransientError
	if got := te.Error(); got != "" {
		t.Fatalf("nil TransientError.Error() = %q, want empty string", got)
	}
	if got := te.Unwrap(); got != nil {
		t.Fatalf("nil TransientError.Unwrap() = %v, want nil", got)
	}
}
