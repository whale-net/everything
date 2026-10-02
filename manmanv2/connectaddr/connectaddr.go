// Package connectaddr derives a deployment's player-facing connect addresses
// from its host's public address and port bindings. It is UI-free so the web
// UI and the MCP server share one derivation.
package connectaddr

import (
	"fmt"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Entry is one connect address, one per port binding.
type Entry struct {
	IP       string // host public address, passed through unmodified
	Port     int32  // host port
	Protocol string // "TCP" | "UDP"
}

// Address renders the entry as "<ip>:<port>".
func (e Entry) Address() string { return fmt.Sprintf("%s:%d", e.IP, e.Port) }

// Unavailable reasons.
const (
	ReasonNoPublicAddress = "host has no public address"
	ReasonNoPortBindings  = "deployment has no port bindings"
)

// Result is either a non-empty Entries list or an explicit Unavailable with
// a Reason; it is never an empty list that looks available.
type Result struct {
	Entries     []Entry
	Unavailable bool
	Reason      string
}

// Derive builds one Entry per non-nil port binding. The host address is not
// validated or normalised, and no binding is designated primary.
func Derive(hostPublicAddress string, portBindings []*manmanpb.PortBinding) Result {
	if hostPublicAddress == "" {
		return Result{Unavailable: true, Reason: ReasonNoPublicAddress}
	}
	entries := make([]Entry, 0, len(portBindings))
	for _, pb := range portBindings {
		if pb == nil {
			continue
		}
		entries = append(entries, Entry{IP: hostPublicAddress, Port: pb.GetHostPort(), Protocol: pb.GetProtocol()})
	}
	if len(entries) == 0 {
		return Result{Unavailable: true, Reason: ReasonNoPortBindings}
	}
	return Result{Entries: entries}
}
