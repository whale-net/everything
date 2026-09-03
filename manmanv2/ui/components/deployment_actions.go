package components

import (
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// DeploymentActions describes which one-click controls a deployment row offers.
type DeploymentActions struct {
	CanStart   bool
	CanStop    bool
	CanRestart bool
}

// LatestSessionStatus returns the status string of the newest session for a
// deployment, or "" when the deployment has never had a session.
func LatestSessionStatus(sessions []*manmanpb.Session) string {
	return LatestSession(sessions).GetStatus()
}

// ComputeDeploymentActions derives the available one-click controls from a
// deployment's latest session, per the M2 plan's action-availability table
// (#1620):
//
//	latest session | CanStart | CanStop | CanRestart
//	nil            | false    | false   | false
//	pending        | false    | false   | false
//	starting       | false    | false   | false
//	running        | false    | true    | true
//	stopping       | false    | false   | false
//	stopped        | true     | false   | false
//	crashed        | true     | false   | true
//	lost           | true     | false   | true
//	unknown/empty  | false    | false   | false
//
//   - FR1: Start is offered on stopped/crashed/lost. A deployment with no
//     session at all shows no Start action -- deploy-and-start is out of
//     scope for M2.
//   - FR3: Stop is offered only when there is a live running session.
//   - FR5: Restart is offered on running/crashed/lost.
//
// Any status not covered above (including nil and unrecognised strings)
// yields all-false, matching ComputeDeploymentStatus's fail-closed
// convention.
func ComputeDeploymentActions(latest *manmanpb.Session) DeploymentActions {
	if latest == nil {
		return DeploymentActions{}
	}
	switch latest.GetStatus() {
	case "running":
		return DeploymentActions{CanStop: true, CanRestart: true}
	case "stopped":
		return DeploymentActions{CanStart: true}
	case "crashed", "lost":
		return DeploymentActions{CanStart: true, CanRestart: true}
	default:
		// pending, starting, stopping, empty, and any unknown status.
		return DeploymentActions{}
	}
}

// IsTransientStatus reports whether a status is a non-terminal state the UI
// should keep polling on (pending, starting, stopping).
func IsTransientStatus(status string) bool {
	switch status {
	case "pending", "starting", "stopping":
		return true
	default:
		return false
	}
}
