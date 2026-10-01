// Pure unit tests for the observed-state guard's shared decision function
// (task_observed_state.go) -- no database needed, since the comparison
// depends only on the ids it is handed. The four verbs' real-Postgres
// coverage of the same guard (each verb's own
// *_integration_test.go) is deliberately outside `bazel test //krill/store:all`,
// so these are what keep the guard's own rules -- nil means unguarded, a
// supplied id on a task holding none is the mismatch rather than a legality
// refusal, and the refusal wraps ErrObservedStateMismatch and nothing else --
// asserted on every ordinary run.
//
// `package store`, not `package store_test`: the helpers are unexported.
package store

import (
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCheckObservedClaim_NilExpected_Unguarded(t *testing.T) {
	taskID, current := uuid.New(), uuid.New()

	assert.NoError(t, checkObservedClaim(taskID, &current, nil),
		"an omitted expected id leaves the call unguarded, whatever the task holds")
	assert.NoError(t, checkObservedClaim(taskID, nil, nil),
		"an omitted expected id is unguarded on an unclaimed task too")
}

func TestCheckObservedClaim_Matching_NoError(t *testing.T) {
	taskID, current := uuid.New(), uuid.New()

	assert.NoError(t, checkObservedClaim(taskID, &current, &current),
		"the claim the caller observed is still current, so the call proceeds")
}

func TestCheckObservedClaim_Mismatched(t *testing.T) {
	taskID := uuid.New()
	current, expected := uuid.New(), uuid.New()

	err := checkObservedClaim(taskID, &current, &expected)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObservedStateMismatch)
	assert.Contains(t, err.Error(), expected.String(), "the refusal must name the id the caller expected")
	assert.Contains(t, err.Error(), current.String(), "the refusal must name the id that is current instead")
}

func TestCheckObservedClaim_NoCurrentClaim_IsMismatchNotLegality(t *testing.T) {
	taskID, expected := uuid.New(), uuid.New()

	err := checkObservedClaim(taskID, nil, &expected)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObservedStateMismatch,
		"a supplied expected id against a task holding no claim is the mismatch, so Release's mapping never overlaps ErrTaskNotClaimed")
	assert.NotErrorIs(t, err, ErrTaskNotClaimed)
	assert.Contains(t, err.Error(), "none",
		"an absent current id must read as 'none' rather than as a zero uuid a caller could mistake for an id")
}

func TestCheckObservedEscalation_NilExpected_Unguarded(t *testing.T) {
	taskID, current := uuid.New(), uuid.New()

	assert.NoError(t, checkObservedEscalation(taskID, &current, nil))
	assert.NoError(t, checkObservedEscalation(taskID, nil, nil))
}

func TestCheckObservedEscalation_Matching_NoError(t *testing.T) {
	taskID, current := uuid.New(), uuid.New()

	assert.NoError(t, checkObservedEscalation(taskID, &current, &current))
}

func TestCheckObservedEscalation_MismatchedOrNone(t *testing.T) {
	taskID := uuid.New()
	current, expected := uuid.New(), uuid.New()

	err := checkObservedEscalation(taskID, &current, &expected)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObservedStateMismatch)
	assert.NotErrorIs(t, err, ErrTaskNotEscalated)

	err = checkObservedEscalation(taskID, nil, &expected)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrObservedStateMismatch)
	assert.NotErrorIs(t, err, ErrTaskNotEscalated)
}

// TestErrObservedStateMismatch_DistinctFromEveryLegalityRefusal is the
// named error's whole reason for existing: a client has to be able to tell
// "the state changed since you looked" from "this action is not legal", so
// the guard's refusal must match none of the legality refusals -- and those
// must not match it either.
func TestErrObservedStateMismatch_DistinctFromEveryLegalityRefusal(t *testing.T) {
	for _, legality := range []error{
		ErrTaskNotClaimed,
		ErrTaskNotEscalated,
		ErrTaskCancelled,
		ErrTaskAlreadyCancelled,
		ErrTaskEscalated,
	} {
		t.Run(legality.Error(), func(t *testing.T) {
			assert.NotErrorIs(t, ErrObservedStateMismatch, legality)
			assert.NotErrorIs(t, legality, ErrObservedStateMismatch)
		})
	}
	assert.NotErrorIs(t, errors.New("some other failure"), ErrObservedStateMismatch,
		"an unrelated error must never satisfy errors.Is against the guard's refusal")
}