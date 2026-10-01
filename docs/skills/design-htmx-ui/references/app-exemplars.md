# App exemplars: what to copy and what to avoid

A scorecard from a 2026-09 survey of every htmx app UI. Use it to find a
working reference implementation before writing a new one. When an app's
design changes, update its entry here.

## Copy these

| Pattern | Where |
|---|---|
| In-place row mutation (`hx-post` → `hx-target` row → `outerHTML`) | `manmanv2/ui/pages/deployment_row.templ` |
| `HX-Request` fragment vs. full page; `HX-Redirect` on navigation | `manmanv2/ui/handlers_*.go`, `leaflab/ui` handlers |
| Toasts via `HX-Trigger: showToast` | `manmanv2/ui/components/layout.templ` (`toastScript`) |
| Right-side blade: focus trap, Esc/scrim close, unsaved-change guard, tabs | `manmanv2/ui/components/blade.templ` |
| Live regions over SSE, plus a "Live" indicator | `libs/go/htmxsse`, `manmanv2/ui/components/live_indicator.templ` |
| Empty state with an action, alert, form fields, `<dl>` rows | `manmanv2/ui/components/ui.templ` |
| Breadcrumbs composed into Shell's `Banner` slot | `manmanv2/ui/components/layout.templ` (`Breadcrumbs`) |
| Dashboard `stat` tiles and clickable environment cards | `tools/app_registry/ui/pages/dashboard.templ` |
| Danger zone and typed confirmation | `htmxui.Confirm`, as used in `tools/app_registry/ui/pages/promote.templ` |
| Disabled-with-reason instead of a control that will fail | `tools/app_registry/ui/components/gate.templ` |
| Domain status → `htmxui.Badge` mapping | `tools/app_registry/ui/components/badges.templ`, manmanv2 `ui.templ` |
| Chat transcript (daisyUI `chat` bubbles, markdown, tool calls as muted `<details>`) and a sticky auto-growing composer | `whagent_net/ui/components/session.templ` |
| Chart with range presets (1h/24h/7d/30d) | `leaflab/ui/components/reading_chart.templ` |

## Per-app notes

- **manmanv2** is the strongest visual design. It has clear status
  badges, large targets, in-place updates and live data. Its problems are
  conceptual: run the "model the objects" step before adding screens. Page
  bodies still mix raw Tailwind `dark:` pairs with daisyUI. Migrate a page
  when you touch it (`DESIGN_SYSTEM.md` migration checklist). Its gradient hero
  headers are legacy: don't copy them.
- **app_registry** has clean, consistent semantic daisyUI and a good
  dashboard. It is developer-facing on purpose (IDs and raw state are fine
  there). Watch for alert overuse (alerts are for persistent conditions, not
  every message) and half-built verbs (gate or remove them). Interactions
  are full-page POSTs, which is acceptable for short pages only.
- **audience_score_system** has good information order and an aggregation
  ("wide") view, but is hard to interact with:
  - it uses zero `hx-*` attributes, so every action is a full-page POST that
    loses scroll position;
  - the Idea detail page is one monolithic card;
  - below 1280px landscape, `#verdict-panel` is a `position: fixed` overlay
    covering the citations it needs, and its form sits in a further closed
    `<details>`;
  - controls use `btn-xs`/`btn-sm` and `checkbox-sm`;
  - channel sub-sections sit behind a row of identical buttons.

  It is a candidate for the workspace shell, with the verdict as a right
  rail.
- **whagent_net** is basic apart from the chat, which is the reference for
  any conversational or streaming surface.
- **leaflab** is basic. It has no active-nav highlighting and no breadcrumbs
  yet. Its chart is worth reusing.
- **krill** is the anti-example, and fails every quality-floor item:
  - it is off the shared stack (`html/template` strings, about 15 lines of
    hand CSS, no htmx or daisyUI);
  - pages are left-hugging, and area pages are lists of "link — blurb";
  - tables are unstyled, status is plain text and buttons are native;
  - raw UUIDs are everywhere, and a design session is opened by typing a
    product UUID;
  - it uses separate confirmation pages and plain-text error pages.

  Path: migrate onto `htmxbase`/`htmxui`, then adopt the workspace shell
  (product switcher, work/design/admin nav groups, properties rail on
  tasks and milestones).

## Cross-app drift to converge on when touched

- **Confirmation** uses five mechanisms today. Converge on the ladder in
  `SKILL.md`.
- **Active nav highlighting** exists only in manmanv2 and whagent_net.
- **Logout** is `/logout` in some apps and `/auth/logout` in others. It is
  passed to `UserMenu` as a parameter, so don't assume either.
- **Theme flash.** Only manmanv2 applies the stored theme in `<head>`; the
  others flash the default theme first.
