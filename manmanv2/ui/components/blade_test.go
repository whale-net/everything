package components

import (
	"context"
	"strings"
	"testing"

	"github.com/a-h/templ"
)

// renderBlade renders Blade(data) with the given children content (as raw
// HTML, so tests can assert on exact placement) and returns the full
// rendered output, matching the render(t, ...) helper convention used by
// layout_test.go/htmxui_test.go for this package's other templ components.
func renderBlade(t *testing.T, data BladeData, childrenHTML string) string {
	t.Helper()
	ctx := context.Background()
	if childrenHTML != "" {
		ctx = templ.WithChildren(ctx, templ.Raw(childrenHTML))
	}
	var buf strings.Builder
	if err := Blade(data).Render(ctx, &buf); err != nil {
		t.Fatalf("Blade render failed: %v", err)
	}
	return buf.String()
}

// bladeMarkup returns the portion of a rendered Blade before its <script>
// tag -- i.e. just the DOM the runtime dismiss() handler would query at
// dismissal time. The script body's own querySelector literals (e.g.
// '[data-blade-dirty="true"]') contain the same attribute strings tests in
// this file assert on, so markup-presence assertions must be scoped here
// rather than to the full rendered output, or they would false-positive on
// the script's source text rather than actual section markup.
func bladeMarkup(t *testing.T, body string) string {
	t.Helper()
	idx := strings.Index(body, "<script")
	if idx == -1 {
		t.Fatalf("expected a <script> tag in rendered Blade output, got %q", body)
	}
	return body[:idx]
}

// TestBlade_ScrimAriaTitleSubtitleAndChildren is the direct structural guard
// listed in #2269's Testing section: scrim element present; aria attributes
// for a dialog layer; title/subtitle rendered; children rendered inside the
// layer.
func TestBlade_ScrimAriaTitleSubtitleAndChildren(t *testing.T) {
	body := renderBlade(t, BladeData{
		ID:       "deployment-settings-blade",
		Title:    "Deployment Settings",
		Subtitle: "vanilla on host-01",
	}, `<div data-testid="blade-child">CHILD-CONTENT</div>`)
	// Scoped to the DOM markup only (see bladeMarkup's doc comment): the
	// script body's own querySelector literals repeat several of these same
	// attribute strings (e.g. '[data-blade-scrim]', '[data-blade-panel]'),
	// so an unscoped strings.Contains(body, ...) would still pass even if
	// the actual element markup below dropped the attribute -- it would
	// just be matching the script's source text instead.
	markup := bladeMarkup(t, body)

	if !strings.Contains(markup, `data-blade-scrim`) {
		t.Errorf("expected a scrim element (data-blade-scrim), got %q", markup)
	}
	if !strings.Contains(markup, `role="dialog"`) {
		t.Errorf(`expected role="dialog" on the blade panel, got %q`, markup)
	}
	if !strings.Contains(markup, `aria-modal="true"`) {
		t.Errorf(`expected aria-modal="true" on the blade panel, got %q`, markup)
	}
	if !strings.Contains(markup, `aria-labelledby="deployment-settings-blade-title"`) {
		t.Errorf("expected aria-labelledby pointing at the title id, got %q", markup)
	}
	if !strings.Contains(markup, `aria-describedby="deployment-settings-blade-subtitle"`) {
		t.Errorf("expected aria-describedby pointing at the subtitle id when Subtitle is set, got %q", markup)
	}
	if !strings.Contains(markup, `id="deployment-settings-blade-title"`) || !strings.Contains(markup, "Deployment Settings") {
		t.Errorf("expected the title to render with its id, got %q", markup)
	}
	if !strings.Contains(markup, `id="deployment-settings-blade-subtitle"`) || !strings.Contains(markup, "vanilla on host-01") {
		t.Errorf("expected the subtitle to render with its id, got %q", markup)
	}

	childIdx := strings.Index(markup, `data-testid="blade-child"`)
	panelIdx := strings.Index(markup, `data-blade-panel`)
	if childIdx == -1 {
		t.Fatalf("expected children to render inside the blade, got %q", markup)
	}
	if panelIdx == -1 || childIdx < panelIdx {
		t.Errorf("expected children to render inside the panel (after data-blade-panel), got %q", markup)
	}
}

// TestBlade_NoSubtitleOmitsSubtitleAndDescribedby covers the conditional
// branch: a Blade with no Subtitle must not render an empty subtitle
// element or a dangling aria-describedby reference to a non-existent id.
func TestBlade_NoSubtitleOmitsSubtitleAndDescribedby(t *testing.T) {
	body := renderBlade(t, BladeData{ID: "b1", Title: "Title Only"}, "")
	markup := bladeMarkup(t, body)
	if strings.Contains(markup, "aria-describedby") {
		t.Errorf("expected no aria-describedby when Subtitle is empty, got %q", markup)
	}
	if strings.Contains(markup, "b1-subtitle") {
		t.Errorf("expected no subtitle element when Subtitle is empty, got %q", markup)
	}
}

