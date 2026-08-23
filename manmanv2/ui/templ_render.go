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
// daisyUI (NFR1: no Node, no bundler, no CSS toolchain — the image build
// stays a pure Go cross-compile). Mirrors
// tools/app_registry/ui/templ_render.go's RenderTempl/buildHead split
// (#1005 adopts app-registry onto this same pattern).
//
// Trap (do not rediscover, see htmxui.ThemesCSS's "Load-order trap" doc
// comment): htmxbase.LayoutData renders CustomCSS BEFORE CustomHead.
// themes.css must load after daisyui.css or it silently loses to
// daisyUI's default palette with no error, so both the daisyUI <link>
// and the themes.css <style> live in CustomHead (which renders last), in
// that exact order (NFR5). Never split them across CustomCSS/CustomHead.
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

// buildHead constructs the CustomHead markup: a small no-reload theme
// bootstrap script, then the pinned Tailwind browser build + daisyUI CDN
// <link>, then htmxui.ThemesCSS, in that exact order (see the "Trap" doc
// comment above). Split out from RenderTempl so a future
// templ_render_test.go can assert the NFR5 load order and the FR5/FR6
// guards directly against production code, mirroring
// tools/app_registry/ui/templ_render.go's buildHead/templ_render_test.go.
func buildHead() string {
	return fmt.Sprintf(`%s
<script src="https://cdn.jsdelivr.net/npm/@tailwindcss/browser@4.3.3/dist/index.global.js"></script>
<style type="text/tailwindcss">
@import "tailwindcss";
%s
</style>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/daisyui@5.6.18/daisyui.css">
<style>%s</style>
<style>%s</style>`, themeBootstrapScript, darkVariantDirective, htmxui.ThemesCSS, legacyClassShimCSS)
}

// themeBootstrapScript applies the operator's saved theme to <html> as
// early as possible in <head> — before any body content paints — to
// avoid a flash of the wrong theme on load (the same goal the old
// pre-migration themeInit script had). It never calls location.reload()
// (FR5): htmxui's ThemeSwitcher (mounted by components.Layout) also
// re-applies the stored theme on every page load, so this script only
// needs to get the *first* paint right.
//
// FR6: this also migrates a previously-saved 'manman-theme' localStorage
// value (the pre-migration key) onto htmxui.ThemeSwitcherStorageKey (the
// key the shared ThemeSwitcher reads/writes) exactly once, the first time
// it finds the new key empty, so an operator's saved preference (light,
// night, or oled) survives the switch to the shared component instead of
// silently resetting. See htmxui.ThemeSwitcher's doc comment ("Exported
// so a consuming app's own bootstrap code ... can read the same key")
// for why this lives here rather than in htmxui itself.
var themeBootstrapScript = fmt.Sprintf(`<script>
(function() {
    var KEY = %q;
    var LEGACY_KEY = 'manman-theme';
    var theme = null;
    try {
        theme = localStorage.getItem(KEY);
        if (!theme) {
            var legacy = localStorage.getItem(LEGACY_KEY);
            if (legacy) {
                theme = legacy;
                localStorage.setItem(KEY, legacy);
            }
        }
    } catch (e) {
        theme = null;
    }
    if (!theme) {
        theme = window.matchMedia('(prefers-color-scheme: dark)').matches ? 'night' : 'light';
    }
    document.documentElement.setAttribute('data-theme', theme);
})();
</script>`, htmxui.ThemeSwitcherStorageKey)

// darkVariantDirective keeps manmanv2's still-unmigrated page bodies
// (manmanv2/ui/pages/*.templ — page-body markup migration to daisyUI is
// the follow-on issue, not this one) working: they use Tailwind's `dark:`
// utility variant extensively, driven under the old pipeline by a
// `.dark` class the removed themeManagement() script toggled on <html>.
// Tailwind v4's default `dark:` strategy is prefers-color-scheme, not a
// class/attribute toggle, so without this the `dark:` classes would stop
// responding to the theme switcher entirely. Redefining the variant to
// key off the shared data-theme attribute (set by htmxui.ThemeSwitcher —
// see theme_switcher.templ) instead of a `.dark` class means page bodies
// need no class-toggling JS of their own.
const darkVariantDirective = `@custom-variant dark (&:where([data-theme="night"], [data-theme="oled"], [data-theme="night"] *, [data-theme="oled"] *));`

// legacyClassShimCSS covers the one manmanStyles-era class name still
// reachable today with no daisyUI equivalent: manmanv2/ui/templates/
// actions_manage.html (the legacy html/template partial rendered by the
// still-live handleGameActions/handleConfigActions handlers) uses
// `btn-danger` for its delete/remove buttons. daisyUI's destructive
// button variant is named `btn-error`, not `btn-danger`, so retiring
// manmanStyles (the old templates.go CSS literal — see its removal in
// this same change) would silently drop that button's color with no
// daisyUI class picking it up. Every other manmanStyles class actually
// reachable today (`.btn`, `.btn-primary`, `.btn-sm`, `.badge`,
// `.badge-success`, `.badge-secondary`) shares its literal name with a
// real daisyUI component class, so daisyUI styles those automatically
// once loaded and needs no shim; unreachable manmanStyles classes
// (`.card`, `.stat-card`, `.session-panel`, etc. — grepped against
// pages/*.templ, components/*.templ, and actions_manage.html, no hits)
// were deleted outright rather than carried forward.
const legacyClassShimCSS = `.btn-danger { background-color: var(--color-error); color: var(--color-error-content); }
.btn-danger:hover { filter: brightness(0.9); }`
