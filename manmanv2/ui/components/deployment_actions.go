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
	// TODO(#1625, scaffold): implement via LatestSession(sessions).GetStatus().
	return ""
}

// ComputeDeploymentActions derives the available one-click controls from a
// deployment's latest session.
//
// TODO(#1625, scaffold): implement the FR1/FR3/FR5 availability table.
func ComputeDeploymentActions(latest *manmanpb.Session) DeploymentActions {
	return DeploymentActions{}
}

// IsTransientStatus reports whether a status is a non-terminal state the UI
// should keep polling on (pending, starting, stopping).
func IsTransientStatus(status string) bool {
	// TODO(#1625, scaffold): implement.
	return false
}
