package main

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestBuildHead_ThemesCSSLoadsAfterDaisyUILink guards the NFR5 load-order
// trap documented on htmxui.ThemesCSS and on RenderTempl: the daisyUI CDN
// <link> must appear in the rendered head before the <style> tag carrying
// htmxui.ThemesCSS, or the palette override (including the FR6 distinct
// OLED palette) silently loses to daisyUI's defaults with no error. Calls
// buildHead directly (the exact production code RenderTempl uses to
// construct CustomHead), mirroring
// tools/app_registry/ui/templ_render_test.go's equivalent guard (#1005).
func TestBuildHead_ThemesCSSLoadsAfterDaisyUILink(t *testing.T) {
	head := buildHead()

	linkIdx := strings.Index(head, `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui`)
	if linkIdx < 0 {
		t.Fatalf("expected daisyUI <link> in head, got: %s", head)
	}

	styleIdx := strings.Index(head, "<style>"+htmxui.ThemesCSS+"</style>")
	if styleIdx < 0 {
		t.Fatalf("expected htmxui.ThemesCSS wrapped in <style> in head, got: %s", head)
	}

	if !strings.Contains(htmxui.ThemesCSS, `[data-theme="oled"]`) {
		t.Fatalf("expected htmxui.ThemesCSS to contain the FR6 [data-theme=\"oled\"] rule, got: %s", htmxui.ThemesCSS)
	}

	if linkIdx >= styleIdx {
		t.Fatalf("NFR5 violation: daisyUI <link> (index %d) must precede ThemesCSS <style> (index %d) in head", linkIdx, styleIdx)
	}
}

// TestBuildHead_NoLocationReload is the FR5 guard on manmanv2's own
// render-pipeline head markup: the pre-migration themeInit/
// themeManagement() scripts called location.reload() on every theme
// change ("Tailwind CDN needs reload" -- see buildHead's doc comment for
// what replaced it). themeBootstrapScript (the FOUC-avoidance + legacy-key
// migration script buildHead emits) must not reintroduce that call.
// htmxui.ThemeSwitcher's own script is guarded independently by
// htmxui_test.go's TestThemeSwitcher_NoLocationReload, and manmanv2's
// components.Layout output (which mounts ThemeSwitcher plus manmanv2's own
// toastScript) is guarded by components/layout_test.go's
// TestLayout_NoLocationReloadInEmittedScripts -- together the three cover
// every script manmanv2 actually emits.
//
// Red/green (verified by hand): temporarily adding
// `location.reload();` inside themeBootstrapScript's IIFE in
// templ_render.go makes this test fail with "expected no location.reload()
// call"; removing it again restores green.
func TestBuildHead_NoLocationReload(t *testing.T) {
	head := buildHead()
	if strings.Contains(head, "location.reload(") {
		t.Errorf("expected no location.reload() call in buildHead's emitted markup (FR5), got: %s", head)
	}
}

// TestThemeBootstrapScript_MigratesLegacyThemeKeyOnce guards the FR6
// requirement that an operator's previously-saved theme preference (stored
// under the pre-migration 'manman-theme' localStorage key) survives the
// switch onto htmxui.ThemeSwitcher's shared storage key
// (htmxui.ThemeSwitcherStorageKey) instead of silently resetting to the
// default.
func TestThemeBootstrapScript_MigratesLegacyThemeKeyOnce(t *testing.T) {
	head := buildHead()
	if !strings.Contains(head, "LEGACY_KEY = 'manman-theme'") {
		t.Errorf("expected bootstrap script to reference the legacy 'manman-theme' key, got: %s", head)
	}
	wantKeyAssignment := `KEY = "` + htmxui.ThemeSwitcherStorageKey + `"`
	if !strings.Contains(head, wantKeyAssignment) {
		t.Errorf("expected bootstrap script to read/write the shared htmxui.ThemeSwitcherStorageKey %q, got: %s", wantKeyAssignment, head)
	}
}
