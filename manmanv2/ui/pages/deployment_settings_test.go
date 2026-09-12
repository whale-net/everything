package pages

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
	manmanpb "github.com/whale-net/everything/manmanv2/protos"
	"github.com/whale-net/everything/manmanv2/ui/components"
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

// --- Ports section (task #2275: FR12, the accumulating half of FR10,
// WD10) -----------------------------------------------------------------

func renderDeploymentSettingsPortsSection(t *testing.T, data DeploymentSettingsPortsData) string {
	t.Helper()
	var sb strings.Builder
	if err := DeploymentSettingsPortsSection(data).Render(context.Background(), &sb); err != nil {
		t.Fatalf("DeploymentSettingsPortsSection render failed: %v", err)
	}
	return sb.String()
}

func samplePortBindings() []*manmanpb.PortBinding {
	return []*manmanpb.PortBinding{
		{ContainerPort: 25565, HostPort: 25565, Protocol: "TCP"},
	}
}

func samplePortContext() *SGCPortContext {
	return &SGCPortContext{
		Ranges: map[string][]*manmanpb.PortRange{"TCP": {{Start: 25565, End: 25570, Protocol: "TCP"}}},
		InUse:  map[string][]int32{"TCP": {25566}},
	}
}

// TestDeploymentSettingsPortsSectionID pins the id format shared by the
// placeholder and every rendered fragment.
func TestDeploymentSettingsPortsSectionID(t *testing.T) {
	if got, want := DeploymentSettingsPortsSectionID(42), "deployment-settings-ports-42"; got != want {
		t.Errorf("DeploymentSettingsPortsSectionID(42) = %q, want %q", got, want)
	}
}

// TestDeploymentSettingsPortsPlaceholder guards the lazy-load contract
// (NFR7), mirroring TestDeploymentSettingsEnvPlaceholder: the placeholder
// starts never-dirty, fetches from the non-"sgc" prefixed route (FR2), and
// fires on hx-trigger="load" only.
func TestDeploymentSettingsPortsPlaceholder(t *testing.T) {
	var sb strings.Builder
	if err := DeploymentSettingsPortsPlaceholder(7).Render(context.Background(), &sb); err != nil {
		t.Fatalf("DeploymentSettingsPortsPlaceholder render failed: %v", err)
	}
	body := sb.String()

	if !strings.Contains(body, `id="deployment-settings-ports-7"`) {
		t.Errorf("expected the placeholder to share the section's stable id, got %q", body)
	}
	if !strings.Contains(body, `data-blade-section="ports" data-blade-dirty="false"`) {
		t.Errorf("expected the initial never-dirty marker on the placeholder, got %q", body)
	}
	if !strings.Contains(body, `hx-get="/deployment-settings/7/ports"`) {
		t.Errorf("expected an hx-get to the lazy-load fragment endpoint, got %q", body)
	}
	if !strings.Contains(body, `hx-trigger="load"`) {
		t.Errorf(`expected hx-trigger="load", got %q`, body)
	}
	if strings.Contains(strings.ToLower(body), "sgc") {
		t.Errorf("expected no raw SGC identifier in the placeholder (FR2), got %q", body)
	}
}

// TestDeploymentSettingsPortsSection_DirtyAttrReflectsData guards the
// section's dirty seeding: Dirty=false (initial load or post-Save) renders
// data-blade-dirty="false"; Dirty=true (a rejected Save's re-render) keeps
// the dirty marker so FR10 keeps prompting.
func TestDeploymentSettingsPortsSection_DirtyAttrReflectsData(t *testing.T) {
	clean := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings()})
	if !strings.Contains(clean, `data-blade-section="ports" data-blade-dirty="false"`) {
		t.Errorf("expected the never-yet-dirty marker on a fresh/clean render, got %q", clean)
	}
	if strings.Contains(clean, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty marker when Dirty=false, got %q", clean)
	}

	dirty := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings(), Dirty: true})
	if !strings.Contains(dirty, `data-blade-section="ports" data-blade-dirty="true"`) {
		t.Errorf("expected the dirty marker after a rejected Save, got %q", dirty)
	}
}

// TestDeploymentSettingsPortsSection_ErrorRendering guards the failure
// path's markup: a Save rejection surfaces an inline error with a stable
// test hook.
func TestDeploymentSettingsPortsSection_ErrorRendering(t *testing.T) {
	body := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{
		SGCID:        7,
		PortBindings: samplePortBindings(),
		Error:        "Failed to save port bindings. Your entered bindings are unchanged below.",
		Dirty:        true,
	})
	if !strings.Contains(body, `data-testid="deployment-settings-ports-error"`) {
		t.Errorf("expected the error alert's stable test hook, got %q", body)
	}
	if !strings.Contains(body, "Failed to save port bindings") {
		t.Errorf("expected the error message to render, got %q", body)
	}

	noError := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings()})
	if strings.Contains(noError, `data-testid="deployment-settings-ports-error"`) {
		t.Errorf("expected no error alert when Error is empty, got %q", noError)
	}
}

