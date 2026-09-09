package components

import (
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// TestBuildConnectAddressView covers the task's connect_address_test.go
// table: resolvable address(es) yield Unavailable == false with the
// byte-identical ComputeConnectAddresses output, and empty bindings /
// empty-or-missing host_public_address both collapse to Unavailable ==
// true -- exactly today's len(addrs) == 0 rule (issue #2268), including
// the handlers_sgc_test.go:185 GetServer-failure case, which upstream
// represents as an empty hostPublicAddress rather than a distinct
// "server missing" condition.
func TestBuildConnectAddressView(t *testing.T) {
	singleBinding := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}
	multiBinding := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		{ContainerPort: 19132, HostPort: 19132, Protocol: "UDP"},
	}

	cases := []struct {
		name              string
		hostPublicAddress string
		portBindings      []*manmanpb.PortBinding
		wantUnavailable   bool
	}{
		{"single resolvable address", "play.example.com", singleBinding, false},
		{"multiple resolvable addresses", "play.example.com", multiBinding, false},
		{"empty port bindings", "play.example.com", nil, true},
		{"empty host public address", "", singleBinding, true},
		{"empty host and empty bindings", "", nil, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			view := BuildConnectAddressView(tc.hostPublicAddress, tc.portBindings)
			if view.Unavailable != tc.wantUnavailable {
				t.Errorf("BuildConnectAddressView(%q, %d bindings).Unavailable = %v, want %v",
					tc.hostPublicAddress, len(tc.portBindings), view.Unavailable, tc.wantUnavailable)
			}

			want := ComputeConnectAddresses(tc.hostPublicAddress, tc.portBindings)
			if len(view.Addresses) != len(want) {
				t.Fatalf("BuildConnectAddressView(%q, %d bindings).Addresses has %d entries, want %d",
					tc.hostPublicAddress, len(tc.portBindings), len(view.Addresses), len(want))
			}
			for i := range want {
				// Byte-identical to ComputeConnectAddresses: no
				// reformatting of the address string.
				if view.Addresses[i] != want[i] {
					t.Errorf("BuildConnectAddressView(%q, %d bindings).Addresses[%d] = %+v, want %+v",
						tc.hostPublicAddress, len(tc.portBindings), i, view.Addresses[i], want[i])
				}
			}
		})
	}
}

// TestBuildConnectAddressView_GetServerFailureCollapsesToUnavailable pins
// handlers_sgc_test.go:185's recorded behaviour verbatim: when the caller's
// upstream GetServer call fails, hostPublicAddress arrives as "" (the
// caller's zero value, not a distinct sentinel), and that must still
// collapse to Unavailable == true through the exact same len(addrs) == 0
// rule as an empty host from any other cause -- no separate "server
// missing" condition.
func TestBuildConnectAddressView_GetServerFailureCollapsesToUnavailable(t *testing.T) {
	bindings := []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}

	// hostPublicAddress == "" is exactly what a *manmanpb.Server{} zero
	// value (the shape a failed GetServer leaves the caller with) yields
	// from GetHostPublicAddress().
	view := BuildConnectAddressView("", bindings)

	if !view.Unavailable {
		t.Errorf("BuildConnectAddressView(GetServer-failure host, bindings).Unavailable = false, want true")
	}
	if len(view.Addresses) != 0 {
		t.Errorf("BuildConnectAddressView(GetServer-failure host, bindings).Addresses = %+v, want empty", view.Addresses)
	}
}
