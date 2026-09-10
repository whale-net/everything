package pages

import (
	"context"
	"strings"
	"testing"
)

// Guards task #2274's Deployment Settings blade -- environment section
// (FR11, WD3, WD9): #2269's Blade renders this section's body, and these
// tests pin the markup contract the blade's own per-section dirty
// tracking, and the FR11 negative assertions (WD3/WD9), depend on.

func renderDeploymentSettingsEnvSection(t *testing.T, data DeploymentSettingsData) string {
	t.Helper()
	var sb strings.Builder
	if err := DeploymentSettingsEnvSection(data).Render(context.Background(), &sb); err != nil {
		t.Fatalf("DeploymentSettingsEnvSection render failed: %v", err)
	}
	return sb.String()
}

// TestDeploymentSettingsEnvSection_NeverDirty is the WD9/FR10 structural
// guard: the section always carries data-blade-section="env"
// data-blade-dirty="false" -- per-key commits leave nothing to
// accumulate, so this section can never make #2269's Blade dismissal
// prompt fire (dirtySections() only ever matches data-blade-dirty="true").
func TestDeploymentSettingsEnvSection_NeverDirty(t *testing.T) {
	data := DeploymentSettingsData{
		SGCID:       7,
		DisplayName: "vanilla on host-01",
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers: []EnvVarLayer{
				{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"},
			},
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)

	if !strings.Contains(body, `data-blade-section="env" data-blade-dirty="false"`) {
		t.Errorf("expected the never-dirty section marker, got %q", body)
	}
	if strings.Contains(body, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty marker anywhere in the env section, got %q", body)
	}
	if got := strings.Count(body, `id="deployment-settings-env-7"`); got != 1 {
		t.Errorf("expected exactly one section id, got %d in %q", got, body)
	}
}

// TestDeploymentSettingsEnvSection_WD9_NoSaveCancelPair asserts the
// negative: no blade-level Save/Cancel pair renders anywhere in this
// section (WD9) -- only per-row Save/Remove and an Add-override submit,
// each a single-key commit.
func TestDeploymentSettingsEnvSection_WD9_NoSaveCancelPair(t *testing.T) {
	data := DeploymentSettingsData{
		SGCID:       7,
		DisplayName: "vanilla on host-01",
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers: []EnvVarLayer{
				{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"},
			},
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)

	if strings.Contains(body, "Cancel") {
		t.Errorf("expected no blade-level Cancel control (WD9), got %q", body)
	}
	if got := strings.Count(body, ">Save<"); got != 1 {
		t.Errorf("expected exactly one per-row Save button (not a blade-level Save), got %d in %q", got, body)
	}
}

// TestDeploymentSettingsEnvSection_WD3_NoExtraAddonsSection asserts WD3's
// documented divergence: this blade has no "Extra Addons" section.
func TestDeploymentSettingsEnvSection_WD3_NoExtraAddonsSection(t *testing.T) {
	data := DeploymentSettingsData{SGCID: 7, DisplayName: "vanilla on host-01"}
	body := renderDeploymentSettingsEnvSection(t, data)

	if strings.Contains(body, "Extra Addons") {
		t.Errorf("expected no Extra Addons section (WD3), got %q", body)
	}
}

// TestDeploymentSettingsEnvSection_FR2_NoRawSGCInDisplayText mirrors
// #2269's blade-level FR2 guard, scoped to this section's *display text*
// -- no "SGC" or "server game config" anywhere a user would read it. The
// shipped write-path routes this section posts to (handlers_sgc_env.go's
// /sgc/{id}/env/set and /remove, unchanged by this task) are excluded:
// FR2 governs what the UI names things for a human, not the existing,
// already-shipped URL scheme those endpoints live at.
func TestDeploymentSettingsEnvSection_FR2_NoRawSGCInDisplayText(t *testing.T) {
	data := DeploymentSettingsData{
		SGCID:       7,
		DisplayName: "vanilla on host-01",
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers: []EnvVarLayer{
				{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"},
			},
			PendingEditHint: true,
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)
	displayText := strings.ReplaceAll(body, "/sgc/7/", "/deployment/7/")
	lower := strings.ToLower(displayText)
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no raw SGC identifier in display text (FR2), got %q", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf(`expected no "server game config" in display text (FR2), got %q`, body)
	}
}

// TestDeploymentSettingsEnvSection_PerKeyForms_NFR5 is the load-bearing
// NFR5/LB4 markup guard: every write in this section is a single <form>
// posting exactly one "key" field to the shipped per-key endpoints --
// never a form (or any other element) that could carry more than one key
// per request.
func TestDeploymentSettingsEnvSection_PerKeyForms_NFR5(t *testing.T) {
	data := DeploymentSettingsData{
		SGCID:       7,
		DisplayName: "vanilla on host-01",
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers: []EnvVarLayer{
				{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"},
				{Key: "DIFFICULTY", TemplateValue: "normal", EffectiveValue: "normal"},
			},
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)

	if got := strings.Count(body, `name="key" value="MAX_PLAYERS"`); got != 2 {
		// One in the row's Save form, one in its Remove form (overridden row).
		t.Errorf(`expected MAX_PLAYERS' hidden key input in exactly 2 forms (Save + Remove), got %d in %q`, got, body)
	}
	if got := strings.Count(body, `name="key" value="DIFFICULTY"`); got != 1 {
		// Inherited row: Save form only, no Remove form.
		t.Errorf(`expected DIFFICULTY's hidden key input in exactly 1 form (Save only), got %d in %q`, got, body)
	}
	if got := strings.Count(body, "/sgc/7/env/set"); got != 3 { // 2 row Save forms + 1 add-variable form
		t.Errorf("expected 3 forms posting to /sgc/7/env/set (per-row Save x2 + add-variable), got %d in %q", got, body)
	}
	if got := strings.Count(body, "/sgc/7/env/remove"); got != 1 { // only the overridden row
		t.Errorf("expected 1 form posting to /sgc/7/env/remove, got %d in %q", got, body)
	}
	// No bulk-shaped input: no array/JSON-bearing field name.
	if strings.Contains(body, "keys[]") || strings.Contains(body, "overrides_json") || strings.Contains(body, "env_json") {
		t.Errorf("expected no bulk-shaped form field, got %q", body)
	}
}

// TestDeploymentSettingsEnvSection_AbsentVsEmptyOverride guards FR11's
// round-trip distinction: an override saved as the empty string still
// renders as overridden (with a Remove form), not as inherited.
func TestDeploymentSettingsEnvSection_AbsentVsEmptyOverride(t *testing.T) {
	empty := ""
	data := DeploymentSettingsData{
		SGCID: 7,
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers: []EnvVarLayer{
				{Key: "MOTD", TemplateValue: "hello", OverrideValue: &empty, EffectiveValue: ""},
				{Key: "DIFFICULTY", TemplateValue: "normal", EffectiveValue: "normal"},
			},
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)

	if !strings.Contains(body, `name="key" value="MOTD"`) {
		t.Fatalf("expected MOTD's row to render, got %q", body)
	}
	if got := strings.Count(body, `name="key" value="MOTD"`); got != 2 {
		t.Errorf("expected MOTD overridden-to-empty to still render a Remove form (2 key inputs: Save + Remove), got %d in %q", got, body)
	}
	if got := strings.Count(body, `name="key" value="DIFFICULTY"`); got != 1 {
		t.Errorf("expected inherited DIFFICULTY to render only a Save form (1 key input), got %d in %q", got, body)
	}
	// MOTD's badge must read overridden, not inherited.
	motdIdx := strings.Index(body, "MOTD")
	nextRowIdx := strings.Index(body[motdIdx+1:], "<tr")
	var motdRow string
	if nextRowIdx == -1 {
		motdRow = body[motdIdx:]
	} else {
		motdRow = body[motdIdx : motdIdx+1+nextRowIdx]
	}
	if !strings.Contains(motdRow, "overridden") {
		t.Errorf("expected MOTD row to show the overridden badge, got %q", motdRow)
	}
}

// TestDeploymentSettingsEnvSection_PendingHint mirrors SGCEnvOverrides'
// pending-hint guard: this section reuses the same shipped
// pendingEnvOverrideHint signal (never a second drift concept).
func TestDeploymentSettingsEnvSection_PendingHint(t *testing.T) {
	data := DeploymentSettingsData{
		SGCID: 7,
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers:          []EnvVarLayer{{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"}},
			PendingEditHint: true,
		},
	}
	body := renderDeploymentSettingsEnvSection(t, data)
	if !strings.Contains(body, "pending override edit applies on next session start") {
		t.Error("expected the pending hint to render when PendingEditHint is true")
	}
	if !strings.Contains(body, `data-testid="deployment-settings-env-pending-hint"`) {
		t.Error("expected the pending hint's stable test hook")
	}

	data.EnvOverrides.PendingEditHint = false
	body = renderDeploymentSettingsEnvSection(t, data)
	if strings.Contains(body, "pending override edit applies on next session start") {
		t.Error("expected no pending hint when PendingEditHint is false")
	}
}

// TestDeploymentSettingsEnvPlaceholder guards the lazy-load contract
// (NFR7): the placeholder carries the same never-dirty section markers as
// the real section, an hx-get to the fragment endpoint, and
// hx-trigger="load" so it fires once the blade is inserted and processed
// -- never at Games-page render time.
func TestDeploymentSettingsEnvPlaceholder(t *testing.T) {
	var sb strings.Builder
	if err := DeploymentSettingsEnvPlaceholder(7).Render(context.Background(), &sb); err != nil {
		t.Fatalf("DeploymentSettingsEnvPlaceholder render failed: %v", err)
	}
	body := sb.String()

	if !strings.Contains(body, `id="deployment-settings-env-7"`) {
		t.Errorf("expected the placeholder to share the section's stable id, got %q", body)
	}
	if !strings.Contains(body, `data-blade-section="env" data-blade-dirty="false"`) {
		t.Errorf("expected the never-dirty section marker on the placeholder, got %q", body)
	}
	if !strings.Contains(body, `hx-get="/deployment-settings/7/env"`) {
		t.Errorf("expected an hx-get to the lazy-load fragment endpoint, got %q", body)
	}
	if strings.Contains(strings.ToLower(body), "sgc") {
		t.Errorf("expected no raw SGC identifier in the placeholder (FR2) -- it is part of the Games page's own initial render, got %q", body)
	}
	if !strings.Contains(body, `hx-trigger="load"`) {
		t.Errorf(`expected hx-trigger="load", got %q`, body)
	}
}

// TestDeploymentSettingsEnvSectionID pins the id format both the
// placeholder and the fragment share.
func TestDeploymentSettingsEnvSectionID(t *testing.T) {
	if got, want := DeploymentSettingsEnvSectionID(42), "deployment-settings-env-42"; got != want {
		t.Errorf("DeploymentSettingsEnvSectionID(42) = %q, want %q", got, want)
	}
}
