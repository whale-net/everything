package components

import (
	"fmt"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// DeploymentStatus is the gamer-facing rollup of a deployment's latest session state.
type DeploymentStatus string

const (
	DeploymentRunning DeploymentStatus = "running"
	DeploymentStopped DeploymentStatus = "stopped"
)

// LatestSession returns the newest session for a deployment, or nil.
//
// Newest is determined by started_at, falling back to the highest session_id
// when started_at is 0 or tied (a pending session has started_at == 0). The
// input slice is never mutated; the caller's ordering is not guaranteed.
func LatestSession(sessions []*manmanpb.Session) *manmanpb.Session {
	var latest *manmanpb.Session
	for _, s := range sessions {
		if s == nil {
			continue
		}
		if latest == nil {
			latest = s
			continue
		}
		if s.GetStartedAt() > latest.GetStartedAt() {
			latest = s
		} else if s.GetStartedAt() == latest.GetStartedAt() && s.GetSessionId() > latest.GetSessionId() {
			latest = s
		}
	}
	return latest
}

// ComputeDeploymentStatus maps a latest session to the two-state gamer-facing rollup.
//
// Session.status == "running" maps to DeploymentRunning. Every other status
// (including nil, empty, and unrecognised values) maps to DeploymentStopped —
// fail closed.
func ComputeDeploymentStatus(latest *manmanpb.Session) DeploymentStatus {
	if latest == nil {
		return DeploymentStopped
	}
	if latest.GetStatus() == "running" {
		return DeploymentRunning
	}
	return DeploymentStopped
}

// ConnectAddress is one displayable host:port pair.
type ConnectAddress struct {
	Address  string // "<host_public_address>:<host_port>"
	Port     int32
	Protocol string // "TCP" | "UDP"
}

// ComputeConnectAddresses builds one ConnectAddress per port binding.
// Returns nil when hostPublicAddress is empty or there are no port bindings.
//
// M1 does no validation or normalisation of hostPublicAddress — it is passed
// through as configured, and no "primary" port is designated; every bound
// port is represented.
func ComputeConnectAddresses(hostPublicAddress string, portBindings []*manmanpb.PortBinding) []ConnectAddress {
	if hostPublicAddress == "" || len(portBindings) == 0 {
		return nil
	}
	addresses := make([]ConnectAddress, 0, len(portBindings))
	for _, pb := range portBindings {
		if pb == nil {
			continue
		}
		addresses = append(addresses, ConnectAddress{
			Address:  fmt.Sprintf("%s:%d", hostPublicAddress, pb.GetHostPort()),
			Port:     pb.GetHostPort(),
			Protocol: pb.GetProtocol(),
		})
	}
	if len(addresses) == 0 {
		return nil
	}
	return addresses
}
