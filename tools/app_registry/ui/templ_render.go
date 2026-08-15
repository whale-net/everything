package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"html/template"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxbase"
)

// themesCSS is a synced copy of tools/wireframe/themes.css — Go's //go:embed
// directive cannot reach across a Bazel package boundary, so the file lives
// in this package too (see themes.css's header comment). It supplies the
// daisyUI palette override that gives this UI its design-standard colors.
//
//go:embed themes.css
var themesCSS string

// RenderTempl renders a templ component wrapped in the shared htmxbase
// layout, pulling in pinned CDN builds of Tailwind's browser build and
// daisyUI (NFR-20: no Node, no bundler, no CSS toolchain — the image build
// stays a pure Go cross-compile). See tools/wireframe/template.html:7-10
// for the reference markup this mirrors.
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

	head := fmt.Sprintf(`<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">@import "tailwindcss";</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>`, themesCSS)

	layoutData := htmxbase.LayoutData{
		Title:      title,
		Content:    template.HTML(buf.String()), //nolint:gosec // component output, not user input
		CustomHead: template.HTML(head),         //nolint:gosec // fixed pinned CDN markup + embedded CSS
	}
	return htmxbase.Render(w, layoutData)
}
