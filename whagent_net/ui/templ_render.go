package main

import (
	"bytes"
	"html/template"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/htmxui"
)

// RenderTempl renders a templ component wrapped in the shared htmxbase
// layout, pulling in pinned CDN builds of Tailwind's browser build and
// daisyUI (no Node, no bundler, no CSS toolchain -- the image build stays
// a pure Go cross-compile, docs/DOCKER.md). Mirrors
// tools/app_registry/ui/templ_render.go's and manmanv2/ui/templ_render.go's
// RenderTempl/buildHead split.
//
// Trap (do not rediscover, see htmxui.ThemesCSS's "Load-order trap" doc
// comment): htmxbase.LayoutData renders CustomCSS BEFORE CustomHead.
// themes.css must load after daisyui.css or it silently loses to daisyUI's
// default palette with no error, so both the daisyUI <link> and the
// themes.css <style> live in CustomHead (which renders last), in that
// exact order. Never split them across CustomCSS/CustomHead.
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

// buildHead constructs the CustomHead markup: pinned Tailwind browser
// build + daisyUI CDN <link>, then htmxui.ThemesCSS, then chatMarkdownCSS,
// in that exact order (see the "Trap" doc comment above), then the htmx
// SSE extension script session.templ's hx-ext="sse" (and its
// sse-connect/sse-swap attributes) require to do anything at all --
// without this script tag, htmx silently no-ops the unknown "sse"
// extension and no live connection is ever opened, regardless of the
// backend correctly publishing to the bus. Mirrors
// manmanv2/ui/templ_render.go's and
// tools/app_registry/ui/templ_render.go's identical buildHead line;
// appended last, same as those two, since it has no ordering dependency on
// daisyUI/ThemesCSS/chatMarkdownCSS and only needs to load after the htmx
// core script, which htmxbase.LayoutData already guarantees by rendering
// core before CustomHead. Split out from RenderTempl so
// templ_render_test.go can assert the load order and the SSE script's
// presence directly against production code, mirroring the other two
// UIs' identical split.
func buildHead() string {
	return `<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">@import "tailwindcss";</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>` + htmxui.ThemesCSS + `</style>
<style>` + chatMarkdownCSS + `</style>
<script src="https://cdn.jsdelivr.net/npm/htmx.org@1.9.10/dist/ext/sse.js"></script>`
}

// chatMarkdownCSS gives goldmark-rendered markdown (components/session.templ's
// renderMarkdown, inside a chat bubble's .chat-markdown wrapper) sane
// default spacing/monospace treatment -- the Tailwind CDN build above
// pulls in no typography plugin, so without this, goldmark's <p>/<pre>/
// <code>/<ul> output would render with bare browser UA styles instead of
// looking like part of the chat window.
const chatMarkdownCSS = `
.chat-markdown :where(p) { margin: 0 0 0.5em; }
.chat-markdown :where(p):last-child { margin-bottom: 0; }
.chat-markdown :where(ul, ol) { margin: 0 0 0.5em 1.25em; }
.chat-markdown :where(ul) { list-style-type: disc; }
.chat-markdown :where(ol) { list-style-type: decimal; }
.chat-markdown :where(pre) { background: rgba(0, 0, 0, 0.15); padding: 0.5em 0.75em; border-radius: 0.5em; overflow-x: auto; margin: 0.5em 0; }
.chat-markdown :where(code) { font-family: ui-monospace, monospace; font-size: 0.875em; }
.chat-markdown :where(pre code) { background: none; padding: 0; }
.chat-markdown :where(code):not(pre code) { background: rgba(0, 0, 0, 0.15); padding: 0.1em 0.35em; border-radius: 0.3em; }
.chat-markdown :where(blockquote) { border-left: 3px solid currentColor; opacity: 0.8; padding-left: 0.75em; margin: 0.5em 0; }
.chat-markdown :where(a) { text-decoration: underline; }
.chat-markdown :where(h1, h2, h3, h4, h5, h6) { font-weight: 700; margin: 0.5em 0 0.25em; }
`
