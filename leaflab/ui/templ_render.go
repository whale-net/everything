package main

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// RenderTempl renders a templ component wrapped in the shared htmxbase
// layout, pulling in pinned CDN builds of Tailwind's browser build and
// daisyUI (no Node, no bundler, no CSS toolchain — the image build stays a
// pure Go cross-compile). Mirrors tools/app_registry/ui/templ_render.go.
//
// Trap (do not rediscover): htmxbase.LayoutData renders CustomCSS BEFORE
// CustomHead. themes.css must load after daisyui.css or it silently loses
// to daisyUI's default palette with no error, so both the daisyUI <link>
// and the themes.css <style> live in CustomHead (which renders last), in
// that exact order. Never split them across CustomCSS/CustomHead.
func RenderTempl(w http.ResponseWriter, r *http.Request, title string, component templ.Component) error {
	var buf bytes.Buffer
	if err := component.Render(r.Context(), &buf); err != nil {
		return err
	}

	layoutData := htmxbase.LayoutData{
		Title:      title,
		Content:    template.HTML(buf.String()), //nolint:gosec // component output, not user input
		CustomHead: template.HTML(buildHead()),  //nolint:gosec // fixed pinned CDN markup + embedded CSS
	}
	return htmxbase.Render(w, layoutData)
}

// buildHead constructs the CustomHead markup: pinned Tailwind browser build
// + daisyUI CDN <link>, then htmxui.ThemesCSS in that exact order (see the
// "Trap" doc comment above).
func buildHead() string {
	return fmt.Sprintf(`<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">@import "tailwindcss";</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>`, htmxui.ThemesCSS)
}
