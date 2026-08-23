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
