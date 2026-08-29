package main

import (
	"bytes"
	"fmt"
	"html/template"
	"log"
	"net/http"

	"github.com/a-h/templ"

	"github.com/whale-net/everything/libs/go/htmxauth"
	"github.com/whale-net/everything/libs/go/htmxbase"
	"github.com/whale-net/everything/libs/go/htmxui"
	"github.com/whale-net/everything/tools/app_registry/ui/components"
)

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
// The htmx SSE extension must also load after the htmx core script, which
// htmxbase.LayoutData renders before CustomHead, so the SSE extension is
// included in buildHead() (FR20a).
func RenderTempl(w http.ResponseWriter, r *http.Request, title string, component templ.Component) error {
	// FR-59: log the misconfiguration at error level on every render, in
	// addition to components.MisconfigBanner's on-page banner (rendered by
	// Shell for every screen) — derived from the loaded session user, not
	// from auth middleware, so AUTH_MODE=none never false-positives here
	// (see components.RolesMisconfigured).
	if user := htmxauth.GetUser(r.Context()); components.RolesMisconfigured(user) {
		log.Printf("ERROR: FR-59 misconfiguration: realm_access.roles claim absent for user %q — likely a missing Keycloak \"Add to ID token\" realm-roles mapper", user.Sub)
	}

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
// + daisyUI CDN <link>, then the htmx SSE extension (pinned to 1.9.10, FR20a),
// then htmxui.ThemesCSS in that order. The htmx core script is loaded by
// htmxbase.LayoutData before this CustomHead is rendered, ensuring correct
// extension load order. Split out from RenderTempl so templ_render_test.go can
// assert the load order directly against production code instead of a
// duplicated literal.
func buildHead() string {
	return fmt.Sprintf(`<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">@import "tailwindcss";</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<script src="https://cdn.jsdelivr.net/npm/htmx.org@1.9.10/dist/ext/sse.js"></script>
<style>%s</style>`, htmxui.ThemesCSS)
}