// TestBlade_FR2_NoRawSGCInDisplayText is the FR2 assertion from #2269's
// Testing section: rendered output (title/subtitle plus the script body)
// must contain no "SGC" and no "server game config" (case-insensitive) --
// this component only ever speaks Game / Game Config / Deployment.
func TestBlade_FR2_NoRawSGCInDisplayText(t *testing.T) {
	body := renderBlade(t, BladeData{
		ID:       "config-editor-blade",
		Title:    "Game Config",
		Subtitle: "vanilla on host-01",
	}, `<div>Deployment Settings body</div>`)

	lower := strings.ToLower(body)
	if strings.Contains(lower, "sgc") {
		t.Errorf("expected no raw SGC identifier anywhere in rendered output (FR2), got %q", body)
	}
	if strings.Contains(lower, "server game config") {
		t.Errorf(`expected no "server game config" anywhere in rendered output (FR2), got %q`, body)
	}
}

// TestBladeTabs_RendersAllTabsMarksActiveAndPreservesLabels covers
// BladeTabs' contract: renders all tabs, marks exactly the active one, and
// tab labels come through verbatim.
func TestBladeTabs_RendersAllTabsMarksActiveAndPreservesLabels(t *testing.T) {
	tabs := []BladeTab{
		{ID: "general", Label: "General"},
		{ID: "ports", Label: "Ports"},
		{ID: "env", Label: "Environment"},
	}

	var buf strings.Builder
	if err := BladeTabs(tabs, "ports").Render(context.Background(), &buf); err != nil {
		t.Fatalf("BladeTabs render failed: %v", err)
	}
	body := buf.String()

	for _, tab := range tabs {
		if !strings.Contains(body, tab.Label) {
			t.Errorf("expected tab label %q to render verbatim, got %q", tab.Label, body)
		}
		if got := strings.Count(body, `data-blade-tab-id="`+tab.ID+`"`); got != 1 {
			t.Errorf("expected exactly one tab element for id %q, got %d in %q", tab.ID, got, body)
		}
	}

	if got := strings.Count(body, `aria-selected="true"`); got != 1 {
		t.Errorf("expected exactly one active tab (aria-selected=true), got %d in %q", got, body)
	}
	if !strings.Contains(body, `data-blade-tab-id="ports" aria-selected="true"`) {
		t.Errorf("expected the ports tab to be the one marked active, got %q", body)
	}
	if got := strings.Count(body, `aria-selected="false"`); got != 2 {
		t.Errorf("expected the other two tabs marked aria-selected=false, got %d in %q", got, body)
	}
}

// TestBladeTabs_NoMatchingActiveIDActivatesNoTab covers the case where
// activeID does not match any BladeTab.ID: no tab should render as active.
func TestBladeTabs_NoMatchingActiveIDActivatesNoTab(t *testing.T) {
	tabs := []BladeTab{
		{ID: "general", Label: "General"},
		{ID: "ports", Label: "Ports"},
	}
	var buf strings.Builder
	if err := BladeTabs(tabs, "does-not-exist").Render(context.Background(), &buf); err != nil {
		t.Fatalf("BladeTabs render failed: %v", err)
	}
	body := buf.String()
	if strings.Contains(body, `aria-selected="true"`) {
		t.Errorf("expected no active tab when activeID matches nothing, got %q", body)
	}
	if got := strings.Count(body, `aria-selected="false"`); got != 2 {
		t.Errorf("expected both tabs marked inactive, got %d in %q", got, body)
	}
}

// --- FR10 dirty-tracking: the dismissal decision -------------------------
//
// The actual gate (read dirtySections().length, prompt via window.confirm,
// only proceed with dismissal on confirm) is vanilla JS that runs at
// dismissal time against live DOM state (see blade.templ's script body) --
// this repo's Bazel Go test toolchain has no headless browser or JS
// runtime, so it cannot execute that script directly. These tests instead
// pin the two things that *are* verifiable from Go:
//
//  1. The markup contract the script depends on: a section's
//     data-blade-section/data-blade-dirty attributes pass through Blade's
//     children slot unchanged and land inside the [data-blade] root the
//     script scopes its querySelectorAll to (root.querySelectorAll(
//     '[data-blade-section][data-blade-dirty="true"]') in blade.templ) --
//     so the three representative DOM states below are exactly what the
//     runtime dismiss() handler would observe for each of the task's three
//     cases.
//  2. The dismiss() function's own conditional structure: it only reaches
//     window.confirm(...) when dirtySections().length > 0, and only removes
//     the blade (root.remove()) after that gate -- i.e. a clean blade skips
//     the prompt entirely and a dirty blade cannot be dismissed without it
//     coming back false-if-cancelled.
//
// Behavior was additionally verified by hand in a browser: with a section
// carrying data-blade-section data-blade-dirty="true", clicking the scrim
// (or pressing Escape) raised the confirm() prompt and cancelling left the
// blade open (dirtySections().length stayed 1, root never removed);
// confirming removed the blade. Setting data-blade-dirty="false" (the
// post-Save state) before dismissing skipped the prompt entirely and
// removed the blade immediately -- matching the three cases below.

