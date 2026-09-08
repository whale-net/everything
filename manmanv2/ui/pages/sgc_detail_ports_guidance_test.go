package pages

import (
	"encoding/json"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
)

// This file guards task #2098's render-time FR13/FR14 wiring on the
// ServerGameConfig detail page: when handleSGCDetail ships a PortContext,
// the ports editor gets (a) the guidance JSON as a data-port-context
// attribute, (b) a per-binding warning element, (c) a random-generate
// affordance wired to the /sgc/{id}/random-port endpoint (FR13), and
// (d) a random-failure notice container. With no PortContext the editor
// renders without any of it (guidance degrades silently, Decision 7).
//
// mutation-tested (verified red, by hand, then reverted): guarding the
// data-port-context attribute block with `false` made
// TestSGCDetail_PortContext_ShipsContextData fail; guarding the random
// button with `false` made TestSGCDetail_PortContext_RendersRandomButton
// fail; reverting restored green in both cases.

func renderSGCDetailGuidance(t *testing.T, portCtx *SGCPortContext) string {
	t.Helper()
	sgc := &manmanpb.ServerGameConfig{
		ServerGameConfigId: 7,
		ServerId:           1,
		PortBindings: []*manmanpb.PortBinding{
			{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
		},
	}
	data := SGCDetailPageData{
		Layout:           components.LayoutData{Title: "SGC"},
		SGC:              sgc,
		DeploymentStatus: components.DeploymentStopped,
		PortContext:      portCtx,
	}
	var sb strings.Builder
	if err := SGCDetail(data).Render(t.Context(), &sb); err != nil {
		t.Fatalf("SGCDetail render failed: %v", err)
	}
	return sb.String()
}

func guidancePortContext() *SGCPortContext {
	return &SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{
			"TCP": {{Start: 25565, End: 25570, Protocol: "TCP"}},
		},
		InUse: map[string][]int32{"TCP": {25566, 30000}},
	}
}

func TestSGCDetail_PortContext_ShipsContextData(t *testing.T) {
	body := renderSGCDetailGuidance(t, guidancePortContext())

	start := strings.Index(body, "data-port-context=")
	if start < 0 {
		t.Fatalf("expected a data-port-context attribute when PortContext is set")
	}
	end := strings.Index(body[start:], `"><form`)
	// The attribute value is HTML-escaped JSON; decode the quoted span.
	raw := body[start+len(`data-port-context=`):]
	quoteEnd := strings.Index(raw[1:], `"`)
	if quoteEnd < 0 {
		t.Fatalf("could not find attribute value end")
	}
	_ = end
	escaped := raw[1 : 1+quoteEnd]
	unescaped := strings.NewReplacer(`&quot;`, `"`, `&amp;`, `&`, `&#34;`, `"`, `&#39;`, "'", `&lt;`, "<", `&gt;`, ">").Replace(escaped)
	var decoded SGCPortContext
	if err := json.Unmarshal([]byte(unescaped), &decoded); err != nil {
		t.Fatalf("data-port-context is not valid SGCPortContext JSON: %v (%q)", err, unescaped)
	}
	if len(decoded.Ranges["TCP"]) != 1 || decoded.Ranges["TCP"][0].GetStart() != 25565 {
		t.Errorf("ranges = %+v, want TCP 25565-25570", decoded.Ranges)
	}
	found := false
	for _, p := range decoded.InUse["TCP"] {
		if p == 25566 {
			found = true
		}
	}
	if !found {
		t.Errorf("in_use missing allocated 25566: %+v", decoded.InUse)
	}
}

func TestSGCDetail_PortContext_RendersRandomButtonAndWarnings(t *testing.T) {
	body := renderSGCDetailGuidance(t, guidancePortContext())

	for _, want := range []string{
		`data-testid="sgc-port-random"`,
		`data-testid="sgc-port-warning"`,
		`data-testid="sgc-port-random-notice"`,
		`data-random-port-base="/sgc/7/random-port"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered page missing %q", want)
		}
	}
}

// Decision 7 / SB-1.2: no context (fetch failure or genuinely range-less
// server) renders the editor bare -- no random affordance, no warnings,
// and crucially no save-blocking markup.
func TestSGCDetail_NoPortContext_RendersBareEditor(t *testing.T) {
	body := renderSGCDetailGuidance(t, nil)

	for _, banned := range []string{
		"data-port-context",
		"data-random-port-base",
		`data-testid="sgc-port-random"`,
		`data-testid="sgc-port-random-notice"`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("bare editor must not render %q", banned)
		}
	}
	if !strings.Contains(body, `data-testid="sgc-port-warning"`) {
		t.Errorf("warning element should remain (x-text yields empty without context)")
	}
}
