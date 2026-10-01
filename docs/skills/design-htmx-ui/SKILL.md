---
name: design-htmx-ui
description: Design, build, or review a page in one of this repo's htmx web apps (Go + templ + daisyUI on libs/go/htmxui — manmanv2, app_registry, audience_score_system, whagent_net, leaflab, krill). Covers the process (subject → objects → layout → plan → build → critique), choosing a layout archetype (sidebar workspace vs. top-nav console vs. focused tool), page anatomy, the "real page" quality floor, htmx interaction rules, the confirmation ladder, and UI copy. Use for a new screen, a redesign, a UI review, or a "this page is hard to use / looks like plain text" complaint.
---

# htmx app UI design

This is the canonical, harness-neutral source for how this repo's htmx apps
should look and behave. It is symlinked into `.claude/skills/design-htmx-ui`
for Claude Code.

**What this skill owns:** information architecture, layout, page anatomy,
interaction, confirmation, and copy rules across every app.
**What it doesn't own:**

- Action/status **color semantics**: `manmanv2/ui/DESIGN_SYSTEM.md` owns them
  (`libs/go/htmxui/ARCHITECTURE.md` §6). Don't restate that table, link to it.
- **Component APIs**: see `libs/go/htmxui/README.md` (catalog) and
  `ARCHITECTURE.md` (primitive rules, including §8: consolidate after 2–3 call
  sites).
- **Wireframing mechanics**: see the `wireframe` skill.

Reference files (read when the step calls for them):

- [`references/workspace-shell.md`](references/workspace-shell.md) has the
  sidebar workspace layout markup. It is proposed and not yet proven in an app.
- [`references/app-exemplars.md`](references/app-exemplars.md) is the
  per-app scorecard: what to copy, what to avoid, with file paths.

## Stack baseline (non-negotiable)

Every app UI uses Go + templ, `libs/go/htmxbase` for `<head>`, and
`libs/go/htmxui` for the chrome (`Shell` or the workspace shell, plus
`ThemeSwitcher` and `UserMenu`). Styling is daisyUI 5 semantic classes
(`bg-base-100`, `btn-primary`, `badge-success`) plus Tailwind layout
utilities, all from the pinned CDNs. That means:

- No hand-written stylesheets. No raw palette classes (`bg-indigo-600`). No
  new Tailwind `dark:` pairs.
- htmx is **4.0.0** (`libs/go/htmxbase`) plus the `hx-sse` extension where a
  page streams. Don't copy htmx 1.x/2.x examples. The differences that bite:
  - Attributes don't inherit; put `:inherited` on the parent
    (`hx-target:inherited`).
  - `hx-delete`/`hx-get` don't send the enclosing form; add
    `hx-include="closest form"`.
  - `hx-vals` sets an array value as one comma-joined field, not repeated
    fields.
  - `hx-ext` is gone (load the extension script). Events are
    `htmx:after:swap`-style, and `hx-on:` uses the same names.
  - 4xx/5xx would swap, but `htmxbase` sets `noSwap` and `history: "reload"`
    (Back is a plain reload, so a handler never sees `HX-Request` for it).
  - `HX-Trigger` fires on the requesting element after the swap; if the swap
    removes that element, send `{"showToast": {"target": "body", ...}}`.
- Alpine is loaded everywhere, but use it only for client-only UI state
  (open/closed, a reveal). Never use it for data.
- Pages must look right in light, night and oled at minimum. `htmxui.Themes`
  also lists `sunset`, which is a proof of concept: check it, but don't
  design for it.

An app off this stack (krill today) migrates onto it before any redesign.
Restyling a hand-rolled CSS app wastes the effort.

## Process

