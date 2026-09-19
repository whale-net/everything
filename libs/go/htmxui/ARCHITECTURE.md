# htmxui — Architecture & Design Principles

This document is the authoritative source for htmxui's design language: the
rules every existing primitive already follows, so a new primitive (or a new
adopting app) can be consistent without reverse-engineering the pattern from
someone else's `.templ` file. `README.md` is the component catalog (what
exists); this file is why each one looks the way it does and what rule to
follow when adding the next one.

## 1. Scope: cross-app primitives only

htmxui holds only chrome and primitives that are common across apps — layout
scaffolding, themes, generic buttons/badges/cards/confirmations. It never
holds:

- **App-specific screens.** Those stay in each app's own UI package
  (`tools/app_registry/ui`, `manmanv2/ui`, `audience_score_system/web`, ...).
- **Domain vocabulary.** `Badge` renders one label and one colour; it does not
  know what "promotable" or "artifact state" means. That vocabulary is
  app-owned (`tools/app_registry/ui/components/badges.templ`'s
  `PromotabilityBadge`/`ArtifactStateBadge`/etc.), expressed *in terms of*
  `Badge`, never folded into it.
- **A specific auth/proto/domain dependency.** `UserMenuData` and
  `ShellData.UserLabel` are plain strings, not `htmxauth.UserInfo` — a caller
  with a different auth story, or no auth at all (`AUTH_MODE=none`), can
  still populate them. If a component would need to import a domain's
  `store` package or `//libs/go/htmxauth` to do its job, it does not belong
  in htmxui.

If you're adding something and it needs a proto import, a domain type, or
hardcoded copy that only makes sense for one app, it's app-owned — compose it
alongside htmxui's primitives (see §3), don't extend htmxui to know about it.

## 2. Shared chrome vs. app-owned content (the Shell boundary)

`Shell` owns exactly: the navbar frame, the `ThemeSwitcher` mount, the banner
slot, the main content region, and the container-width wrapper. It hardcodes
**no nav items, no banner content, no header-right extras**. Those are
`templ.Component` slots (`Nav`, `Banner`, `HeaderRight`) the caller supplies.

This is a boundary, not a convenience: `Shell` is deliberately *not* a literal
lift of an existing app's layout component that happens to hardcode its nav
inline. Every app's nav is different (`audience_score_system`'s four global
links vs. manmanv2's entirely different set); baking one into `Shell` would
make it stop being shared. When in doubt about whether something is Shell's
job or a slot's job, ask: "would every adopting app want this exact content?"
If no, it's a slot.

## 3. Composition, not folding

When an app needs Shell's shared pieces *plus* something app-specific in the
same visual spot (e.g. manmanv2's `ServerSelector` next to the theme
switcher and user menu), the app-specific piece is never added as a new
htmxui parameter or merged into an existing component. Instead, the caller
composes its own component together with htmxui's (e.g. via `templ.Join`)
into one combined `templ.Component`, and passes that combined component as
the slot (`ShellData.HeaderRight`). htmxui's own primitive and the app's
extra render as **siblings** inside the slot, not nested inside one another.

`UserMenu` is the canonical example: it takes no app-specific parameters at
all, precisely so it stays composable this way indefinitely as more apps add
more header-right extras.

## 4. Empty means "render nothing"

Every optional htmxui input has the same contract: the zero value (empty
string, nil slot, nil component) means *omit the affected markup entirely*,
never render an empty/broken placeholder for it.

- `ShellData.UserLabel` / `UserMenuData.IdentityLabel` empty → no span, no
  dropdown. This matters for `AUTH_MODE=none`, where there may be no real
  user to show — a menu with a logout control and no identity behind it is
  worse than no menu.
- `ShellData.Banner` / `Card`'s `header`/`body`/`actions` nil → that slot's
  wrapper element doesn't appear at all, not an empty `<div>`.
- `ShellData.ContainerClass` empty → falls back to the default (`max-w-4xl`),
  not an unstyled container.
- `ButtonSize`/`BadgeSize` `MD` is `""` → no size class is emitted at all,
  relying on daisyUI's own implicit default, rather than htmxui hardcoding
  what that default class name currently is.

New components should follow the same rule: a caller that didn't supply an
optional value gets a clean omission, never a rendered gap.

## 5. Variant/size are typed string enums mapped 1:1 to daisyUI classes

