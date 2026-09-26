package main

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestBuildHead_ThemesCSSLoadsAfterDaisyUI guards the load-order trap
// documented in htmxui ARCHITECTURE §10: ThemesCSS maps daisyUI's CSS
// variables to krill's palette, so it must load *after* the daisyUI
// stylesheet. Loading it first makes the override silently lose to
// daisyUI's defaults -- no error, just wrong colours.
func TestBuildHead_ThemesCSSLoadsAfterDaisyUI(t *testing.T) {
	head := buildHead()

	daisyUIIdx := strings.Index(head, `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui`)
	themesIdx := strings.Index(head, "<style>"+htmxui.ThemesCSS+"</style>")
	if daisyUIIdx < 0 {
		t.Fatalf("expected the pinned daisyUI stylesheet in head, got: %s", head)
	}
	if themesIdx < 0 {
		t.Fatalf("expected the htmxui ThemesCSS in head, got: %s", head)
	}
	if daisyUIIdx > themesIdx {
		t.Errorf("ThemesCSS (index %d) must load after the daisyUI stylesheet (index %d), or the palette override silently loses",
			themesIdx, daisyUIIdx)
	}
}

// TestBuildHead_PinsTheSameVersionsAsTheRestOfTheRepo pins the CDN
// versions so one app drifting off them is visible here rather than as a
// visual difference nobody attributes to a version bump.
func TestBuildHead_PinsTheSameVersionsAsTheRestOfTheRepo(t *testing.T) {
	head := buildHead()
	for _, want := range []string{
		"@tailwindcss/browser@4.3.3",
		"daisyui@5.6.18",
	} {
		if !strings.Contains(head, want) {
			t.Errorf("expected pinned %s in head, got: %s", want, head)
		}
	}
}

// TestBuildHead_ThemeBootstrapReadsTheSharedStorageKey guards that the
// no-FOUC bootstrap reads the same localStorage key htmxui's
// ThemeSwitcher writes, or the operator's chosen theme is overwritten on
// the next page load.
func TestBuildHead_ThemeBootstrapReadsTheSharedStorageKey(t *testing.T) {
	head := buildHead()
	if !strings.Contains(head, `"`+htmxui.ThemeSwitcherStorageKey+`"`) {
		t.Errorf("expected the bootstrap to read htmxui.ThemeSwitcherStorageKey (%q), got: %s",
			htmxui.ThemeSwitcherStorageKey, head)
	}
	// The regression htmxui guards against in its own test: a theme
	// switch must not need a full reload to take effect.
	if strings.Contains(head, "location.reload") {
		t.Errorf("the theme bootstrap must not force a reload, got: %s", head)
	}
}
