package main

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/whale-net/everything/krill/store"
)

// TestLegalInterventions pins the one reading of the FRs' action legality
// (43e39aae, af61631d) for every state and both sides of the Done-lane
// subtraction. The order is asserted, not just membership: the FRs name the
// verbs in this order and the rows render what this returns.
func TestLegalInterventions(t *testing.T) {
	for _, tc := range []struct {
		name  string
		state taskInterventionState
		lane  store.Lane
		want  []string
	}{
		{
			name:  "claimed in a working lane offers Release, Escalate and Cancel",
			state: taskInterventionClaimed,
			lane:  store.LaneImplementation,
			want:  []string{actionRelease, actionEscalate, actionCancel},
		},
		{
			name:  "claimed in the Done lane offers only Release",
			state: taskInterventionClaimed,
			lane:  store.LaneDone,
			want:  []string{actionRelease},
		},
		{
			name:  "escalated in a working lane offers Requeue and Cancel",
			state: taskInterventionEscalated,
			lane:  store.LaneScaffold,
			want:  []string{actionRequeue, actionCancel},
		},
		{
			name:  "escalated in the Done lane offers only Requeue",
			state: taskInterventionEscalated,
			lane:  store.LaneDone,
			want:  []string{actionRequeue},
		},
		{
			name:  "ready in a working lane offers Escalate and Cancel",
			state: taskInterventionReady,
			lane:  store.LaneTesting,
			want:  []string{actionEscalate, actionCancel},
		},
		{
			name:  "ready in the Done lane offers nothing",
			state: taskInterventionReady,
			lane:  store.LaneDone,
			want:  nil,
		},
		{
			name:  "cancelled offers nothing in a working lane",
			state: taskInterventionCancelled,
			lane:  store.LaneImplementation,
			want:  nil,
		},
		{
			name:  "cancelled offers nothing in the Done lane",
			state: taskInterventionCancelled,
			lane:  store.LaneDone,
			want:  nil,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, legalInterventions(tc.state, tc.lane))
		})
	}
}

// TestLegalInterventionsNeverReleasesAnEscalatedTask is the FR's own
// prohibition: an escalated task holds no claim, so Release is never offered
// from it, in any lane -- including the Done lane, where the subtraction of
// Escalate and Cancel must not somehow leave Release behind.
func TestLegalInterventionsNeverReleasesAnEscalatedTask(t *testing.T) {
	for _, lane := range []store.Lane{
		store.LaneScaffold,
		store.LaneImplementation,
		store.LaneTesting,
		store.LaneValidation,
		store.LaneDone,
	} {
		assert.NotContains(t, legalInterventions(taskInterventionEscalated, lane), actionRelease,
			"lane %s must offer no Release on an escalated task", lane)
	}
}

// TestLegalInterventionsDoneLaneWithholdsEscalateAndCancel states the Done
// lane's rule independently of the state: whatever else a finished task
// offers, it offers neither of the two verbs the FRs withhold from it.
func TestLegalInterventionsDoneLaneWithholdsEscalateAndCancel(t *testing.T) {
	for _, state := range []taskInterventionState{
		taskInterventionReady,
		taskInterventionClaimed,
		taskInterventionEscalated,
		taskInterventionCancelled,
	} {
		got := legalInterventions(state, store.LaneDone)
		assert.NotContains(t, got, actionEscalate, "state %s in the Done lane", state)
		assert.NotContains(t, got, actionCancel, "state %s in the Done lane", state)
	}
}
