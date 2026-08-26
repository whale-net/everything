package main

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/a-h/templ"
	"github.com/whale-net/everything/libs/go/htmxbase"
)

// RenderTempl renders a templ component wrapped in the shared htmxbase
// layout, pulling in pinned CDN builds of Tailwind's browser build and
// daisyUI (NFR-20: no Node, no bundler, no CSS toolchain — the image build
// stays a pure Go cross-compile).
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
		CustomHead: template.HTML(buildHead()),  //nolint:gosec // fixed pinned CDN markup
	}
	return htmxbase.Render(w, layoutData)
}
