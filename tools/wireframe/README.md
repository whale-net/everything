# Wireframe Kit

Assembles static screen fragments into a single clickable `preview.html` for
fast UI design iteration — fake data, daisyUI styling, hash-link navigation
between screens. No build pipeline, no app changes; open the output in a
browser and refresh after each edit.

Styling comes from pinned CDN builds (daisyUI 5 + Tailwind 4 browser runtime),
so viewing requires network. `themes.css` maps the daisyUI theme variables to
the manmanv2 design standards (`manmanv2/ui/DESIGN_SYSTEM.md`): three themes
(`light` / `night` / `oled`), primary=indigo, success=green, error=red,
neutral=slate.

## Usage

```
bazel run //tools/wireframe -- --dir <app>/design/wireframes --title "My App"
# writes <app>/design/wireframes/preview.html (gitignored)
```

## Input layout

```
<dir>/_shell.html       optional shared chrome (nav etc.); must contain the
                        marker <!-- wf:screen --> where screen bodies go
<dir>/screens/*.html    one fragment per screen; ordered by filename
                        (number-prefix to control order; first = default route)
```

Each fragment starts with a metadata comment, then plain daisyUI markup:

```html
<!-- wf: name="servers" title="Servers" -->
<div class="card bg-base-100">...</div>
```

Link between screens with `href="#/<name>"`. The assembler adds a floating
screen index and theme switcher automatically.

**Layers** (xbox-blade drill-ins): add `parent="other-screen"` to the
metadata and the screen renders as a panel sliding over its parent instead
of a full page — no app shell, scrim click or Esc tabs back out. Layers
nest (a layer's parent may itself be a layer). Give the panel its own
heading and a close link to `#/<parent>`.

## Adopting for a new app

Create `<app>/design/wireframes/screens/` (plus `_shell.html`), write
fragments, run the assembler. That's all — the kit is app-agnostic. See
`manmanv2/ui/design/wireframes/` for a worked example and
`.claude/skills/wireframe/SKILL.md` for the iteration workflow and guardrails.
