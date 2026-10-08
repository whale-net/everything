// Intervention legality: which verbs a task offers, decided from its observed
// state and lane. One function so every view reads the rule the same way.
package main

import "github.com/whale-net/everything/krill/store"

// taskInterventionState is the view-observed state an intervention's legality
// is read from; a view-level reading, not a store type.
type taskInterventionState string

const (
	taskInterventionReady     taskInterventionState = "ready"
	taskInterventionClaimed   taskInterventionState = "claimed"
	taskInterventionEscalated taskInterventionState = "escalated"
	// taskInterventionCancelled is a dead-lettered task; nothing may act on it.
	taskInterventionCancelled taskInterventionState = "cancelled"
)

// legalInterventions returns the verbs a task offers, in canonical order. Done
// tasks never offer Escalate or Cancel; Release and Requeue still apply there.
func legalInterventions(state taskInterventionState, lane store.Lane) []string {
	done := lane == store.LaneDone
	switch state {
	case taskInterventionClaimed:
		return append([]string{actionRelease}, escalateAndCancel(done)...)
	case taskInterventionEscalated:
		if done {
			return []string{actionRequeue}
		}
		return []string{actionRequeue, actionCancel}
	case taskInterventionReady:
		return escalateAndCancel(done)
	default:
		// Cancelled or unknown states must not invent an action.
		return nil
	}
}

func escalateAndCancel(done bool) []string {
	if done {
		return nil
	}
	return []string{actionEscalate, actionCancel}
}
