---
name: wireframe
description: Iterate on UI wireframes with the user — create/edit screen fragments, assemble a clickable preview.html, apply design feedback. Use for "wireframe", "mockup", "design a screen/page", or UI redesign ideation.
---

# Wireframe iteration

Drive UI design iteration using the wireframe kit (`tools/wireframe/README.md`).
Screens are static daisyUI fragments with fake data; the assembler stitches
them into one clickable `preview.html` the user opens locally.

## Loop

1. Edit fragments in `<app>/design/wireframes/screens/` (manmanv2:
   `manmanv2/ui/design/wireframes/`). Shared chrome lives in `_shell.html`.
2. `bazel run //tools/wireframe -- --dir <app>/design/wireframes --title "<App>"`
3. Tell the user to open/refresh `preview.html`. Apply their feedback; repeat.

Fragment format: first line `<!-- wf: name="servers" title="Servers" -->`,
then terse daisyUI markup (`btn btn-primary`, `card`, `table`, `badge`,
`stat`). Screens link to each other via `href="#/<name>"`. Filename
number-prefixes control ordering; first file is the default route. Annotate
open design questions with `<p class="wf-note">…</p>`.

## Design standards (from manmanv2/ui/DESIGN_SYSTEM.md)

- Action colors: create/edit/view = `btn-primary` (indigo); start/save/deploy =
  `btn-success`; delete/stop/force = `btn-error`; cancel/back = `btn-secondary`.
- Badges: running/online = `badge-success`, pending/starting = `badge-warning`,
  crashed/failed = `badge-error`, stopped/offline = `badge-neutral`.
- Section pages get a `wf-hero` gradient header; detail pages get `breadcrumbs`
  instead (never both).
- Destructive actions go in a danger zone card at the page bottom (see
  `screens/11-server-detail.html` for the pattern).
- Check all three themes (light/night/oled) via the floating theme button.

## Guardrails

- Never hand-edit, hand-assemble, or commit `preview.html` — always re-run the
  assembler.
- Fragments are static: no `<script>`, no Alpine/HTMX attributes, no
  interactivity beyond `#/name` links.
- No inline styles or new CSS in fragments; stay on daisyUI semantic classes +
  Tailwind layout utilities. Recurring patterns go in `tools/wireframe/themes.css`
  or the app's `_shell.html`.
- Colors only via semantic roles (primary/success/error/etc.) — never raw
  palette classes like `bg-purple-500`.
- Keep each fragment roughly one screenful of markup; split dense ideas into
  more screens instead.

## Turning an approved design into code

Approved fragments map to templ components in `manmanv2/ui/components/` and
pages in `manmanv2/ui/pages/`. daisyUI adoption in the real app (replacing
`cdn.tailwindcss.com` in `manmanv2/ui/templ_render.go` with the kit's pinned
daisyUI/Tailwind CDN builds + `themes.css`) is the agreed endgame but a
separate task — don't fold it into a wireframe session.