// TestDeploymentSettingsPortsSection_BindingsAndContextDataAttrs guards
// the section's Alpine hand-off: the current bindings and the FR13/FR14
// guidance context both round-trip into data-* attributes Alpine's init()
// reads at mount (portsAlpineState), and PortContext == nil (a degraded
// auxiliary fetch) omits the context attribute rather than rendering
// "null" -- portsAlpineState's own default `{ranges: {}, in_use: {}}` is
// what should apply in that case.
func TestDeploymentSettingsPortsSection_BindingsAndContextDataAttrs(t *testing.T) {
	body := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{
		SGCID:        7,
		PortBindings: samplePortBindings(),
		PortContext:  samplePortContext(),
	})
	// templ.JSONString's output is HTML-attribute-escaped -- double quotes
	// render as &#34;, not " -- so these assertions match the escaped form
	// actually present in the served markup, not the raw JSON.
	if !strings.Contains(body, `&#34;container_port&#34;:25565`) || !strings.Contains(body, `&#34;host_port&#34;:25565`) {
		t.Errorf("expected the current bindings to round-trip into data-port-bindings, got %q", body)
	}
	if !strings.Contains(body, `&#34;start&#34;:25565`) || !strings.Contains(body, `&#34;in_use&#34;`) {
		t.Errorf("expected the guidance context to round-trip into data-port-context, got %q", body)
	}

	noContext := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings()})
	if strings.Contains(noContext, "data-port-context") {
		t.Errorf("expected no data-port-context attribute when PortContext is nil (degraded guidance fetch), got %q", noContext)
	}
}

// TestDeploymentSettingsPortsSection_FR2_NoRawSGCInDisplayText mirrors the
// env section's FR2 guard, scoped to the ports section's display text: the
// only route it posts to (/deployment-settings/{id}/ports/save) already
// avoids "sgc" in its URL (see DeploymentSettingsEnvPlaceholder's doc
// comment on why), so unlike the env test this needs no substitution
// before asserting.
func TestDeploymentSettingsPortsSection_FR2_NoRawSGCInDisplayText(t *testing.T) {
	body := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{
		SGCID:        7,
		PortBindings: samplePortBindings(),
		PortContext:  samplePortContext(),
		Error:        "Failed to save port bindings. Your entered bindings are unchanged below.",
	})
	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no raw SGC identifier in display text (FR2), got %q", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf(`expected no "server game config" in display text (FR2), got %q`, body)
	}
}

// TestDeploymentSettingsPortsSection_NoClientSideValidationBlocking is the
// structural half of FR12's "no new validation authority" rule: the Save
// button carries no disabled/x-bind:disabled wiring at all -- portWarning
// (portsAlpineState) only ever renders text via x-text/x-show, so a
// binding the guidance flags as unusual can never be blocked from
// submitting client-side; only a save-time or allocation-time backend
// check may reject it.
func TestDeploymentSettingsPortsSection_NoClientSideValidationBlocking(t *testing.T) {
	body := renderDeploymentSettingsPortsSection(t, DeploymentSettingsPortsData{
		SGCID:        7,
		PortBindings: samplePortBindings(),
		PortContext:  samplePortContext(),
	})
	saveIdx := strings.Index(body, `data-testid="deployment-settings-ports-save"`)
	if saveIdx == -1 {
		t.Fatalf("expected the Save button's stable test hook, got %q", body)
	}
	// Look at the button's own opening tag (back to its preceding "<button").
	tagStart := strings.LastIndex(body[:saveIdx], "<button")
	tagEnd := strings.Index(body[saveIdx:], ">")
	saveTag := body[tagStart : saveIdx+tagEnd]
	if strings.Contains(saveTag, "disabled") {
		t.Errorf("expected the Save button to never be disabled by guidance warnings, got %q", saveTag)
	}
	if strings.Contains(body, "preventDefault") {
		t.Errorf("expected no submit-blocking script (FR12 adds no validation authority), got %q", body)
	}
}

