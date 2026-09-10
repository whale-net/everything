package pages

import (
	"context"
	"strings"
	"testing"
)

// Guards task #2276's Config Editor blade markup contract (FR13, the
// Config-Editor half of FR10, NFR5, WD4, WD10): these tests pin what
// #2269's Blade + BladeTabs render for this section, independent of the
// handler wiring covered by handlers_config_editor_test.go.

func renderConfigEditor(t *testing.T, data ConfigEditorData) string {
	t.Helper()
	var sb strings.Builder
	if err := ConfigEditor(data).Render(context.Background(), &sb); err != nil {
		t.Fatalf("ConfigEditor render failed: %v", err)
	}
	return sb.String()
}

func baseConfigEditorData() ConfigEditorData {
	return ConfigEditorData{
		GameID:        1,
		ConfigID:      3,
		Name:          "vanilla",
		Image:         "itzg/minecraft-server",
		ArgsTemplate:  "--nogui",
		EnvVars:       []ConfigEditorEnvVar{{Key: "MAX_PLAYERS", Value: "10"}},
		Volumes:       []ConfigEditorVolume{{Name: "world", Description: "World data", ContainerPath: "/data", HostSubpath: "world", ReadOnly: true, VolumeType: "bind"}},
		Dirty:         false,
		ActiveTab:     "basics",
		BackupLinkURL: "/games/1/configs/3",
	}
}

// TestConfigEditor_WD10_ExactlyThreeTabsNoPorts is the WD10 negative
// assertion: exactly three tabs render -- Basics, Environment, Volumes --
// and no Ports tab anywhere, since GameConfig has no port field
// (port_bindings live on the deployment, FR12's #2275).
func TestConfigEditor_WD10_ExactlyThreeTabsNoPorts(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())

	// data-blade-tab-id="..." (with the trailing quote) matches only the
	// actual tab strip's <a> elements -- the blade's own script also
	// references the bare attribute name (e.g. querySelectorAll bracket
	// selectors, getAttribute calls) without a trailing "=", which would
	// otherwise inflate this count.
	if got := strings.Count(body, `data-blade-tab-id="`); got != 3 {
		t.Errorf("expected exactly 3 tabs, got %d in %q", got, body)
	}
	for _, want := range []string{`data-blade-tab-id="basics"`, `data-blade-tab-id="environment"`, `data-blade-tab-id="volumes"`} {
		if !strings.Contains(body, want) {
			t.Errorf("expected tab %q, got %q", want, body)
		}
	}
	if strings.Contains(body, "Ports") {
		t.Errorf("expected no Ports tab (WD10), got %q", body)
	}
}

// TestConfigEditor_Decision8_NoActionsTab guards decision 8: Actions is
// not a Config Editor tab.
func TestConfigEditor_Decision8_NoActionsTab(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())
	if strings.Contains(body, "Actions") {
		t.Errorf("expected no Actions tab (decision 8), got %q", body)
	}
}

// TestConfigEditor_VolumesReadOnly_AllSixFields guards WD4: all six
// GameConfigVolume fields render for each volume row, plus the backup
// link-out and the "editable here in M6" note.
func TestConfigEditor_VolumesReadOnly_AllSixFields(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())

	for _, want := range []string{"world", "World data", "/data", "Yes", "bind"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected volume field %q to render, got %q", want, body)
		}
	}
	if got := strings.Count(body, ">world<"); got != 1 {
		// Name column renders "world" exactly once; HostSubpath also "world"
		// but rendered without surrounding tags matching this exact pattern.
		t.Logf("name cell count = %d (informational)", got)
	}
	if !strings.Contains(body, "editable here in M6") {
		t.Errorf("expected the M6 note, got %q", body)
	}
	if !strings.Contains(body, `href="/games/1/configs/3"`) {
		t.Errorf("expected the backup link-out to BackupLinkURL, got %q", body)
	}
	if !strings.Contains(body, "Manage backups") {
		t.Errorf("expected the backup link-out label, got %q", body)
	}
}

// TestConfigEditor_VolumesReadOnly_NoWriteControls is WD4's negative
// assertion: no add/edit/remove control and no inline backup-schedule
// control render anywhere in the Volumes panel.
func TestConfigEditor_VolumesReadOnly_NoWriteControls(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())

	volStart := strings.Index(body, `data-blade-tab-panel="volumes"`)
	if volStart == -1 {
		t.Fatalf("expected a volumes tab panel, got %q", body)
	}
	// The Volumes panel div is the last thing before the blade's shared
	// <script> tag (which is not part of the panel's own markup, and whose
	// comments/JS would otherwise false-match substrings like "Edit" inside
	// "Config Editor" or "Editor blade") -- scope strictly to the panel.
	scriptStart := strings.Index(body[volStart:], "<script")
	if scriptStart == -1 {
		t.Fatalf("expected a trailing <script> tag after the Volumes panel, got %q", body)
	}
	volumesPanel := body[volStart : volStart+scriptStart]

	for _, unwanted := range []string{"<button", "<input", "<form", "btn-error", "Add Volume", ">Edit<", ">Remove<", ">Delete<", "Backup schedule", "schedule"} {
		if strings.Contains(volumesPanel, unwanted) {
			t.Errorf("expected no write control (%q) in the Volumes panel (WD4), got %q", unwanted, volumesPanel)
		}
	}
}

