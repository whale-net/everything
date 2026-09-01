package pages

import (
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards #1530's "Status & Connect" card on the deployment page
// (FR2, FR5, FR6, FR7, FR12): a gamer-facing running/stopped badge plus the
// computed connect address(es), rendered inline when running and behind an
// explicit reveal when stopped/offline, with an explicit "unavailable"
// message when no host_public_address is configured. Every case below
// builds its DeploymentStatus/ConnectAddresses/ConnectAddressUnavailable
// inputs with the real components.LatestSession /
// components.ComputeDeploymentStatus / components.ComputeConnectAddresses
// helpers (NFR1) rather than hand-picking template inputs, so a regression
// in those helpers (already unit-tested independently in
// components/deployment_status_test.go) would also surface here as a
// rendering-level failure, and the template itself never duplicates their
// logic.
//
// mutation-tested (verified red, by hand, then reverted): commenting out
// the `else if data.DeploymentStatus == components.DeploymentRunning`
// branch guard in sgc_detail.templ (so the running case always fell into
// the reveal-gated branch) made
// TestSGCDetail_RunningConfigured_AddressVisibleNoInteraction fail on its
// "must not be gated behind x-show" assertion; reverting restored green.

// buildSGCDetailData computes DeploymentStatus/ConnectAddresses/
// ConnectAddressUnavailable exactly as handleSGCDetail does, from a
// sessions list, a host_public_address, and port bindings.
func buildSGCDetailData(sgc *manmanpb.ServerGameConfig, server *manmanpb.Server, sessions []*manmanpb.Session) SGCDetailPageData {
	latest := components.LatestSession(sessions)
	status := components.ComputeDeploymentStatus(latest)
	addrs := components.ComputeConnectAddresses(server.GetHostPublicAddress(), sgc.GetPortBindings())
	return SGCDetailPageData{
		Layout:                    components.LayoutData{Title: "SGC"},
		SGC:                       sgc,
		Server:                    server,
		Sessions:                  sessions,
		DeploymentStatus:          status,
		ConnectAddresses:          addrs,
		ConnectAddressUnavailable: len(addrs) == 0,
	}
}

// statusConnectSection isolates the "Status & Connect" card's markup from
// the rest of the (much larger) SGCDetail page, so assertions about x-show
// gating or address presence can't accidentally match unrelated sections
// (e.g. the port-bindings edit form's own x-data, or the Danger Zone's
// Alpine confirm gate further down the page).
func statusConnectSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "Status &amp; Connect")
	if start < 0 {
		t.Fatalf("expected a 'Status & Connect' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "<!-- SGC Info -->")
	if end < 0 {
		t.Fatalf("expected a '<!-- SGC Info -->' marker after the Status & Connect card, got %q", body)
	}
	return body[start : start+end]
}

// --- 1. running + configured + one port binding ----------------------------

func TestSGCDetail_RunningConfigured_AddressVisibleNoInteraction(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 1,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 1, Name: "Alpha", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}}

	data := buildSGCDetailData(sgc, server, sessions)
	body := renderPage(t, SGCDetail(data))
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "play.example.com:25565") {
		t.Fatalf("expected the connect address to render, got section %q", section)
	}
	if strings.Contains(section, "x-show") {
		t.Errorf("expected the running case's address to render directly (no x-show gate), got section %q", section)
	}
	if !strings.Contains(section, "Running") {
		t.Errorf("expected the Running status label, got section %q", section)
	}
}

// --- 2. running + configured + three port bindings --------------------------

func TestSGCDetail_RunningConfigured_MultipleAddresses(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 2,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			{ContainerPort: 19132, HostPort: 19132, Protocol: "UDP"},
			{ContainerPort: 8080, HostPort: 8080, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 2, Name: "Beta", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "running"}}

	data := buildSGCDetailData(sgc, server, sessions)
	body := renderPage(t, SGCDetail(data))
	section := statusConnectSection(t, body)

	for _, want := range []string{"play.example.com:25565", "play.example.com:19132", "play.example.com:8080"} {
		if !strings.Contains(section, want) {
			t.Errorf("expected %q to render, got section %q", want, section)
		}
	}
}

