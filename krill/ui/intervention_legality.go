// The shared intervention-legality predicate (FRs 43e39aae, af61631d): which
// of the four verbs a task offers, decided from the state and lane a view
// observed. One function, so the Needs attention rows and (later) task detail
// cannot drift into two readings of the same rule.
package main

import "github.com/whale-net/everything/krill/store"

// taskInterventionState is the condition an intervention's legality is read
// from -- which one of the mutually exclusive states a view observed. It is
// deliberately a view-level reading, not a store type: a row knows which read
// produced it (a claimed row, an escalated row) and a detail page knows the
// same from the task it loaded.
type taskInterventionState string

const (
	// taskInterventionReady is a task nothing holds and nothing flagged: not
	// claimed, not escalated, not cancelled.
	taskInterventionReady taskInterventionState = "ready"
	// taskInterventionClaimed is a task a live claim holds.
	taskInterventionClaimed taskInterventionState = "claimed"
	// taskInterventionEscalated is a task carrying a current escalation.
	taskInterventionEscalated taskInterventionState = "escalated"
	// taskInterventionCancelled is a dead-lettered task, which no
	// intervention may act on.
	taskInterventionCancelled taskInterventionState = "cancelled"
)

// legalInterventions is the one reading of the FRs' action legality: the
// verbs a task in state, in lane, offers, in the order the FRs name them.
//
//   - claimed -> Release, Escalate, Cancel
//   - escalated -> Requeue, Cancel (never Release: an escalated task holds no
//     claim to force-close)
//   - ready -> Escalate, Cancel
//   - cancelled -> none
//
// A task in the Done lane is offered neither Escalate nor Cancel whatever its
// state: it is finished, so there is nothing to flag for attention and
// nothing to dead-letter. Release and Requeue survive that subtraction -- a
// finished task can still hold a claim to release, and returning a
// finished-but-escalated task to claimable is the recovery the queue exists
// for.
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
		// Cancelled, and any state a caller failed to name, offers nothing.
		// A dead-lettered task cannot be acted on, and an unknown state must
		// not invent an action for itself.
		return nil
	}
}

// escalateAndCancel is the pair of verbs the Done lane withholds, so the
// subtraction is stated once for every state that carries them.
func escalateAndCancel(done bool) []string {
	if done {
		return nil
	}
	return []string{actionEscalate, actionCancel}
}
