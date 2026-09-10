package pages

import (
	"context"
	"strings"
	"testing"
)

// Guards task #2276's Config Editor blade markup contract (FR13, the
// Config-Editor half of FR10, NFR5, WD4, WD10) and task #2363's Volumes
// tab (FR14, FR15): these tests pin what #2269's Blade + BladeTabs render
// for this section, independent of the handler wiring covered by
// handlers_config_editor_test.go / handlers_config_editor_volumes_test.go.

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
		Volumes:       []ConfigEditorVolume{{VolumeID: 9, Name: "world", Description: "World data", ContainerPath: "/data", HostSubpath: "world", ReadOnly: true, VolumeType: "bind"}},
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

// TestConfigEditor_Volumes_AllSixFieldsRender guards WD4's still-live half:
// all six GameConfigVolume fields render for each volume row, plus the
// backup link-out (NFR4, #2363: the M6 read-only note itself is gone --
// see TestConfigEditor_Volumes_AssignControlWhenUnassigned below for what
// replaced it).
func TestConfigEditor_Volumes_AllSixFieldsRender(t *testing.T) {
	body := renderConfigEditor(t, baseConfigEditorData())

	for _, want := range []string{"world", "World data", "/data", "bind"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected volume field %q to render, got %q", want, body)
		}
	}
	if !strings.Contains(body, "Read-only") {
		t.Errorf("expected the read-only flag to render, got %q", body)
	}
	if strings.Contains(body, "editable here in M6") {
		t.Errorf("expected the stale M5/M6 note to be gone (#2363), got %q", body)
	}
	if !strings.Contains(body, `href="/games/1/configs/3"`) {
		t.Errorf("expected the backup link-out to BackupLinkURL, got %q", body)
	}
	if !strings.Contains(body, "Manage backups") {
		t.Errorf("expected the backup link-out label, got %q", body)
	}
}

// TestConfigEditor_Volumes_AssignControlWhenUnassigned guards FR14: a
// volume with no Backup assignment renders an inline Assign control
// (targeting the .../backup-config/assign route), not Edit/Remove.
func TestConfigEditor_Volumes_AssignControlWhenUnassigned(t *testing.T) {
	data := baseConfigEditorData()
	data.Volumes[0].Backup = nil
	body := renderConfigEditor(t, data)

	if !strings.Contains(body, "data-volume-backup-assign-form") {
		t.Errorf("expected an assign form for an unassigned volume, got %q", body)
	}
	if !strings.Contains(body, `/games/1/configs/3/volumes/9/backup-config/assign`) {
		t.Errorf("expected the assign form to post to the volume's assign route, got %q", body)
	}
	if strings.Contains(body, "data-volume-backup-edit-form") || strings.Contains(body, "data-volume-backup-remove-form") {
		t.Errorf("expected no edit/remove controls for an unassigned volume, got %q", body)
	}
}

// TestConfigEditor_Volumes_EditAndRemoveControlsWhenAssigned guards
// FR14/FR15: a volume with a Backup assignment renders inline Edit and
// Remove controls, seeded with the current assignment's values, not the
// Assign control.
func TestConfigEditor_Volumes_EditAndRemoveControlsWhenAssigned(t *testing.T) {
	data := baseConfigEditorData()
	data.Volumes[0].Backup = &ConfigEditorVolumeBackup{
		BackupConfigID: 42,
		CadenceMinutes: 1440,
		BackupPath:     "saves",
		Enabled:        true,
	}
	body := renderConfigEditor(t, data)

	if !strings.Contains(body, "data-volume-backup-edit-form") {
		t.Errorf("expected an edit form for an assigned volume, got %q", body)
	}
	if !strings.Contains(body, `/games/1/configs/3/volumes/9/backup-config/edit`) {
		t.Errorf("expected the edit form to post to the volume's edit route, got %q", body)
	}
	if !strings.Contains(body, `value="saves"`) {
		t.Errorf("expected the current backup path to seed the edit form, got %q", body)
	}
	if !strings.Contains(body, `value="42"`) {
		t.Errorf("expected the backup_config_id to be carried in the edit form, got %q", body)
	}
	if !strings.Contains(body, "data-volume-backup-remove-form") {
		t.Errorf("expected a remove form for an assigned volume, got %q", body)
	}
	if !strings.Contains(body, `/games/1/configs/3/volumes/9/backup-config/remove`) {
		t.Errorf("expected the remove form to post to the volume's remove route, got %q", body)
	}
	if strings.Contains(body, "data-volume-backup-assign-form") {
		t.Errorf("expected no assign control for an already-assigned volume, got %q", body)
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
// "server game config" (case-insensitive) anywhere in the rendered blade.
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
