package components

import (
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
// TODO(#1529): implement newest-by-started_at selection, falling back to the
// highest session_id when started_at is 0 or tied. Must not mutate the input
// slice.
func LatestSession(sessions []*manmanpb.Session) *manmanpb.Session {
	return nil
}

// ComputeDeploymentStatus maps a latest session to the two-state gamer-facing rollup.
//
// TODO(#1529): implement the "running" -> DeploymentRunning, everything else
// (including nil and unrecognised status) -> DeploymentStopped mapping.
func ComputeDeploymentStatus(latest *manmanpb.Session) DeploymentStatus {
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
// TODO(#1529): implement per-binding "host:port" formatting, one entry per
// binding in SGC order, nil when hostPublicAddress is empty.
func ComputeConnectAddresses(hostPublicAddress string, portBindings []*manmanpb.PortBinding) []ConnectAddress {
	return nil
}
