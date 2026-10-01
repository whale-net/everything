// The observed-state guard shared by the four operator interventions --
// ReleaseLease (task_release.go), RequeueTask (task_requeue.go),
// CancelTask (task_cancel.go) and EscalateTask (task_escalate.go). A
// console row or agent read once names the claim (task.current_claim_id)
// or escalation (task.current_escalation_id) it saw; the write that
// follows can carry that id back, and is refused if the state moved on
// between the read and the write -- so a Release posted from a stale page
// never force-closes whatever claim became current in the meantime.
//
// Every id here is one the caller's own row read returned; an unguarded
// caller (an existing agent or MCP tool, or a row that held no claim or
// escalation at all) simply leaves the field nil and the check is a
// no-op.
package store

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
)

// ErrObservedStateMismatch is the observed-state guard's named refusal: a
// caller supplied an expected claim or escalation id that is no longer the
// task's current one -- including the task holding none at all. It is
// deliberately distinct from every legality refusal (ErrTaskNotClaimed,
// ErrTaskNotEscalated, ErrTaskCancelled, ErrTaskAlreadyCancelled,
// ErrTaskEscalated) so a client can tell "the state changed since you
// looked, re-read and retry" from "this action is not legal". Nothing is
// written when it fires.
var ErrObservedStateMismatch = errors.New("krill/store: observed claim or escalation is no longer current")

// observedID renders an optional current id for an error message: an
// absent current id reads as "none" rather than as a zero uuid.
func observedID(current *uuid.UUID) string {
	if current == nil {
		return "none"
	}
	return current.String()
}

// checkObservedClaim compares the claim the caller observed against the
// task's current claim, read inside the caller's row-locked transaction.
// An expected id of nil means the caller observed nothing to guard and the
// call is unguarded; otherwise the task must hold exactly that claim.
//
// This is the one place the "supplied but the task holds none" case is
// decided as a mismatch rather than as ErrTaskNotClaimed, so Release's
// mapping stays unambiguous.
func checkObservedClaim(taskID uuid.UUID, current, expected *uuid.UUID) error {
	if expected == nil {
		return nil
	}
	if current == nil || *current != *expected {
		return fmt.Errorf("%w: expected claim %s, current claim %s: task id %s",
			ErrObservedStateMismatch, expected, observedID(current), taskID)
	}
	return nil
}

// checkObservedEscalation is checkObservedClaim's counterpart for the
// task's current escalation, supplied by requeue (whose whole action acts
// on one escalation) and by cancel (whose Escalated-row form guards the
// escalation it is about to supersede). A task holding no escalation when
// one was expected is a mismatch, not a legality refusal.
func checkObservedEscalation(taskID uuid.UUID, current, expected *uuid.UUID) error {
	if expected == nil {
		return nil
	}
	if current == nil || *current != *expected {
		return fmt.Errorf("%w: expected escalation %s, current escalation %s: task id %s",
			ErrObservedStateMismatch, expected, observedID(current), taskID)
	}
	return nil
}
