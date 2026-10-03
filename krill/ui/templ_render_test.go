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

// TestBuildHead_RelativeAgeBindsItsListenerWhereItRuns guards the load-time
// trap in relativeAgeScript: it is emitted into the HEAD, where <body> does
// not exist yet, so a document.body guard evaluates false and binds nothing.
//
// The consequence is silent and specific: the DOMContentLoaded upgrade runs,
// so the first paint says "Updated 3 seconds ago"; but the after:swap listener
// never binds, so every later in-place swap -- a filter change on the Tasks
// page, a Refresh -- leaves the element showing the absolute instant the
// server rendered. Nothing anywhere reports an error.
//
// Asserting the binding is on `document` is what makes this a test rather
// than a substring check: the line could be deleted outright and a naive
// "does the script mention afterSwap" assertion would stay green.
func TestBuildHead_RelativeAgeBindsItsListenerWhereItRuns(t *testing.T) {
	head := buildHead()

	const listen = "addEventListener('htmx:after:swap'"
	if !strings.Contains(head, listen) {
		t.Fatalf("the relative-age script no longer listens for htmx:after:swap at all, got: %s", head)
	}
	if strings.Contains(head, "document.body&&document.body."+listen) {
		t.Errorf("the after:swap listener is guarded on document.body, which does not exist "+
			"when a head script runs -- it can never bind, and every in-place swap falls back "+
			"to the absolute instant: %s", head)
	}
}

// TestHeadScripts_ListensForHtmx4SwapEvents pins the event NAME, which the
// attachment checks above cannot reach: a listener bound to an event htmx
// never dispatches is attached, unguarded, on document -- every invariant
// they assert holds, and the upgrade still never runs. htmx 4.0.0 (the build
// htmxbase serves) renamed its lifecycle events to the colon form, so
// `htmx:afterSwap` binds nothing at all and the first in-place swap reverts
// every lease to its RFC3339 instant and "Updated N ago" to the same.
//
// The shape follows libs/go/htmxsse/liveindicator's
// TestLiveIndicator_ListensForHtmx4SSEEvents: the good name present AND the
// 1.x name absent. The negative half is the load-bearing one -- presence
// alone would still pass against a script carrying both spellings.
func TestHeadScripts_ListensForHtmx4SwapEvents(t *testing.T) {
	const htmx4Event = "addEventListener('htmx:after:swap'"
	const htmx1Event = "addEventListener('htmx:afterSwap'"

	for name, script := range map[string]string{
		"leaseCountdownScript": leaseCountdownScript,
		"relativeAgeScript":    relativeAgeScript,
	} {
		if !strings.Contains(script, htmx4Event) {
			t.Errorf("%s no longer listens for htmx:after:swap, so a swap re-runs no upgrade, got: %s",
				name, script)
		}
		if strings.Contains(script, htmx1Event) {
			t.Errorf("%s still binds htmx:afterSwap: htmx 1.x event names no longer fire, got: %s",
				name, script)
		}
	}

	// Head-wide, so a camelCase binding added by a third head script is
	// caught here rather than only by whichever test owns that script.
	head := buildHead()
	if strings.Contains(head, "htmx:afterSwap") {
		t.Errorf("the head binds a 1.x camelCase htmx event: htmx 1.x event names no longer fire, got: %s", head)
	}
	if !strings.Contains(head, htmx4Event) {
		t.Errorf("the head never listens for htmx:after:swap, so no swap re-runs a head upgrade, got: %s", head)
	}
}