// TestBlade_DirtyTracking_NoDirtySectionRendersNoDirtyMarker is case 1: a
// blade with no accumulating section (or one that never went dirty, e.g.
// FR11's per-key env table) must render no data-blade-dirty="true" anywhere
// -- so dirtySections() at dismissal time is empty and dismiss() never
// reaches window.confirm(...).
func TestBlade_DirtyTracking_NoDirtySectionRendersNoDirtyMarker(t *testing.T) {
	body := renderBlade(t, BladeData{ID: "b1", Title: "Deployment Settings"},
		`<div data-blade-section="env" data-blade-dirty="false">env table</div>`)
	markup := bladeMarkup(t, body)

	if strings.Contains(markup, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty section marker, got %q", markup)
	}
	if !strings.Contains(markup, `data-blade-section="env" data-blade-dirty="false"`) {
		t.Errorf("expected the clean section's markers to pass through unchanged, got %q", markup)
	}
}

// TestBlade_DirtyTracking_OneDirtyAccumulatingSection is case 2: one dirty
// accumulating section (e.g. FR12's ports editor after a first edit) must
// render its data-blade-section/data-blade-dirty="true" pair inside the
// panel the script's root.querySelectorAll('[data-blade-section]
// [data-blade-dirty="true"]') scans -- so dirtySections().length is 1 and
// dismiss() reaches the window.confirm(...) gate.
func TestBlade_DirtyTracking_OneDirtyAccumulatingSection(t *testing.T) {
	body := renderBlade(t, BladeData{ID: "b1", Title: "Deployment Settings"},
		`<div data-blade-section="ports" data-blade-dirty="true">ports editor</div>`)
	markup := bladeMarkup(t, body)

	if got := strings.Count(markup, `data-blade-dirty="true"`); got != 1 {
		t.Errorf("expected exactly one dirty section marker, got %d in %q", got, markup)
	}
	if !strings.Contains(markup, `data-blade-section="ports" data-blade-dirty="true"`) {
		t.Errorf("expected the ports section marked dirty, got %q", markup)
	}

	// Pin the gate itself: dismiss() must only call window.confirm(...)
	// inside the dirtySections().length > 0 branch, so a dirty section is
	// what makes that call reachable.
	if !strings.Contains(body, "if (dirtySections().length > 0)") {
		t.Fatalf("expected dismiss()'s dirty-count gate to guard window.confirm, got %q", body)
	}
	gateIdx := strings.Index(body, "if (dirtySections().length > 0)")
	confirmIdx := strings.Index(body, "window.confirm(")
	closeBraceIdx := strings.Index(body[gateIdx:], "}")
	if confirmIdx == -1 || closeBraceIdx == -1 || confirmIdx > gateIdx+closeBraceIdx {
		t.Errorf("expected window.confirm(...) to sit inside the dirty-count gate, got %q", body)
	}
}

// TestBlade_DirtyTracking_DirtySectionSavedThenDismissed is case 3: a
// section that accumulated edits and was then saved (clearing its
// data-blade-dirty flag back to "false", per Blade's doc comment on how a
// section owns its own attribute) must not leave any
// data-blade-dirty="true" marker behind, matching case 1's post-save DOM
// shape -- so a subsequent dismissal skips the prompt.
func TestBlade_DirtyTracking_DirtySectionSavedThenDismissed(t *testing.T) {
	body := renderBlade(t, BladeData{ID: "b1", Title: "Deployment Settings"},
		`<div data-blade-section="ports" data-blade-dirty="false">ports editor, saved</div>`)
	markup := bladeMarkup(t, body)

	if strings.Contains(markup, `data-blade-dirty="true"`) {
		t.Errorf("expected no dirty marker after a section clears itself on Save, got %q", markup)
	}
	if !strings.Contains(markup, `data-blade-section="ports" data-blade-dirty="false"`) {
		t.Errorf("expected the saved section's marker to read clean, got %q", markup)
	}
}
