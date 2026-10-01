// Pure-Go coverage of design_session_summary.go's Stage vocabulary and
// deriveStage derivation (FR d0a63ffb-8e80-47c1-8a64-2729d4950522).
// Deliberately `package store`, not `package store_test` like every sibling
// unit test in this package: deriveStage is unexported and the Stage
// constants' rule IS a store-internal derivation -- the same reason
// task_escalation_integration_test.go is same-package.
//
// Two things this file proves that
// krill/store/design_session_integration_test.go cannot:
//
//   - The vocabulary itself. Stage is UI-facing and named as stable for
//     later phases, so each constant's exact string is pinned here. An
//     integration test comparing a derived Stage against another constant
//     passes even if every constant is renamed at once; only this file
//     fails on a rename.
//   - deriveStage's default branch. migration 008's DDL CHECK confines
//     event_type to five values, so no revision_event row can reach the
//     `default:` arm through SQL -- an integration test could never drive
//     it. Here it is driven directly, so a later widening of that CHECK
//     (or of the Go enum) that reaches the fallback is a known-opened,
//     already-asserted-on branch rather than an untested one.
//
// No database needed -- part of `bazel test //...`.
package store

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// strp is a helper for spelling the *string arguments deriveStage takes:
// nil for "this session has never received the corresponding kind of
// event", a pointer for the value it did.
func strp(s string) *string { return &s }

// TestStageConstantValues pins the wire vocabulary. These strings reach
// the HTTP response body and the MCP structured content verbatim, so a
// rename is a breaking change for every UI badge and every agent reading
// the value -- which is exactly why the FR asks for the vocabulary to be
// named in one place and kept stable.
func TestStageConstantValues(t *testing.T) {
	assert.Equal(t, Stage("opened"), StageOpened)
	assert.Equal(t, Stage("approved"), StageApproved)
	assert.Equal(t, Stage("changes_requested"), StageChangesRequested)
	assert.Equal(t, Stage("architect_review"), StageArchitectReview)
	assert.Equal(t, Stage("in_draft"), StageInDraft)
	assert.Equal(t, Stage("answered"), StageAnswered)
	assert.Equal(t, Stage("ruled"), StageRuled)
}

// TestStageConstants_AreDistinct guards the "one place" promise from the
// other direction: no two values may collide, or the derivation could not
// be inverted by a reader.
func TestStageConstants_AreDistinct(t *testing.T) {
	seen := map[Stage]bool{}
	for _, s := range []Stage{
		StageOpened, StageApproved, StageChangesRequested,
		StageArchitectReview, StageInDraft, StageAnswered, StageRuled,
	} {
		assert.False(t, seen[s], "duplicate Stage value %q", s)
		seen[s] = true
	}
	assert.Len(t, seen, 7)
}

// TestDeriveStage is the whole derivation as one table: every event type
// the FR enumerates, the terminal approved rule against a later event of
// every other type, the non-approval signoff, the no-events session, and
// the default arm.
func TestDeriveStage(t *testing.T) {
	cases := []struct {
		name          string
		latestEvent   *string
		latestSignoff *string
		want          Stage
	}{
		// A session with no revision events at all.
		{"no events at all is opened", nil, nil, StageOpened},

		// The five DDL-legal event types, each deciding on its own.
		{"draft is in draft", strp(string(EventTypeDraft)), nil, StageInDraft},
		{"reconciliation is architect review", strp(string(EventTypeReconciliation)), nil, StageArchitectReview},
		{"answer is answered", strp(string(EventTypeAnswer)), nil, StageAnswered},
		{"ruling is ruled", strp(string(EventTypeRuling)), nil, StageRuled},
		// A signoff that is the latest event, and is not an approval.
		{"changes_requested signoff is changes requested", strp(string(EventTypeSignoff)), strp(string(SignoffStatusChangesRequested)), StageChangesRequested},

		// The terminal rule: an approval outranks a later event of every
		// type, because the follow-up form is hidden once signed off.
		{"approved beats no later event", strp(string(EventTypeSignoff)), strp(string(SignoffStatusApproved)), StageApproved},
		{"approved beats a later draft", strp(string(EventTypeDraft)), strp(string(SignoffStatusApproved)), StageApproved},
		{"approved beats a later reconciliation", strp(string(EventTypeReconciliation)), strp(string(SignoffStatusApproved)), StageApproved},
		{"approved beats a later answer", strp(string(EventTypeAnswer)), strp(string(SignoffStatusApproved)), StageApproved},
		{"approved beats a later ruling", strp(string(EventTypeRuling)), strp(string(SignoffStatusApproved)), StageApproved},
		{"approved beats a later changes_requested signoff", strp(string(EventTypeSignoff)), strp(string(SignoffStatusApproved)), StageApproved},

		// A non-approval signoff is not terminal: the latest event decides,
		// so a draft after a changes-requested signoff is back in draft.
		{"changes_requested signoff does not beat a later draft", strp(string(EventTypeDraft)), strp(string(SignoffStatusChangesRequested)), StageInDraft},
		{"changes_requested signoff does not beat a later ruling", strp(string(EventTypeRuling)), strp(string(SignoffStatusChangesRequested)), StageRuled},

		// A signoff with no event row behind it is still terminal.
		{"approved with no latest event is approved", nil, strp(string(SignoffStatusApproved)), StageApproved},

		// The fallback arm. Unreachable through SQL today (008's CHECK
		// confines event_type to five values); reachable if a future event
		// type is added without a Stage case, where "opened" -- not a
		// silent zero Stage -- is the safe answer.
		{"an unrecognized event type falls back to opened", strp("some_future_event_type"), nil, StageOpened},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, deriveStage(tc.latestEvent, tc.latestSignoff))
		})
	}
}
