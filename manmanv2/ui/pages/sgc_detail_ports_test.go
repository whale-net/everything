package pages

import (
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards task #2094's ports section on the ServerGameConfig
// surface (FR11): existing bindings render in the same
// container_port:host_port/protocol form the section has always used, the
// empty state reads as "nothing configured" rather than an error, and the
// editor form posts its hidden port_bindings_json field to the public-API
// write route (/sgc/{id}/update-ports) the seed scripts' replace-all
// semantics share. The random-generate / inline-warning UX is deliberately
// out of scope (separate tasks).
//
// mutation-tested (verified red, by hand, then reverted): changing the
// form action in sgc_detail.templ's editPorts block from
// /sgc/{id}/update-ports to /sgc/{id}/delete made
// TestSGCDetail_PortsEditor_PostsToPublicAPIRoute fail on its action
// assertion while compiling cleanly; reverting restored green.

func renderSGCDetailPorts(t *testing.T, sgc *manmanpb.ServerGameConfig) string {
	t.Helper()
	data := SGCDetailPageData{
		Layout:           components.LayoutData{Title: "SGC"},
		SGC:              sgc,
		DeploymentStatus: components.DeploymentStopped,
	}
	var sb strings.Builder
	if err := SGCDetail(data).Render(t.Context(), &sb); err != nil {
		t.Fatalf("SGCDetail render failed: %v", err)
	}
	return sb.String()
}

// portsSection isolates the Deployment Info card's port-bindings block so
// assertions can't be satisfied by the Status & Connect card's address
// codes (which also render host:port/protocol fragments).
func portsSection(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, "Port Bindings")
	if start < 0 {
		t.Fatalf("expected a 'Port Bindings' heading in rendered body, got %q", body)
	}
	end := strings.Index(body[start:], "<!-- Libraries -->")
	if end < 0 {
		t.Fatalf("expected a '<!-- Libraries -->' marker after the Deployment Info card, got %q", body)
	}
	return body[start : start+end]
}

func TestSGCDetail_PortsBindingsListedWithProtocol(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 7,
		Status:             "inactive",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
			{ContainerPort: 27015, HostPort: 27015, Protocol: "UDP"},
		},
	}
	section := portsSection(t, renderSGCDetailPorts(t, sgc))

	for _, want := range []string{"25565:25565/TCP", "27015:27015/UDP"} {
		if !strings.Contains(section, want) {
			t.Errorf("ports section missing rendered binding %q", want)
		}
	}
}

func TestSGCDetail_PortsEmptyState(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 7,
		Status:             "inactive",
	}
	section := portsSection(t, renderSGCDetailPorts(t, sgc))

	if !strings.Contains(section, "No port bindings configured.") {
		t.Error("ports section missing empty-state message for a config with no bindings")
	}
	if strings.Contains(section, "25565:25565/") {
		t.Error("ports section rendered a binding that was never configured")
	}
}

func TestSGCDetail_PortsEditor_PostsToPublicAPIRoute(t *testing.T) {
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 7,
		Status:             "inactive",
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	section := portsSection(t, renderSGCDetailPorts(t, sgc))

	// Writes must land on /sgc/{id}/update-ports, whose handler goes
	// through UpdateServerGameConfig (NFR3: no UI-only write path).
	if !strings.Contains(section, "/sgc/7/update-ports") {
		t.Error("ports editor form does not post to /sgc/7/update-ports")
	}
	if !strings.Contains(section, `name="port_bindings_json"`) {
		t.Error("ports editor form missing its port_bindings_json hidden field")
	}
	// Editor can add and remove rows.
	if !strings.Contains(section, "+ Add Port Binding") {
		t.Error("ports editor missing its add-row affordance")
	}
	if !strings.Contains(section, "removePort") {
		t.Error("ports editor missing its remove-row affordance")
	}
}
