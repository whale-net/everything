package link

import (
	"context"
	"testing"
)

// TestNewVerifier_Scaffold and TestVerify_Scaffold document today's
// scaffold-stub contract (issue #2598): both return a non-nil "not
// implemented" error rather than a zero value silently succeeding, so a
// stub accidentally left returning (nil, nil) fails loudly. The
// Implementation phase of #2598 replaces both bodies, and its Testing
// phase replaces this file with the full red/green suite the issue
// describes (happy path, signature tampering, kid rotation, ordering,
// etc.) -- this file exists only to give the Scaffold phase's `go_test`
// target something real to assert against.
func TestNewVerifier_Scaffold(t *testing.T) {
	_, err := NewVerifier(context.Background(), "https://ui.example.test/.well-known/jwks.json", "https://ui.example.test")
	if err == nil {
		t.Fatal("NewVerifier: got nil error from scaffold stub, want non-nil")
	}
}

func TestVerify_Scaffold(t *testing.T) {
	v := &Verifier{}
	_, err := v.Verify(context.Background(), "not-a-real-token")
	if err == nil {
		t.Fatal("Verify: got nil error from scaffold stub, want non-nil")
	}
}
