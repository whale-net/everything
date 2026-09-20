// Pure unit tests for store.NextLane (task_complete.go, issue #2725's
// Testing section, FR8) -- every routing branch, with no Postgres and no
// TaskStore involved at all: NextLane reads only the sequence/current
// arguments it is given, never a global lane order.
package store_test

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

// fullLaneSequence is the canonical five-lane sequence -- the same one
// createTestTask (task_dependency_integration_test.go) builds every task
// with by default.
var fullLaneSequence = []store.Lane{
	store.LaneScaffold, store.LaneImplementation, store.LaneTesting, store.LaneValidation, store.LaneDone,
}

// TestNextLane_FullSequence_PassAdvancesOneLane is Testing section item 1's
// first half: pass from each lane in a full five-lane sequence lands on
// the next one.
func TestNextLane_FullSequence_PassAdvancesOneLane(t *testing.T) {
	cases := []struct {
		current store.Lane
		want    store.Lane
	}{
		{store.LaneScaffold, store.LaneImplementation},
		{store.LaneImplementation, store.LaneTesting},
		{store.LaneTesting, store.LaneValidation},
	}
	for _, c := range cases {
		t.Run(string(c.current), func(t *testing.T) {
			got := store.NextLane(fullLaneSequence, c.current, store.VerdictPass)
			assert.Equal(t, c.want, got)
		})
	}
}

// TestNextLane_FullSequence_PassFromValidationLandsOnDone is Testing
// section item 1's second half: pass from Validation (the lane before
// Done in the full sequence) lands on Done.
func TestNextLane_FullSequence_PassFromValidationLandsOnDone(t *testing.T) {
	got := store.NextLane(fullLaneSequence, store.LaneValidation, store.VerdictPass)
	assert.Equal(t, store.LaneDone, got)
}

// TestNextLane_TruncatedSequence_PassFromLastElementLandsOnDone is Testing
// section item 2: pass from the last element of a truncated sequence
// (e.g. [Implementation, Testing] at Testing) lands on Done -- FR8's "or
// to Done if its current lane is the last one", even when that last
// element is not itself Validation.
func TestNextLane_TruncatedSequence_PassFromLastElementLandsOnDone(t *testing.T) {
	sequence := []store.Lane{store.LaneImplementation, store.LaneTesting}
	got := store.NextLane(sequence, store.LaneTesting, store.VerdictPass)
	assert.Equal(t, store.LaneDone, got)
}

// TestNextLane_SkippingSequence_PassAdvancesToNextMember is Testing
// section item 3's first half: a skipping sequence ([Testing, Validation,
// Done]) advances pass from Testing to Validation -- the next MEMBER of
// this task's own sequence, never the canonically adjacent lane
// (Implementation, which this sequence skips entirely).
func TestNextLane_SkippingSequence_PassAdvancesToNextMember(t *testing.T) {
	sequence := []store.Lane{store.LaneTesting, store.LaneValidation, store.LaneDone}
	got := store.NextLane(sequence, store.LaneTesting, store.VerdictPass)
	assert.Equal(t, store.LaneValidation, got)
}

// TestNextLane_SkippingSequence_FailFromStartingLaneStaysPut is Testing
// section item 3's second half: fail from Testing -- this sequence's own
// starting lane, with no preceding member -- stays at Testing rather than
// reverting past the start of this task's own sequence.
func TestNextLane_SkippingSequence_FailFromStartingLaneStaysPut(t *testing.T) {
	sequence := []store.Lane{store.LaneTesting, store.LaneValidation, store.LaneDone}
	got := store.NextLane(sequence, store.LaneTesting, store.VerdictFail)
	assert.Equal(t, store.LaneTesting, got)
}

// TestNextLane_FailFromMiddleLane_RevertsExactlyOneLaneInOwnSequence is
// Testing section item 4: fail from a middle lane goes back exactly one
// lane in THIS task's own sequence, not the canonical global order --
// proven with a sequence whose own predecessor of Validation is Scaffold
// (Implementation and Testing both skipped), which the canonical order
// (.../Testing/Validation/...) would never produce.
func TestNextLane_FailFromMiddleLane_RevertsExactlyOneLaneInOwnSequence(t *testing.T) {
	sequence := []store.Lane{store.LaneScaffold, store.LaneValidation, store.LaneDone}
	got := store.NextLane(sequence, store.LaneValidation, store.VerdictFail)
	assert.Equal(t, store.LaneScaffold, got, "fail must revert to this sequence's own preceding member, not the canonically adjacent lane")
}

// TestNextLane_FullSequence_FailFromScaffoldStaysPut proves the full
// sequence's own starting-lane fail case: no preceding lane exists, so the
// task stays in Scaffold.
func TestNextLane_FullSequence_FailFromScaffoldStaysPut(t *testing.T) {
	got := store.NextLane(fullLaneSequence, store.LaneScaffold, store.VerdictFail)
	assert.Equal(t, store.LaneScaffold, got)
}

// TestCompleteTask_UnknownVerdict_Rejected proves CompleteTask validates
// Verdict before ever touching the database (the same "pure validation
// first" shape CreateTask's own validateLaneSequence establishes) --
// store.New(nil) is safe here because the rejection returns before the
// pool is ever used (krill/store/store.go's New doc comment).
func TestCompleteTask_UnknownVerdict_Rejected(t *testing.T) {
	_, err := store.New(nil).Tasks().CompleteTask(context.Background(), store.CompleteTaskParams{
		Verdict: store.Verdict("maybe"),
	})
	assert.ErrorIs(t, err, store.ErrUnknownVerdict)
}