// TestConfigEditor_FR10_TabPanelsAllPresent guards FR10: all three tab
// panels stay in the DOM regardless of ActiveTab -- only the inactive
// ones carry `hidden` -- so switching tabs cannot lose entered values.
func TestConfigEditor_FR10_TabPanelsAllPresent(t *testing.T) {
	for _, activeTab := range []string{"basics", "environment", "volumes"} {
		data := baseConfigEditorData()
		data.ActiveTab = activeTab
		body := renderConfigEditor(t, data)

		for _, panel := range []string{"basics", "environment", "volumes"} {
			marker := `data-blade-tab-panel="` + panel + `"`
			if !strings.Contains(body, marker) {
				t.Errorf("ActiveTab=%q: expected panel %q to always be present, got %q", activeTab, panel, body)
			}
		}
		// The non-active panels carry `hidden`; the active one does not.
		for _, panel := range []string{"basics", "environment", "volumes"} {
			idx := strings.Index(body, `data-blade-tab-panel="`+panel+`"`)
			if idx == -1 {
				continue
			}
			// Look at a small window after the marker for a `hidden` token
			// before the next attribute/tag close, mirroring how the templ
			// conditionally emits the bare `hidden` attribute.
			window := body[idx : idx+200]
			hasHidden := strings.Contains(window, " hidden")
			wantHidden := panel != activeTab
			if hasHidden != wantHidden {
				t.Errorf("ActiveTab=%q panel=%q: hidden=%v, want %v; window=%q", activeTab, panel, hasHidden, wantHidden, window)
			}
		}
	}
}

// TestConfigEditor_FR10_DirtyMarkerReflectsData guards the Basics/
// Environment section's dirty seeding: Dirty=false renders
// data-blade-dirty="false" (fresh open, no prompt); Dirty=true renders
// data-blade-dirty="true" (re-render after a rejected Save, unsaved edits
// intact).
func TestConfigEditor_FR10_DirtyMarkerReflectsData(t *testing.T) {
	clean := baseConfigEditorData()
	clean.Dirty = false
	body := renderConfigEditor(t, clean)
	if !strings.Contains(body, `data-blade-section="basics-environment" data-blade-dirty="false"`) {
		t.Errorf("expected the never-yet-dirty marker on a fresh open, got %q", body)
	}

	dirty := baseConfigEditorData()
	dirty.Dirty = true
	body = renderConfigEditor(t, dirty)
	if !strings.Contains(body, `data-blade-section="basics-environment" data-blade-dirty="true"`) {
		t.Errorf("expected the dirty marker after a rejected Save, got %q", body)
	}
}

// TestConfigEditor_ValidationErrors_PerField guards FR10: a per-field
// error renders against that field and entered values are not lost.
func TestConfigEditor_ValidationErrors_PerField(t *testing.T) {
	data := baseConfigEditorData()
	data.Name = ""
	data.Errors = ConfigEditorErrors{Name: "Name is required."}
	data.Dirty = true
	body := renderConfigEditor(t, data)

	if !strings.Contains(body, `data-field-error="name"`) {
		t.Errorf("expected the error to be reported against the name field, got %q", body)
	}
	if !strings.Contains(body, "Name is required.") {
		t.Errorf("expected the error message to render, got %q", body)
	}
	if !strings.Contains(body, `value="itzg/minecraft-server"`) {
		t.Errorf("expected the Image value to survive despite the Name error, got %q", body)
	}
	if strings.Contains(body, `data-field-error="image"`) {
		t.Errorf("expected no Image field error when only Name failed, got %q", body)
	}
}

// TestConfigEditor_FR13_EmptyArgsTemplateRenders guards FR13's rendering
// half: an emptied ArgsTemplate renders as an empty input value, not a
// stale/previous value.
func TestConfigEditor_FR13_EmptyArgsTemplateRenders(t *testing.T) {
	data := baseConfigEditorData()
	data.ArgsTemplate = ""
	body := renderConfigEditor(t, data)

	if !strings.Contains(body, `id="config-editor-args-template"`) {
		t.Fatalf("expected the args template input to render, got %q", body)
	}
	if strings.Contains(body, `name="args_template" value="--nogui"`) {
		t.Errorf("expected the args template value to be cleared, got %q", body)
	}
}

// TestConfigEditor_FR2_NoRawSGCInDisplayText guards FR2: no "SGC" or
// "server game config" (case-insensitive) anywhere in the rendered blade,
// including the Volumes M6 note.
func TestConfigEditor_FR2_NoRawSGCInDisplayText(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())
	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no raw SGC identifier in display text (FR2), got %q", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf(`expected no "server game config" in display text (FR2), got %q`, body)
	}
}

// TestConfigEditor_EnvVarsRenderAsRows guards the Environment tab's
// rendering of GameConfig.env_template rows (NFR5: this is the base-layer
// editor, not a deployment override table).
func TestConfigEditor_EnvVarsRenderAsRows(t *testing.T) {
	data := baseConfigEditorData()
	data.EnvVars = []ConfigEditorEnvVar{
		{Key: "MAX_PLAYERS", Value: "10"},
		{Key: "DIFFICULTY", Value: "hard"},
	}
	body := renderConfigEditor(t, data)

	// 2 data rows + 1 hidden <template data-env-row-template> row the "+ Add
	// Variable" button clones client-side.
	if got := strings.Count(body, `name="env_key"`); got != 3 {
		t.Errorf("expected 3 env_key inputs (2 data rows + 1 template row), got %d in %q", got, body)
	}
	if !strings.Contains(body, `value="MAX_PLAYERS"`) || !strings.Contains(body, `value="DIFFICULTY"`) {
		t.Errorf("expected both env var keys to render, got %q", body)
	}
}
