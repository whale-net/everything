# htmxui

Shared UI primitives (chrome, themes, reusable components) for HTMX-based
applications in this monorepo. This package holds only cross-app primitives
— never app-specific screens. App-specific UI stays in each consuming app's
own package (e.g. `tools/app_registry/ui`, `manmanv2/ui`).

This is Phase 1 groundwork (part of #998, FR1): no consuming app has been
migrated to these primitives yet (that is issue #1005's job) -- the
primitives below exist here but are not yet adopted anywhere.

## Chrome primitives (FR1 / FR1a / FR5 / FR8)

- **`Shell(data ShellData)`** (`shell.templ`) — the shared navbar+main
  chrome: navbar frame, `ThemeSwitcher` mount point, user-identity label
  slot, banner slot, and main content region. **Not** a literal lift of
  `tools/app_registry/ui/components/layout.templ`'s `Shell()`, which
  hardcodes its nav inline (FR1a) — `ShellData.Nav`, `.Banner`, and
  `.HeaderRight` are app-owned `templ.Component` slots the caller supplies
  (nav item list, e.g. app-registry's `MisconfigBanner`, and top-right
  extras like manmanv2's `ServerSelector`, respectively); Shell hardcodes
  none of them. `ShellData.Themes` drives the `ThemeSwitcher` mount and
  `UserLabel` is plain display text — Shell has no dependency on
  `//libs/go/htmxauth` or any auth-specific user type. See `ShellData`'s
  doc comment for the full app-owned/shared boundary.
- **`ThemeSwitcher(themes []Theme)`** (`theme_switcher.templ`) — the
  shared, CSS-variable/daisyUI `data-theme` switcher (FR5, shared side).
  Renders one control per caller-supplied `Theme{Value, Label}` and an
  inline script that re-applies the stored theme on every page load and
  writes new selections to `localStorage` (key: exported
  `ThemeSwitcherStorageKey`) — **without ever calling
  `location.reload()`**, unlike manmanv2's current
  `manmanv2/ui/components/layout.templ` `themeManagement()` script.
  Requires the consuming app to have injected `htmxui.ThemesCSS` per the
  NFR5 load-order rule (see `ThemesCSS`'s doc comment in `themes.go`).
  `themes.css` currently defines four palettes: `light`, `night`, `oled`,
  and `sunset` (a Phase 3 proof-of-concept theme, #998 FR10 — a warm
  amber/coral palette added to prove the switcher plumbing generalizes
  past the original three; not a long-term design commitment). A caller's
  `[]Theme` list may offer any subset.

- **`UserMenu(data UserMenuData)`** (`user_menu.templ`) — the shared
  top-right identity + logout component (FR8). Renders a single daisyUI
  `dropdown`/`menu` control showing `UserMenuData.IdentityLabel` plus a
  logout link to `UserMenuData.LogoutHref` (a parameter, never a hardcoded
  `/auth/logout` — both apps happen to register that route today via
  `htmxauth.Authenticator.HandleLogout`, but UserMenu must not assume it).
  Empty `IdentityLabel` (e.g. `AUTH_MODE=none`) renders nothing, matching
  `ShellData.UserLabel`'s existing convention. Takes no
  `//libs/go/htmxauth` dependency and no app-specific parameters — extras
  like manmanv2's `ServerSelector` compose *alongside* `UserMenu` (e.g. via
  `templ.Join`) into a single component passed as `ShellData.HeaderRight`,
  not folded into `UserMenu` itself. One markup shape covers both
  desktop and narrow/mobile presentation (a collapsed button expanding a
  dropdown needs no width-specific variant), unlike manmanv2's current
  approach of a separately-marked-up mobile copy of its logout link. Not
  yet adopted by either app — see `UserMenu`'s doc comment for the full
  design rationale; adoption is issues #1010 (app-registry) and #1011
  (manmanv2).

## Content primitives (FR1 / FR1b)

Each is a `.templ` component in `libs/go/htmxui`, daisyUI-classes-only
(NFR1), with no proto/app dependency:

- **`Button(variant ButtonVariant, size ButtonSize, disabled bool, attrs templ.Attributes)`**
  (`button.templ`) — the shared `btn` primitive. `ButtonVariant` covers the
  manmanv2 design-standard semantics (`ButtonPrimary`, `ButtonSecondary`,
  `ButtonSuccess`, `ButtonWarning`, `ButtonError`, `ButtonNeutral`,
  `ButtonGhost`); `ButtonSize` covers `ButtonSizeXS/SM/MD/LG`. `attrs` is the
  escape hatch for `type`, `name`/`value`, `hx-*`, and `title`.
- **`Badge(label string, variant BadgeVariant, size BadgeSize, soft bool, attrs templ.Attributes)`**
  (`badge.templ`) — a generic single-label, single-colour `badge` primitive.
  Deliberately repo-generic: app-registry's promotability/artifact-state/
  provenance vocabulary in `tools/app_registry/ui/components/badges.templ`
  stays domain-owned and is expressed in terms of this `Badge` in the
  adoption issue, keeping its labels and colours unchanged.
- **`Card(header, body, actions templ.Component, attrs templ.Attributes)`**
  (`card.templ`) — the shared `card bg-base-100 border border-base-300
  shadow-sm` chrome with three composition slots; a `nil` slot renders
  nothing, `actions` wraps in `card-actions justify-end`.
- **`Confirm(p ConfirmProps, submitAttrs templ.Attributes)`** (`confirm.templ`)
  — the confirm/danger-zone action primitive. **Net-new consolidation, not
  extraction (FR1b):** it is the first single implementation of a pattern
  previously repeated independently in `tools/app_registry/ui/pages/
  promote.templ`, `rollback.templ`, and `environment_form.templ`. See
  `ConfirmProps`' doc comment for how each call site's needs map onto its
  fields (`ZoneTitle` for the danger-zone card chrome, `Summary`, a
  children slot for the typed-confirmation/explicit-acknowledge control,
  `Disabled`/`DisabledReason`, `CancelHref`). The `<form>` element itself
  stays app-owned; `submitAttrs` forwards onto the submit `<button>` (e.g.
  an `hx-post` target).

## BUILD shape: `templ_library`, not `go_library`

`libs/go/htmxui` is a **`templ_library`**, loaded from `//tools:templ.bzl`
— the first `templ_library` under `libs/go/`. This is deliberate and
diverges from its directory siblings `libs/go/htmxauth` and
`libs/go/htmxbase`, which are plain `go_library` rules because they contain
no `.templ` sources.

```python
load("//tools:templ.bzl", "templ_library")

templ_library(
    name = "htmxui",
    srcs = glob(["*.templ"]),
    go_srcs = ["doc.go"],       # non-generated .go files
    visibility = ["//visibility:public"],
    deps = [],
)
```

`templ_library` (defined in `tools/templ.bzl`) genrules each `.templ` file
into a `<name>_templ.go` file via the `templ` CLI, then wraps the generated
files plus `go_srcs` in a `go_library`. The `importpath` is derived
automatically from `native.package_name()`, so this package's Go import
path is `github.com/whale-net/everything/libs/go/htmxui`.

Because this shape is hand-written (not gazelle-generated) and the rule is
named `templ_library` rather than `go_library`, gazelle would otherwise try
to rewrite or duplicate it. The `# keep` marker in `BUILD.bazel` on the
`templ_library(...)` rule tells `bazel run //:gazelle` to leave it alone.

## Depending on this package

Add `//libs/go/htmxui` to your target's `deps`. Visibility is
`//visibility:public` (FR1, user story 10) so any UI — `//tools/wireframe`,
`//tools/app_registry/ui/...`, `//manmanv2/ui/...`, and any future third UI
— can depend on it directly:

```python
templ_library(
    name = "my_ui",
    srcs = glob(["*.templ"]),
    deps = [
        "//libs/go/htmxui",
        # ...
    ],
)
```

## No bundler assets (NFR1)

This package ships no Node/npm/bundler files and introduces no CSS build
toolchain — there is no `package.json` here, and there won't be one.
Tailwind and daisyUI arrive at the browser via each consuming app's own
pinned CDN `<link>`/`<script>` tags; `htmxui` supplies Go/templ code that
assumes those utility classes exist, not the CSS itself.
