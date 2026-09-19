package main

import (
	"strings"
	"testing"

	"github.com/whale-net/everything/libs/go/htmxui"
)

// TestBuildHead_ThemesCSSLoadsAfterDaisyUILink guards the load-order trap
// documented on htmxui.ThemesCSS and on RenderTempl: the daisyUI CDN
// <link> must appear in the rendered head before the <style> tag carrying
// htmxui.ThemesCSS, or the palette override silently loses to daisyUI's
// defaults with no error. Calls buildHead directly (the exact production
// code RenderTempl uses to construct CustomHead), mirroring
// manmanv2/ui/templ_render_test.go's and
// tools/app_registry/ui/templ_render_test.go's equivalent guard.
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

	if linkIdx >= styleIdx {
		t.Fatalf("load-order violation: daisyUI <link> (index %d) must precede ThemesCSS <style> (index %d) in head", linkIdx, styleIdx)
	}
}

// TestBuildHead_HTMXSSEExtensionScript guards the htmx SSE extension
// script buildHead must emit: session.templ's hx-ext="sse" (and its
// sse-connect/sse-swap attributes) silently no-op without this script
// loaded, so the session detail page never opens a live connection even
// though the backend correctly publishes every committed event to the
// bus (see buildHead's doc comment). Pinned to the same htmx.org@1.9.10
// build the already-loaded htmx core script uses (htmxbase.LayoutData
// renders core before CustomHead), and asserted to load after the
// daisyUI <link>/htmxui.ThemesCSS <style> pair so it cannot disturb the
// load-order trap TestBuildHead_ThemesCSSLoadsAfterDaisyUILink guards
// above. Mirrors manmanv2/ui/templ_render_test.go's
// TestBuildHead_HTMXSSEExtensionScript.
func TestBuildHead_HTMXSSEExtensionScript(t *testing.T) {
	head := buildHead()

	wantScript := `<script src="https://cdn.jsdelivr.net/npm/htmx.org@1.9.10/dist/ext/sse.js"></script>`
	sseScriptIdx := strings.Index(head, wantScript)
	if sseScriptIdx < 0 {
		t.Fatalf("expected pinned htmx SSE extension script %q in head, got: %s", wantScript, head)
	}

	themesStyleIdx := strings.Index(head, "<style>"+htmxui.ThemesCSS+"</style>")
	if themesStyleIdx < 0 {
		t.Fatalf("expected htmxui.ThemesCSS wrapped in <style> in head, got: %s", head)
	}
	if sseScriptIdx <= themesStyleIdx {
		t.Errorf("expected the SSE extension script (index %d) to be appended after the daisyUI/ThemesCSS pair (ThemesCSS <style> at index %d), not interleaved before it, got: %s", sseScriptIdx, themesStyleIdx, head)
	}
}
