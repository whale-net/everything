package pages

import (
	"context"
	"strings"
	"testing"

	manmanpb "github.com/whale-net/everything/manmanv2/protos"
)

// Guards task #2090's layered Environment section (FR1/FR2/FR4): one row
// per variable with template/override/effective layers, explicit
// inherited/overridden badges, and override CRUD wired to the deployment's
// env endpoints.

func strP(s string) *string { return &s }

func renderSGCEnvOverrides(t *testing.T, data SGCEnvOverridesData) string {
	t.Helper()
	var sb strings.Builder
	if err := SGCEnvOverrides(data).Render(context.Background(), &sb); err != nil {
		t.Fatalf("SGCEnvOverrides render failed: %v", err)
	}
	return sb.String()
}

func TestSGCEnvOverrides_LayeredRows(t *testing.T) {
	data := SGCEnvOverridesData{
		SGC:             &manmanpb.ServerGameConfig{ServerGameConfigId: 7},
		HasTemplateKeys: true,
		Layers: []EnvVarLayer{
			{Key: "INHERITED_VAR", TemplateValue: "template-a", EffectiveValue: "template-a"},
			{Key: "OVERRIDDEN_VAR", TemplateValue: "template-b", OverrideValue: strP("override-b"), EffectiveValue: "override-b"},
			{Key: "OVERRIDE_ONLY", TemplateValue: "", OverrideValue: strP("only"), EffectiveValue: "only"},
		},
	}
	body := renderSGCEnvOverrides(t, data)

	for _, want := range []string{
		"INHERITED_VAR", "template-a", "OVERRIDDEN_VAR", "override-b", "OVERRIDE_ONLY", "only",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("rendered body missing %q", want)
		}
	}

	// Both rows carry their override endpoint with the right key
	// (remove is a per-row form with a hidden key input; the inherited
	// row still offers Edit so a template var can gain an override).
	if !strings.Contains(body, "/sgc/7/env/remove") {
		t.Errorf("remove form missing /sgc/7/env/remove action")
	}
	if !strings.Contains(body, "name=\"key\" value=\"OVERRIDDEN_VAR\"") &&
		!strings.Contains(body, `name="key" value="OVERRIDDEN_VAR"`) {
		t.Errorf("remove form missing hidden key input for OVERRIDDEN_VAR")
	}
	if !strings.Contains(body, "/sgc/7/env/edit?key=INHERITED_VAR") {
		t.Errorf("inherited row missing edit endpoint for INHERITED_VAR")
	}

	// Add form posts key+value to the set endpoint.
	if !strings.Contains(body, "/sgc/7/env/set") {
		t.Errorf("add form missing /sgc/7/env/set action")
	}
}

func TestSGCEnvOverrides_EmptyState(t *testing.T) {
	data := SGCEnvOverridesData{
		SGC: &manmanpb.ServerGameConfig{ServerGameConfigId: 7},
	}
	body := renderSGCEnvOverrides(t, data)
	if !strings.Contains(body, "No environment variables defined") {
		t.Errorf("empty state (no template keys) missing nudge text")
	}
	if strings.Contains(body, "/sgc/7/env/remove") {
		t.Errorf("empty state should render no remove forms")
	}
}

func TestSGCEnvOverrideEditForm(t *testing.T) {
	var sb strings.Builder
	if err := SGCEnvOverrideEditForm(42, "MAX_PLAYERS", "20").Render(context.Background(), &sb); err != nil {
		t.Fatalf("SGCEnvOverrideEditForm render failed: %v", err)
	}
	body := sb.String()
	for _, want := range []string{
		"/sgc/42/env/set",
		`name="key" value="MAX_PLAYERS"`,
		`name="value"`,
		`value="20"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("edit form missing %q", want)
		}
	}
}

// Task #2096 (FR3): while a running start predates the latest saved
// override edit, the Environment section shows the pending hint; it never
// renders itself without the handler's PendingEditHint input.
func TestSGCEnvOverrides_PendingHintVisibleWhileSaveIsUnrun(t *testing.T) {
	data := SGCEnvOverridesData{
		SGC: &manmanpb.ServerGameConfig{ServerGameConfigId: 7},
		Layers: []EnvVarLayer{
			{Key: "MAX_PLAYERS", TemplateValue: "20", OverrideValue: strP("32"), EffectiveValue: "32"},
		},
		HasTemplateKeys: true,
		PendingEditHint: true,
	}
	body := renderSGCEnvOverrides(t, data)

	if !strings.Contains(body, "pending override edit applies on next session start") {
		t.Error("pending hint missing while a saved edit is newer than the running session's start")
	}
	if !strings.Contains(body, `data-testid="sgc-env-pending-hint"`) {
		t.Error("pending hint missing its stable test hook")
	}
}

func TestSGCEnvOverrides_NoPendingHintWithoutInput(t *testing.T) {
	data := SGCEnvOverridesData{
		SGC: &manmanpb.ServerGameConfig{ServerGameConfigId: 7},
		Layers: []EnvVarLayer{
			{Key: "MAX_PLAYERS", TemplateValue: "20", OverrideValue: strP("32"), EffectiveValue: "32"},
		},
		HasTemplateKeys: true,
		PendingEditHint: false,
	}
	body := renderSGCEnvOverrides(t, data)

	if strings.Contains(body, "pending override edit applies on next session start") {
		t.Error("pending hint rendered although no unrun save is pending")
	}
	// The standing explanation under the editor is unchanged and always
	// present -- it describes the general behavior, not a pending state.
	if !strings.Contains(body, "Overrides apply at the next session start") {
		t.Error("standing override-timing explanation missing")
	}
}