// --- FR10 combined-blade behaviour (the headline test for this task) ---
//
// #2269's Blade dismiss() only reads data-blade-section/data-blade-dirty
// markers already in the DOM (blade_test.go covers that generic mechanism
// itself, mutation-tested); this file's job is to confirm the two real
// section templates -- not synthetic markup -- produce the combined
// marker set FR10 requires for every state the blade can be in. Because
// dismiss() only ever removes the whole blade root after window.confirm
// returns true (or does nothing if the operator cancels), and neither
// section's own markup is ever touched by that decision, a cancelled
// prompt structurally cannot drop the ports section's accumulated
// bindings -- there is no code path between "confirm() returns false" and
// any mutation of this section's DOM.

// combinedDeploymentSettingsBladeMarkup renders #2269's Blade with both
// sections as its children (mirroring gameDeploymentRow's own composition
// in games.templ) and returns the markup portion before the Blade's own
// <script> tag -- scoped the same way blade_test.go's bladeMarkup helper
// is, so assertions can't false-positive on the script's own
// data-blade-dirty="true" selector literal.
func combinedDeploymentSettingsBladeMarkup(t *testing.T, envData DeploymentSettingsData, portsData DeploymentSettingsPortsData) string {
	t.Helper()
	envHTML := renderDeploymentSettingsEnvSection(t, envData)
	portsHTML := renderDeploymentSettingsPortsSection(t, portsData)
	ctx := templ.WithChildren(context.Background(), templ.Raw(envHTML+portsHTML))

	var sb strings.Builder
	data := components.BladeData{ID: "deployment-settings-blade-7", Title: "Deployment Settings", Subtitle: "vanilla on host-01"}
	if err := components.Blade(data).Render(ctx, &sb); err != nil {
		t.Fatalf("Blade render failed: %v", err)
	}
	body := sb.String()
	idx := strings.Index(body, "<script")
	if idx == -1 {
		t.Fatalf("expected a <script> tag in rendered Blade output, got %q", body)
	}
	return body[:idx]
}

// TestDeploymentSettingsBlade_FR10_PortsDirty_NotSaved is case 1: ports
// edited but not saved must make the combined blade's dismissal prompt
// fire (exactly one dirty section, and it is ports).
func TestDeploymentSettingsBlade_FR10_PortsDirty_NotSaved(t *testing.T) {
	envData := DeploymentSettingsData{SGCID: 7, DisplayName: "vanilla on host-01"}
	portsData := DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings(), Dirty: true}
	markup := combinedDeploymentSettingsBladeMarkup(t, envData, portsData)

	if got := strings.Count(markup, `data-blade-dirty="true"`); got != 1 {
		t.Errorf("expected exactly one dirty section marker, got %d in %q", got, markup)
	}
	if !strings.Contains(markup, `data-blade-section="ports" data-blade-dirty="true"`) {
		t.Errorf("expected the ports section marked dirty, got %q", markup)
	}
	if !strings.Contains(markup, `data-blade-section="env" data-blade-dirty="false"`) {
		t.Errorf("expected the env section to stay never-dirty alongside dirty ports, got %q", markup)
	}
}

// TestDeploymentSettingsBlade_FR10_OnlyEnvEdited_NoPrompt is case 2: only
// env edited (per-key commits, WD9) must never make the combined blade
// prompt -- this is the regression guard the split note calls out
// explicitly: promoting either section's dirty state to a blade-level flag
// would make dismissing prompt over the env table too.
func TestDeploymentSettingsBlade_FR10_OnlyEnvEdited_NoPrompt(t *testing.T) {
	envData := DeploymentSettingsData{
		SGCID:       7,
		DisplayName: "vanilla on host-01",
		EnvOverrides: SGCEnvOverridesData{
			HasTemplateKeys: true,
			Layers:          []EnvVarLayer{{Key: "MAX_PLAYERS", TemplateValue: "10", OverrideValue: strP("30"), EffectiveValue: "30"}},
		},
	}
	portsData := DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings()}
	markup := combinedDeploymentSettingsBladeMarkup(t, envData, portsData)

	if strings.Contains(markup, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty section marker anywhere -- env commits per key and accumulates nothing (WD9), got %q", markup)
	}
}

// TestDeploymentSettingsBlade_FR10_PortsSaved_NoPrompt is case 3: ports
// edited then saved (the post-Save fragment renders Dirty=false, per
// handleDeploymentSettingsPortsSave's success branch) must not prompt.
func TestDeploymentSettingsBlade_FR10_PortsSaved_NoPrompt(t *testing.T) {
	envData := DeploymentSettingsData{SGCID: 7, DisplayName: "vanilla on host-01"}
	portsData := DeploymentSettingsPortsData{SGCID: 7, PortBindings: samplePortBindings()} // Dirty: false (zero value)
	markup := combinedDeploymentSettingsBladeMarkup(t, envData, portsData)

	if strings.Contains(markup, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty section marker once ports have been saved, got %q", markup)
	}
}
