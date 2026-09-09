package components

import (
	"context"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// renderConnectAddressDisplay renders ConnectAddressDisplay directly
// (mirrors renderLiveRegion in live_indicator_test.go), so this component's
// own markup contract -- the copyable address control and the unavailable
// message -- is guarded independently of any page that embeds it.
func renderConnectAddressDisplay(t *testing.T, view ConnectAddressView) string {
	t.Helper()
	var buf strings.Builder
	if err := ConnectAddressDisplay(view).Render(context.Background(), &buf); err != nil {
		t.Fatalf("ConnectAddressDisplay render failed: %v", err)
	}
	return buf.String()
}

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

// TestConnectAddressDisplay_RendersCopyableAddresses guards
// ConnectAddressDisplay's own markup contract: each address renders as a
// <code> element plus a copy control wired to the copyConnectAddress
// script, not just the bare address/protocol text. This is the specific
// markup sgc_detail.templ's fix commit started delegating to instead of
// hand-rolling; a caller that reverted to a hand-rolled <ul>/<li> would
// still match the address/protocol substrings sgc_detail_status_connect_test.go
// asserts, but would never produce the copy button/script this test pins,
// which is why that page-level test also asserts these markers (see
// sgc_detail_status_connect_test.go).
func TestConnectAddressDisplay_RendersCopyableAddresses(t *testing.T) {
	html := renderConnectAddressDisplay(t, ConnectAddressView{
		Addresses: []ConnectAddress{
			{Address: "play.example.com:25565", Protocol: "TCP"},
			{Address: "play.example.com:19132", Protocol: "UDP"},
		},
	})

	for _, want := range []string{"play.example.com:25565", "play.example.com:19132"} {
		if !strings.Contains(html, want) {
			t.Errorf("expected %q to render, got %q", want, html)
		}
	}
	if !strings.Contains(html, `title="Copy connect address"`) {
		t.Errorf("expected a copy-address control, got %q", html)
	}
	if !strings.Contains(html, "⧉") {
		t.Errorf("expected the copy icon, got %q", html)
	}
	if !strings.Contains(html, `onclick="__templ_copyConnectAddress_`) {
		t.Errorf("expected the copy button wired to the copyConnectAddress script, got %q", html)
	}
	if strings.Contains(html, "Connect address unavailable") {
		t.Errorf("expected no unavailable message when addresses are present, got %q", html)
	}
}

// TestConnectAddressDisplay_Unavailable guards the unavailable branch:
// no address markup or copy control renders, only the existing message.
func TestConnectAddressDisplay_Unavailable(t *testing.T) {
	html := renderConnectAddressDisplay(t, ConnectAddressView{Unavailable: true})

	if !strings.Contains(html, "Connect address unavailable") {
		t.Errorf("expected the unavailable message, got %q", html)
	}
	if strings.Contains(html, "<code") {
		t.Errorf("expected no address <code> element when unavailable, got %q", html)
	}
	if strings.Contains(html, "Copy connect address") {
		t.Errorf("expected no copy control when unavailable, got %q", html)
	}
}