// --- 3. stopped ("crashed") + configured address ----------------------------

func TestSGCDetail_StoppedConfigured_AddressGatedBehindReveal(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 3,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 3, Name: "Gamma", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "crashed"}}

	data := buildSGCDetailData(sgc, server, sessions)
	body := renderPage(t, SGCDetail(data))
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "Stopped") {
		t.Errorf("expected the Stopped/Offline status label, got section %q", section)
	}
	if !strings.Contains(section, "play.example.com:25565") {
		t.Fatalf("expected the last-known connect address to still be present in the DOM (just hidden), got section %q", section)
	}
	if !strings.Contains(section, `x-show="showConnectAddress"`) {
		t.Errorf("expected the address to be wrapped in the x-show reveal gate, got section %q", section)
	}
	// The address itself must appear after the x-show attribute opens (i.e.
	// actually inside the gated element), not merely somewhere in the card.
	gateIdx := strings.Index(section, `x-show="showConnectAddress"`)
	addrIdx := strings.Index(section, "play.example.com:25565")
	if addrIdx < gateIdx {
		t.Errorf("expected the address to appear after the x-show gate opens, got section %q", section)
	}
	if !strings.Contains(section, "Show last-known connect address") {
		t.Errorf("expected the reveal control's label, got section %q", section)
	}
}

// --- 4. empty host_public_address (any status) -------------------------------

func TestSGCDetail_NoHostPublicAddress_RendersUnavailable(t *testing.T) {
	for _, status := range []string{"running", "crashed"} {
		t.Run(status, func(t *testing.T) {
			sgc := &manmanpb.ServerGameConfig{
				ServerGameConfigId: 4,
				Status:             "active",
				PortBindings: []*manmanpb.PortBinding{
					{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
				},
			}
			server := &manmanpb.Server{ServerId: 4, Name: "Delta", HostPublicAddress: ""}
			sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: status}}

			data := buildSGCDetailData(sgc, server, sessions)
			body := renderPage(t, SGCDetail(data))
			section := statusConnectSection(t, body)

			if !strings.Contains(section, "Connect address unavailable") {
				t.Errorf("expected the unavailable message, got section %q", section)
			}
			if strings.Contains(section, ":25565") {
				t.Errorf("expected no dangling port fragment, got section %q", section)
			}
			if strings.Contains(section, "Show last-known connect address") {
				t.Errorf("expected no reveal control when unavailable, got section %q", section)
			}
			if strings.Contains(section, "<code") {
				t.Errorf("expected no address <code> element when unavailable, got section %q", section)
			}
		})
	}
}

// --- 5. empty session list ----------------------------------------------------

func TestSGCDetail_NoSessions_RendersStoppedNoAddress(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{ServerGameConfigId: 5, Status: "active"}
	server := &manmanpb.Server{ServerId: 5, Name: "Epsilon", HostPublicAddress: "play.example.com"}

	data := buildSGCDetailData(sgc, server, nil)
	body := renderPage(t, SGCDetail(data))
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "Stopped") {
		t.Errorf("expected the Stopped/Offline status label for a never-deployed SGC, got section %q", section)
	}
	if strings.Contains(section, "<code") {
		t.Errorf("expected no address to render (no port bindings), got section %q", section)
	}
}

// --- 6. pending latest session --------------------------------------------------

func TestSGCDetail_PendingSession_RendersStoppedNotRunning(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 6,
		Status:             "active",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	server := &manmanpb.Server{ServerId: 6, Name: "Zeta", HostPublicAddress: "play.example.com"}
	sessions := []*manmanpb.Session{{SessionId: 1, StartedAt: 100, Status: "pending"}}

	data := buildSGCDetailData(sgc, server, sessions)
	body := renderPage(t, SGCDetail(data))
	section := statusConnectSection(t, body)

	if !strings.Contains(section, "Stopped") {
		t.Errorf("expected the Stopped/Offline status label for a pending session, got section %q", section)
	}
	if strings.Contains(section, ">Running<") {
		t.Errorf("expected no Running label for a pending session, got section %q", section)
	}
}
