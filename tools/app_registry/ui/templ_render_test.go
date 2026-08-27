package main

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestRenderTempl_ThemesCSSLoadsAfterDaisyUILink guards the NFR5 load-order
// trap documented on htmxui.ThemesCSS and on RenderTempl: the daisyUI CDN
// <link> must appear in the rendered head before the <style> tag carrying
// htmxui.ThemesCSS, or the palette override silently loses to daisyUI's
// defaults with no error. Calls buildHead directly (the exact production
// code RenderTempl uses to construct CustomHead), rather than a full
// htmxbase.Render + templ.Component round trip.
func TestRenderTempl_ThemesCSSLoadsAfterDaisyUILink(t *testing.T) {
	head := buildHead()

	linkIdx := strings.Index(head, `<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui`)
	if linkIdx < 0 {
		t.Fatalf("expected daisyUI <link> in head, got: %s", head)
	}

	styleIdx := strings.Index(head, "<style>"+htmxui.ThemesCSS+"</style>")
	if styleIdx < 0 {
		t.Fatalf("expected htmxui.ThemesCSS wrapped in <style> in head, got: %s", head)
	}

	if !strings.Contains(htmxui.ThemesCSS, `[data-theme="light"]`) {
		t.Fatalf("expected htmxui.ThemesCSS to contain a [data-theme=...] rule, got: %s", htmxui.ThemesCSS)
	}

	if linkIdx >= styleIdx {
		t.Fatalf("NFR5 violation: daisyUI <link> (index %d) must precede ThemesCSS <style> (index %d) in head", linkIdx, styleIdx)
	}
}

// TestRenderTempl_SSEExtensionScript_FR20a tests FR20(a): the rendered <head>
// contains the pinned htmx SSE extension script (htmx.org@1.9.10/dist/ext/sse.js)
// AFTER the htmx core script, matching the required load order.
// Failure: htmx 2.x SSE extension is incompatible with 1.9.10 core; the failure is silent.
func TestRenderTempl_SSEExtensionScript_FR20a(t *testing.T) {
	head := buildHead()

	// FR20(a): Must contain the pinned SSE extension URL
	expectedSSEExtURL := "https://cdn.jsdelivr.net/npm/htmx.org@1.9.10/dist/ext/sse.js"
	if !strings.Contains(head, expectedSSEExtURL) {
		t.Fatalf("FR20(a) violation: expected SSE extension script %q in head, got: %s", expectedSSEExtURL, head)
	}

	// FR20(a): Must be after the htmx core script for proper extension loading
	coreScriptIdx := strings.Index(head, "https://cdn.jsdelivr.net/npm/htmx.org")
	if coreScriptIdx < 0 {
		t.Fatalf("expected htmx core script URL in head, got: %s", head)
	}

	// The SSE extension should appear after the core htmx script for load order
	sseExtIdx := strings.Index(head, expectedSSEExtURL)
	if coreScriptIdx > sseExtIdx {
		t.Fatalf("FR20(a) violation: htmx core must appear BEFORE SSE extension (got core at %d, SSE at %d)", coreScriptIdx, sseExtIdx)
	}
}