`ButtonVariant`, `ButtonSize`, `BadgeVariant`, `BadgeSize` are all named
string types whose constants are exactly the daisyUI class they emit
(`ButtonPrimary ButtonVariant = "btn-primary"`). This keeps two things true
at once: call sites read as intent ("this is the primary action"), and the
mapping to the underlying utility class needs no translation table anyone
has to keep in sync — the constant *is* the class.

Follow this shape for any future "pick one of N daisyUI modifier classes"
parameter, rather than inventing a `switch` statement that translates an
arbitrary Go enum into class strings.

## 6. Color semantics are defined once, outside htmxui

htmxui's `ThemesCSS` (and, by extension, every `ButtonVariant`/`BadgeVariant`
built on top of it) implements the color semantics documented in
**`manmanv2/ui/DESIGN_SYSTEM.md`**: primary = create/edit/view, success =
start/save, error = delete/stop (destructive), neutral/secondary =
cancel/back, warning = pending/caution. htmxui does not re-derive or
re-document this mapping — it is the implementation of that domain-owned
design system, not a second source of truth for it. If the semantics ever
need to change, change them there first; htmxui's constants and `themes.css`
follow.

## 7. Named presets over inline literals

When a Tailwind/daisyUI value is meant to be reused as a deliberate design
choice — not a one-off tweak — it gets a named Go constant in htmxui instead
of being hand-written at each call site. `ContainerWide` (`container.go`) is
the reference example: audience_score_system's Idea detail page originally
had the same breakpoint string
(`max-w-4xl xl:max-w-7xl 2xl:max-w-[1600px]`) copy-pasted in two places
before it was named and exported.

Rule of thumb: if you find yourself writing (or copying) the same
multi-class Tailwind string more than once, or if you want a page's author —
human or agent — to be able to discover "is there a wide-page option?"
without reading another app's source, it belongs here as a named constant,
not as freehand classes at the call site.

## 8. Consolidation happens at 2–3 real call sites, never speculatively

`Confirm`/`ConfirmProps` is htmxui's model for turning a repeated pattern
into a shared component: it was written only after the same
confirm/danger-zone shape had been independently implemented three times
(`promote.templ`, `rollback.templ`, `environment_form.templ`), and its
parameters are literally the union of what those three call sites needed —
nothing speculative added for a hypothetical fourth.

Where the three diverged, the fix was a **parameter** (`ZoneTitle` toggles
the bordered/headed card chrome on or off). Where they diverged *too much* to
force into one shape (the exact typed-confirmation affordance — a checkbox
here, a required textarea there), the fix was a **children slot**, not a
parameter, and not a second component. Don't add a boolean/enum parameter to
approximate something that's really open-ended content; give it a slot.

Do not build a new htmxui primitive off of one call site or off of an
imagined future need — wait for the second or third real, independent
implementation, then look at what they actually have in common.

## 9. Structural chrome only; content and the `<form>`/attribute escape hatch stay app-owned

Every content primitive (`Card`, `Confirm`, `Button`, `Badge`) renders
structural chrome and delegates two things back to the caller:

- **Slot content** — headings, `<dl>` grids, forms, whatever markup a slot
  holds. htmxui decides slot *order* and *wrapper classes*, never slot
  *content*.
- **The `<form>` element itself.** `Confirm` never renders `<form
  method=... action=...>` — callers wrap `Confirm`'s children and action row
  in their own form. This is what lets one `Confirm` serve an `hx-post`
  form, a plain POST form, or a multi-action form (`promote.templ`'s
  dry-run vs. commit submit buttons) without htmxui needing to know which.

Every primitive also accepts a `templ.Attributes` escape hatch (`attrs`,
`submitAttrs`) forwarded onto the relevant element, specifically so
`hx-*` attributes, `id`, `title`, `name`/`value` pairs, and similar concerns
never need to become named htmxui parameters. If you're tempted to add a
parameter for a single HTML attribute, use the escape hatch instead.

## 10. No bundler assets; CSS ships as CDN + a single embedded stylesheet

