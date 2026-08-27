// Package placement's unit test coverage for RefuseIfBackdated -- the
// caller-facing half of FR19's no-back-dating guard. No database required:
// see move_integration_test.go for Writer.Move's close-and-open write path
// and the database-side guard (NFR6.2).
package placement

import (
	"testing"
	"time"

	"github.com/whale-net/everything/leaflab/api/contract"
)

// TestRefuseIfBackdated_RefusesPastBoundary proves a requestedAt earlier
// than now is refused via contract.Refuse (FR59.3's refuse-and-name-the-
// alternative contract), distinguishable by class from an ordinary
// invalid_argument failure.
func TestRefuseIfBackdated_RefusesPastBoundary(t *testing.T) {
	past := time.Now().Add(-1 * time.Hour)

	err := RefuseIfBackdated(past)
	if err == nil {
		t.Fatal("RefuseIfBackdated(past) = nil, want a refusal error")
	}

	detail, ok := contract.FromError(err)
	if !ok {
		t.Fatal("RefuseIfBackdated(past) error carries no structured Failure detail")
	}
	if detail.Class != "refused_with_alternative" {
		t.Errorf("Class = %q, want %q", detail.Class, "refused_with_alternative")
	}
	if detail.Alternative == "" {
		t.Error("Alternative is empty, want the named alternative path (retry without a past boundary)")
	}
	if detail.Entity != "plant_region_history" {
		t.Errorf("Entity = %q, want %q", detail.Entity, "plant_region_history")
	}
}

// TestRefuseIfBackdated_AcceptsNowOrFuture proves a requestedAt at or after
// now is accepted -- the guard only rejects a boundary strictly in the past.
func TestRefuseIfBackdated_AcceptsNowOrFuture(t *testing.T) {
	future := time.Now().Add(1 * time.Hour)
	if err := RefuseIfBackdated(future); err != nil {
		t.Errorf("RefuseIfBackdated(future) = %v, want nil", err)
	}

	// A requestedAt computed "just now" but evaluated a moment later must
	// still be accepted -- the guard compares against time.Now() at
	// evaluation time, so anything not already in the past when the check
	// runs is fine.
	justAhead := time.Now().Add(1 * time.Millisecond)
	if err := RefuseIfBackdated(justAhead); err != nil {
		t.Errorf("RefuseIfBackdated(justAhead) = %v, want nil", err)
	}
}