Work in this order. Skipping steps 1–2 produces pages that are pretty and
conceptually confused (manmanv2's remaining problem). Skipping 3–6 produces
pages that are correct but read as a wall of text (krill).

1. **Ground it.** Write three lines before anything else:
   - Who uses this: developer, operator, or general public.
   - The top three jobs they come here to do.
   - What they must see within five seconds of landing.

   Audience sets density. A developer tool (app_registry) can show IDs,
   digests and raw state. A general-public surface can't: use human names
   and plain status words, and put the technical detail behind a disclosure.
2. **Model the objects.** List the domain objects, their hierarchy
   (product → milestone → task; channel → idea → note), and the verbs on
   each one.
   - Navigation follows that object tree.
   - Each page has **one primary object and one primary action**.
   - If you can't name the primary object of a page, the page is two pages
     or none.
   - Remove or disable half-built verbs. Never render a control that is
     guaranteed to fail: render it disabled, with the reason as a `title`
     (`tools/app_registry/ui/components/gate.templ`).
3. **Choose the archetype** (next section).
4. **Plan each screen** before writing templ. For each one, write:
   - an ASCII wireframe,
   - the primary object and primary action,
   - where the status shows,
   - what the empty state says.

   Then review the plan against step 1 and the quality floor below. If any
   part is the generic default you would draw for any app, revise it and say
   why. For new screens or redesigns, iterate the plan with the user via the
   `wireframe` skill.
5. **Build with primitives.**
   - Use `htmxui` first, then the app's own components.
   - Name a new shared shape only after it recurs (htmxui §8).
   - Map domain statuses through one `StatusXVariant(status) htmxui.BadgeVariant`
     function per domain.
6. **Critique with screenshots.** Use the `run` skill or a browser to check:
   - light, night and oled themes,
   - 390px and 1440px widths,
   - a keyboard-only tab-through, where focus must always be visible.

   Then remove one thing. Most pages improve by losing a card, a sentence or
   a redundant button.

## Layout archetypes

| Archetype | Use when | Shell | Today |
|---|---|---|---|
| **Workspace**: left sidebar, top bar, full-width content | More than ~6 destinations, an object tree 3+ levels deep, or long sessions moving between many entities (the Jira/Linear shape) | `references/workspace-shell.md` (proposed) | Fits krill and audience_score_system |
| **Console**: top nav, centered content | 6 or fewer flat sections, mostly list → detail → act | `htmxui.Shell` | manmanv2, app_registry, leaflab |
| **Focused**: one task fills the viewport | A conversation, a wizard, a live view | `htmxui.Shell`, content-led | whagent_net session |

Workspace rules:

- **Sidebar.** Group items by object type, with section titles. Highlight the
  active item (`menu-active`). Put the context switcher (current product,
  channel, server) at the top of the sidebar.
- **Top bar.** Breadcrumbs, search or jump-to, `ThemeSwitcher` and
  `UserMenu`.
- **Content.** List pages are a filter bar plus a table. Detail pages are a
  header, tabs for the object's facets, a main column, and a right
  **properties rail**: status, owner, dates and links, as label/value rows,
  editable in place. Below `lg` the rail stacks under the main column.
- **Quick look.** Use a right-side blade (`manmanv2/ui/components/blade.templ`)
  to view or edit a row without leaving the list. Blades go one level deep
  only.

Console rules:

- Section pages get a page header. Detail pages add breadcrumbs (in Shell's
  `Banner` slot) above the page header.
- Highlight the active nav item.
- Sub-sections of an object are tabs or a sub-nav, never a row of identical
  buttons inside a card.

Width:

- Choose width per **page type**, never per page:
  - forms: `max-w-2xl`,
  - prose: `max-w-prose`,
  - tables and dashboards: wide (`htmxui.ContainerWide` in Console, the full
    content area in Workspace).
- Every page in a flow uses the same width, so the layout doesn't jump as the
  user clicks through.

## Page anatomy

Every page, top to bottom:

1. **Header.** Title (the object's human name), status badge, the primary
   action as the one `btn-primary`/`btn-success`, then secondary actions as
   `btn-ghost` or behind an overflow `dropdown`.
2. **At a glance**, on landing and dashboard pages only. Show the 3–5 numbers
   or states that answer "is anything wrong / what needs me", using `stat`
   tiles or a "needs attention" list. **A landing page is never a list of
   links with blurbs.**
3. **Body.** Group related content into cards (`card bg-base-100 border
   border-base-300`) on the `bg-base-200` page.
   - Cards group things. They aren't for every item, and one giant card per
     page is also wrong.
   - Use tables for collections and `<dl>` label/value rows for properties.
4. **Danger zone** at the bottom, only for page-level destructive actions
   (see the confirmation ladder).

## Quality floor: a real page, not a document

Each of these failures is present in at least one app today:

- **Surfaces and hierarchy.** `bg-base-200` page, `bg-base-100` panels, real
  headings. If the page reads the same with CSS turned off, it fails.
- **Every enum is a badge.** Status, verdict, lane and role always render as
  a `badge` through the domain's variant mapper, never as plain words.
- **Buttons look like buttons** (`btn` classes) and links look like links.
  Never ship unstyled native `<button>`s.
- **Humans pick, they never type IDs.** Replace "paste a UUID" inputs with a
  `select`, a search or typeahead (`hx-get` with
  `hx-trigger="keyup changed delay:300ms"`), or links from where the object
  already appears.
- **IDs are secondary.** Show the human name. Show the ID only as a small
  `font-mono` copy chip where someone genuinely needs it.
- **Styled tables.** Use `table` with `table-zebra` or row hover, sticky
  headers on long lists, right-aligned numbers, row actions in the last
  column, and `overflow-x-auto` around the table.
- **Empty states point to an action**: what's missing and the button that
  fixes it (`manmanv2/ui/components/ui.templ` `EmptyState`). Avoid a lone
  "No items." in faint text.
- **Icons.** Use inline SVG in templ (Lucide, ISC license), held in an
  app-local `icons.templ` until a second app needs the same icons. There's
  no bundler and no icon font. Use icons for nav items, status and common
  verbs. Every icon-only button gets `aria-label`.
- **Pagination** is daisyUI `join` buttons with a count, not a bare
  "Next page" link.
- **Errors render inside the shell.** Avoid a bare `http.Error` plain-text
  page for anything a user can reach.
- **Contrast.** Muted text is `text-base-content/70` at the faintest. Don't
  set metadata in `text-xs` at `/50`.

## Interaction rules

- **Mutate in place.** For anything on a page longer than one screen, use
  `hx-post`/`hx-delete` with `hx-target` set to the smallest owning fragment
  and `hx-swap="outerHTML"`. The reference is
  `manmanv2/ui/pages/deployment_row.templ`. A full-page POST-redirect throws
  away scroll position and context (audience_score_system's main usability
  problem).
- **One handler, two shapes.** Branch on the `HX-Request` header: return the
  fragment for htmx, the full page otherwise. Use `HX-Redirect` when an
  action navigates.
- **Feedback.**
  - Validation errors go inline under the field.
  - Operation results go in a toast via the `HX-Trigger` `showToast` event
    (`manmanv2/ui/components/layout.templ`).
  - Use a persistent `alert` only for conditions that still hold, such as a
    misconfiguration.
- **Loading.** Any request that can take over ~300ms shows `hx-indicator` on
  the control that fired it.
- **Live data.** Use SSE via `libs/go/htmxsse`: `hx-sse:connect` on a
  container and `data-sse-topic="<topic>"` on each region it swaps (see that
  package's README; it needs the `hx-sse` extension script in the head).
  Use `hx-trigger="load, every 30s"` polling only for cheap summaries.
- **URLs reflect state.** Tabs, filters, sorting and pagination go through
  GET forms or `hx-push-url`, so back, refresh and shared links work.
- **Targets.**
  - Default `btn` size (44px). `btn-sm` is allowed only in dense table rows
    and toolbars.
  - Never use `btn-xs` for anything a user must click to finish a job:
    `htmxui/themes.css` gives it no minimum height.
  - Required checkboxes and radios use the default size.
- **Don't hide the main job.** Collapse only secondary or advanced content
  (`<details>`). The form a page exists for is open by default.
- **No overlays on the content they act on.** A form that cites items on the
  page sits beside them: a right rail at `lg`, stacked below on smaller
  screens. Never use a `position: fixed` panel covering them.

## Confirmation ladder

Use exactly one mechanism per rung:

| Action | Mechanism |
|---|---|
| Reversible or low-stakes (archive, toggle, edit) | No confirm. Act in place, then toast the outcome, with an undo if it's cheap. |
| Destructive, one row (delete a note, stop a server, remove access) | `hx-confirm` with specific text: "Remove alice from Owners? They lose access immediately." |
| Destructive, page-level or high blast radius (delete a game and its configs, promote to prod) | Danger zone at the bottom of the page using `htmxui.Confirm`. Add typed or checkbox acknowledgement when it's irreversible. |

Never use a separate confirmation page, and never put a destructive action
behind zero confirmation. manmanv2's Alpine click-to-reveal danger zones are
a tolerated existing variant while `htmxui.Confirm`'s reveal mode is still
undecided (htmxui `ARCHITECTURE.md` §13). Don't add new copies.

## Copy

- **Use the user's nouns, not the system's.** "Notifications", not "webhook
  config". The UI's names for objects and verbs match the IA from step 2,
  and stay the same everywhere.
- **Buttons say what happens** ("Save verdict", "Start server"), never
  "Submit" or "OK". The resulting toast uses the same verb: "Verdict saved".
- **Sentence case everywhere**, headers and table columns included.
- **Errors say what happened and how to fix it**, in the interface's voice.
  No apologies, no "something went wrong".
- **Empty states invite action.** Every word must make the screen easier to
  use. Cut intros that explain what the page is.

## Relationship to the frontend-design skill

This skill adapts Anthropic's `frontend-design` skill (Apache-2.0, in the
`claude-plugins-official` marketplace). That skill is tuned for distinctive
one-off pages. These apps share one design system on purpose, so its advice
is split as follows.

**Kept:**

- Ground the design in subject, audience and primary job.
- Plan, review the plan against the brief, build, then critique with
  screenshots.
- Visual structure carries information. Numbering, borders and labels must
  encode something real.
- Spend boldness in one place.
- The quality floor: responsive, visible focus, reduced motion respected,
  accessible contrast.
- The writing guidance.

**Overridden:**

- No per-app palette, typeface or identity reinvention. Every app uses the
  shared themes and daisyUI's font stack, and changing those is a cross-app
  decision.
- An app's identity is its brand mark plus **one signature element** that
  does real work, such as whagent_net's chat composer. Decoration doesn't
  count as a signature.
- No hero banners. Most pages here are working surfaces, and a plain page
  header (title, status, primary action) is the header.

**Generated-UI tells, decided:**

- **Banned in new code:**
  - ALL-CAPS tracked labels and eyebrows above headings (including uppercase
    table headers),
  - `→` appended to link or button text,
  - meta strings joined with `·` (use labeled fields or badges),
  - decorative gradients, including gradient hero banners,
  - identical cards for every item.
- **Allowed with a reason:** `font-mono` for actual machine values (IDs,
  addresses, hashes, code), never as a style for labels.
- **Legacy:** manmanv2's uppercase table headers and gradient hero headers
  (`HeroHeader`). Replace them with plain page headers when you touch the
  page.

## Review checklist

Run this before calling a page done:

- [ ] The audience, top-3 jobs and five-second answer are written down, and
      the page serves them.
- [ ] One primary object, one primary action, visible in the header.
- [ ] Nav matches the object tree. The active item is highlighted.
      Breadcrumbs appear on detail pages.
- [ ] Every status is a badge. No typed IDs. No unstyled controls.
- [ ] Mutations swap in place. Toast feedback on success. Inline errors on
      failure.
- [ ] The confirmation rung matches the ladder.
- [ ] Empty, loading and error states are designed, not defaulted.
- [ ] Checked in light, night and oled, at 390px and 1440px, keyboard only.
- [ ] No banned tells. One element removed after the first critique.