This package ships no Node/npm/bundler files and introduces no CSS build
toolchain (no `package.json`, and there won't be one). Tailwind and daisyUI
arrive at the browser via each consuming app's own pinned CDN
`<link>`/`<script>` tags. htmxui's only shipped CSS is `themes.css`
(`go:embed`'d as `ThemesCSS`), which maps daisyUI's CSS variables to the
palette semantics in §6 — it does not, and must not, define layout or
component classes of its own. All layout/structure is expressed as inline
daisyUI/Tailwind utility classes in the `.templ` files themselves.

**Load-order trap (do not rediscover):** `ThemesCSS` must be injected after
the daisyUI CDN `<link>` tag, in a caller's `CustomHead`, not `CustomCSS`
(which renders earlier). Loading before daisyUI means the palette override
silently loses to daisyUI's defaults with no error. See `ThemesCSS`'s doc
comment in `themes.go` for the reference load order.

## 11. Canonical shared values live in htmxui, not per-app copies

`Themes` (`themes.go`) is the single list of available theme entries; every
adopting app passes it straight through to `ShellData.Themes` rather than
declaring its own copy. Before this existed, two apps hand-maintained
identical copies and two others fell behind with a stale subset. The lesson
generalizes: any value meant to be identical across every adopting app (a
theme list, `ContainerWide`, a future shared color/label mapping) is a
canonical exported value here, never a per-app literal that can silently
drift.

## 12. BUILD shape: `templ_library`, hand-maintained sources

`libs/go/htmxui` is a `templ_library` (from `//tools:templ.bzl`), the first
one under `libs/go/`. Its `go_srcs` and `deps` lists are hand-maintained with
a `# keep` marker (gazelle would otherwise try to collapse it back into a
plain `go_library`) — adding a new non-`.templ` Go file (like `container.go`)
requires manually adding it to `go_srcs` in `BUILD.bazel`; gazelle will not
pick it up on its own. See `README.md`'s "BUILD shape" section for the full
rule set.

## 13. Known conventions not yet formalized (survey, 2026-09)

A cross-domain survey of `manmanv2/ui`, `tools/app_registry/ui`,
`leaflab/ui`, `whagent_net/ui`, and `audience_score_system/web` found
several patterns independently reinvented — sometimes 15+ times in a single
domain, sometimes with visibly drifting markup between call sites — that are
not yet htmxui primitives. Recorded here so the next person hitting one of
these doesn't re-derive it from scratch or add a sixth independent copy.
Per §8, extraction should still wait for a deliberate decision (someone
implementing one of these should read the real call sites listed below, the
same way `Confirm` was built from its three), not happen automatically
because this list exists.

- **Alert/flash banner** — by far the strongest signal: `<div role="alert"
  class="alert alert-{variant} ...">` (or a thin wrapper of the same shape)
  appears independently in **every surveyed domain** —
  `tools/app_registry/ui` (15+ call sites, e.g. `promote.templ`,
  `environment_form.templ`, `deployments.templ`), `leaflab/ui` (`admin_boards.templ`,
  `board_detail.templ`, `boards.templ`, `regions.templ`,
  `sensor_history.templ`), `whagent_net/ui` (`grants.templ`,
  `grants_admin.templ`, `link_ass_result.templ`, `mcp_consent.templ`,
  `session_new.templ`), and `manmanv2/ui` (already has its own generic
  `components.Alert(alertType, message)` in `ui.templ`). Already drifting
  between copies (some append `shadow-md`, some don't; `role="alert"` vs.
  `role="status"` for success). Same variant-enum-plus-message-slot shape as
  `Badge`. This is the single best next candidate for a shared
  `htmxui.Alert`.
- **Empty-state message** — `<p class="text-base-content/NN">No X
  yet.</p>` (or `opacity-70` equivalent), with the opacity fraction actively
  drifting between call sites even within one domain (`audience_score_system`
  alone has `/50`, `/60`, and `/70` versions). Found in
  `audience_score_system/web` (20+ sites), `leaflab/ui`, `whagent_net/ui`.
  `manmanv2/ui` already has a generic `components.EmptyState(title,
  description, actionText, actionHref)` in `ui.templ` worth using as the
  starting shape.
- **Form field group** — the `form-control`/`label`/`label-text` wrapper
  plus an inline `<p class="text-error text-sm">{ err }</p>` validation
  message, reinvented per-field in `audience_score_system/web` (10+ sites
  across `outcomes/`, `schedule/`, `videos/`, `research/`, `matches/`) and
  in `tools/app_registry/ui`. `manmanv2/ui/components/ui.templ`'s
  `FormInput`/`FormSelect`/`FormTextarea` are already generic (pure daisyUI,
  no manmanv2 logic) and are the natural donor implementation.
- **Breadcrumbs** — `tools/app_registry/ui` has 7 near-identical call sites;
  `manmanv2/ui` already has a `Breadcrumbs([]Breadcrumb)` component
  (`components/layout.templ`) composed into `Shell`'s `Banner` slot — a
  reasonable reference for how a shared version would plug into `Shell`
  without needing a new dedicated field. `leaflab` only has this in design
  mockups (`ui/design/wireframes/*.html`), not real `.templ` yet — worth
  building before leaflab reinvents it independently.
- **`Confirm`'s missing click-to-reveal variant** — `manmanv2/ui` explicitly
  does *not* use `htmxui.Confirm` for its danger-zone delete actions
  (`game_detail.templ`, `workshop_addon_detail.templ`, `games.templ`,
  `workshop_library_detail.templ`; see `deployment_row.templ`'s own comment:
  Confirm's confirmation UI is "always-visible," not "click-to-reveal") and
  instead hand-rolls the same `x-data="{ confirmDelete: false }"` Alpine.js
  toggle 4+ times. This is a real gap in an *existing* primitive, not a
  missing one — `Confirm` was consolidated from three call sites that all
  wanted the always-visible shape, and a fourth, click-to-reveal shape has
  since appeared independently at least four more times without ever being
  offered `Confirm` as an option. Worth deciding deliberately (extend
  `Confirm` with a reveal mode, or name this as a second, distinct
  primitive) rather than leaving it as undocumented Alpine glue.
- **Definition-list label/value rows** (`DLItem`/`DLItemMono`/`DLItemCode`,
  `manmanv2/ui/components/ui.templ`) and a **card-wrapped vs. bare table**
  helper (`Table`/`TableInline`, same file) are already written generically
  in `manmanv2/ui` with no manmanv2-specific logic, but no second domain has
  adopted or reinvented them yet — list here as available donor
  implementations for whoever hits the second real need, not as confirmed
  htmxui candidates (§8 still applies: one implementation isn't a pattern
  yet).
- **Domain-owned `StatusXVariant` mapper** — not a component to extract, but
  a naming convention worth stating explicitly: a domain that needs to map
  its own status vocabulary onto `htmxui.BadgeVariant` should do it as one
  small, domain-owned `func StatusXVariant(status string) htmxui.BadgeVariant`
  (manmanv2's `ui.templ` does this), not by reinventing badge markup or
  colour classes directly. `audience_score_system` currently does the
  latter (three separate ad hoc badge-class switch functions in
  `access/views.templ`, `schedule/views.templ`,
  `pages/channel_detail.templ`) instead of adopting `htmxui.Badge` at all —
  logged as adoption debt, not a new primitive need.
- **Never render a control guaranteed to fail** — `tools/app_registry/ui`'s
  `components/gate.templ` (`GatedAction`/`GatedLinkAction`) encodes a
  repo-wide UX rule worth stating even though the component itself is tied
  to app-registry's own `GateDecision` type and isn't a drop-in htmxui
  candidate as-is: a caller that knows an action is denied renders it
  disabled with the reason as a `title` tooltip, never a live control that
  is guaranteed to fail server-side. A generalized `(allowed bool, reason
  string)` version of this shape would be htmxui-worthy if a second domain
  needs the same rule.
- **Active nav-link highlighting** — `whagent_net/ui`'s `navLink` helper is
  a verbatim duplicate of `manmanv2/ui/components/layout.templ`'s (its own
  comment says so); `leaflab/ui`'s nav has no active-state highlighting at
  all, a third, inconsistent variant. Since `Nav` is an app-owned `Shell`
  slot (§2), this can't become a `Shell` feature directly, but a small
  shared `htmxui.NavLink(href, label, active)` helper would remove the
  duplication and give leaflab an easy way to add what it's currently
  missing.
- **Confirmed non-issues** — no htmx-indicator/loading-spinner convention
  and no icon usage exist anywhere in the surveyed domains (all
  text/daisyUI-only) — not hidden conventions, just gaps nobody has hit
  yet. Table-shell wrapping (`overflow-x-auto` + `table table-sm`) is
  consistent everywhere it appears but thin enough (one or two classes)
  that formalizing it as a component is marginal; a documented convention
  is enough.

## 14. Tests assert on rendered markup, not snapshots

`htmxui_test.go` renders each component to a buffer and asserts on specific
emitted classes/attributes/substrings (e.g. `hasClass`, or a direct
`strings.Contains` check like `TestThemeSwitcher_NoLocationReload`), rather
than comparing against a full golden-file snapshot. This keeps tests
readable as a spec of the contract ("`ContainerWide` renders `xl:max-w-7xl`")
instead of an opaque diff that breaks on any incidental markup change. New
component tests should follow the same style: assert the specific behavioral
claim the doc comment makes, not the entire rendered string.
