---
name: wireframe
description: Iterate on UI wireframes with the user — create/edit screen fragments, assemble a clickable preview.html, apply design feedback. Use for "wireframe", "mockup", "design a screen/page", or UI redesign ideation.
---

# Wireframe iteration

Drive UI design iteration using the wireframe kit (`tools/wireframe/README.md`).
Screens are static daisyUI fragments with fake data; the assembler stitches
them into one clickable `preview.html` the user opens locally.
For *what* to design (layout archetype, page anatomy, quality floor),
load the `design-htmx-ui` skill first; this skill covers the mechanics.

## Loop

0. **Brief first.** Before the first screen, write `screens/00-brief.html`
   (`<!-- wf: name="brief" title="Brief" -->`) with `design-htmx-ui`
   process steps 1–3:
   - the audience, their top three jobs, and the five-second answer;
   - the object tree, with the verbs on each object;
   - the chosen archetype (workspace, console, or focused).

   It is the default route, so every review starts from it. Update it when
   feedback changes the model, not just the screens.
1. Edit fragments in `<app>/design/wireframes/screens/` (manmanv2:
   `manmanv2/ui/design/wireframes/`). Each screen opens with a `wf-note`
   naming its primary object and primary action. If you can't name them,
   the screen is wrong.
2. `bazel run //tools/wireframe -- --dir <app>/design/wireframes --title "<App>"`
3. Tell the user to open/refresh `preview.html`. Apply their feedback; repeat.

Fragment format: first line `<!-- wf: name="servers" title="Servers" -->`,
then terse daisyUI markup (`btn btn-primary`, `card`, `table`, `badge`,
`stat`). Screens link to each other via `href="#/<name>"`. Filename
number-prefixes control ordering; first file is the default route. Annotate
open design questions with `<p class="wf-note">…</p>`.

Layering: `parent="other-screen"` in the metadata renders the screen as a
panel over its parent (scrim/Esc closes). This is the wireframe form of a
blade. Design rule: keep few, dense base screens — routine operations
inline; only complex editing gets a `parent=` drill-in layer. Layers skip
the shell, so give them a heading and a close link to `#/<parent>`.

## Shell

`_shell.html` is shared chrome per app and must contain `<!-- wf:screen -->`.
Match it to the brief's archetype:

- **Console**: a top navbar. See `manmanv2/ui/design/wireframes/_shell.html`.
- **Workspace**: the daisyUI drawer from `design-htmx-ui`'s
  `references/workspace-shell.md`. Its checkbox toggle needs no script, so
  it is allowed in a fragment.

The shell is one static file shared by every screen, so its nav can't
highlight the current screen. Leave every nav item unhighlighted rather than
hard-coding one as active.

## Design standards

- Colors follow `manmanv2/ui/DESIGN_SYSTEM.md`:
  - **Buttons:** `btn-primary` for create/edit/view, `btn-success` for
    start/save/deploy, `btn-error` for delete/stop/force, `btn-secondary`
    for cancel/back.
  - **Badges:** `badge-success` for running/online, `badge-warning` for
    pending/starting, `badge-error` for crashed/failed, `badge-neutral` for
    stopped/offline.
- Every screen gets a plain page header: title, status badge, primary action.
  Detail screens add `breadcrumbs` above it. No gradient heroes: `wf-hero` is
  legacy, kept only so old screens still render.
- Destructive actions go in a danger zone card at the page bottom (see
  `manmanv2/ui/design/wireframes/screens/11-server-detail.html`).
- Check all three themes (light/night/oled) via the floating theme button.

## Guardrails

- Never hand-edit, hand-assemble, or commit `preview.html` — always re-run the
  assembler.
- Fragments are static: no `<script>`, no Alpine/HTMX attributes, no
  interactivity beyond `#/name` links (and the workspace drawer's checkbox).
- No inline styles or new CSS in fragments; stay on daisyUI semantic classes +
  Tailwind layout utilities. Recurring patterns go in the app's `_shell.html`
  or, if shared, `libs/go/htmxui/themes.css`.
- Colors only via semantic roles (primary/success/error/etc.) — never raw
  palette classes like `bg-purple-500`.
- `wf-*` classes (`wf-note`, `wf-panel`, `wf-scrim`, legacy `wf-hero`)
  hardcode colors outside the theme system and are wireframe-only — never
  reference them from production templ code.
- Keep each fragment roughly one screenful of markup; split dense ideas into
  more screens instead.

## Turning an approved design into code

Approved fragments map to templ pages and components in the app's UI
package (e.g. `manmanv2/ui/pages/`, `manmanv2/ui/components/`), built on
`libs/go/htmxui` primitives. Translate any `wf-*` class to its themed
daisyUI equivalent during that mapping (`wf-note` → plain muted text or an
`alert`, `wf-panel` → a blade). Copying a class verbatim breaks contrast in
themes the wireframe was never checked against. The brief screen's object
tree and primary actions become the nav and page headers; carry them
over unchanged.
