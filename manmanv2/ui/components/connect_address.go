package components

import (
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// ConnectAddressView is the shared render state for a deployment's connect
// address. Unavailable is true when ComputeConnectAddresses returned no
// resolvable address, so callers render the existing unavailable state
// rather than a blank cell.
//
// NFR1 (htmxui candidate): this type is arguably manmanv2-domain-specific
// -- it is built from ComputeConnectAddresses / *manmanpb.PortBinding,
// which are manmanv2 concepts -- so it stays in this package rather than
// being written for a lift into libs/go/htmxui. If a future domain needs
// the same "resolved addresses vs. unavailable" shape, that is the moment
// to reconsider, not before.
type ConnectAddressView struct {
	Addresses   []ConnectAddress
	Unavailable bool
}

// BuildConnectAddressView derives the view from the same inputs
// handleSGCDetail uses today: the server's public address and the
// deployment's port bindings.
//
// The Unavailable rule is exactly today's: len(addrs) == 0 after
// ComputeConnectAddresses -- including when hostPublicAddress is empty
// (e.g. a GetServer failure upstream leaves the caller passing "") or
// portBindings is empty. There is no separate "server missing" condition;
// handlers_sgc_test.go's GetServer-failure case records that both collapse
// to the same Unavailable state.
func BuildConnectAddressView(hostPublicAddress string, portBindings []*manmanpb.PortBinding) ConnectAddressView {
	addrs := ComputeConnectAddresses(hostPublicAddress, portBindings)
	return ConnectAddressView{
		Addresses:   addrs,
		Unavailable: len(addrs) == 0,
	}
}
