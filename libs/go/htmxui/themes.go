package htmxui

import _ "embed"

// ThemesCSS is the daisyUI theme override stylesheet, mapping daisyUI's CSS
// variables to the manmanv2 design standards (manmanv2/ui/DESIGN_SYSTEM.md):
// primary=indigo (create/edit/view), success=green (start/save),
// error=red (delete/stop), neutral/secondary=slate (cancel/back),
// warning=yellow (pending states). Themes: light / night / oled / sunset
// (sunset is a Phase 3 POC theme, #998 FR10 — see themes.css's doc comment).
//
// Load-order trap (NFR5 — do not rediscover): ThemesCSS must be rendered
// inside each consuming app's htmxbase.LayoutData.CustomHead, after the
// daisyUI CDN <link> tag — not in CustomCSS, which htmxbase.LayoutData
// renders first (before CustomHead). If it loads before daisyUI, the
// palette override silently loses to daisyUI's defaults with no error.
//
// This package documents the trap but cannot enforce it from inside the
// library — preserving the order is each consuming app's wiring
// responsibility. Reference implementation of the correct order: the head
// string in tools/app_registry/ui/templ_render.go (Tailwind browser build
// <script>, then daisyUI <link>, then <style>{ThemesCSS}</style>).
//
//go:embed themes.css
var ThemesCSS string
