# App Registry UI

HTMX-based admin UI for the App Registry. It records promotions — it never
deploys anything (NFR-1). Structural pattern mirrors `manmanv2/ui/`
(`main.go`, `pages/`, `components/`, `templ_render.go`, the `templ_library`
Bazel macro), but this is the repo's **first daisyUI adopter**; styling
deliberately differs from `manmanv2/ui`'s Tailwind-only approach.

See [ARCHITECTURE.md](ARCHITECTURE.md) for data-access patterns (gRPC-only
with the one `htmxauth` session-DB exception, token forwarding, the
as-of/SCD2 read pattern), presentation-only role gating, the FR-13/FR-14
UI-layer policies, the FR-19 actor-identity note, and every recorded
wireframe deviation with its reason — that's a **real** doc, unlike
`manmanv2/ui/README.md`'s link to the same filename, which resolves to
nothing.

## Layout

```
tools/app_registry/ui/
  main.go                  # config (env vars only), App wiring, HTTP server, graceful shutdown
  grpc_client.go            # typed app-registry-api client forwarding the user's own token
  templ_render.go           # wraps a templ.Component in libs/go/htmxbase's layout; also logs FR-59
  deployments_data.go       # buildDeploymentMatrix: the N concurrent GetEnvironmentState fan-out (FR-7)
  handlers_dashboard.go     # screen 01-dashboard ("/")
  handlers_deployments.go   # screen 10-environment-status ("/deployments")
  components/                # shared chrome, vocabulary, and role-gate helpers (see "Design system")
    layout.templ               # Shell: thin wrapper around libs/go/htmxui's Shell (nav items, FR-59 banner slot)
    badges.templ                # PromotabilityBadge / ArtifactStateBadge / ProvenanceBadge — the one definition of each, over htmxui.Badge
    digest.templ                 # DigestDisplay: truncated + copyable digest, shown alongside version
    banner.templ                  # MisconfigBanner: the FR-59 persistent banner
    gate.templ                     # GatedAction / GatedLinkAction: standard "unavailable action" rendering
    roles.go                        # HasRole, EnvironmentPromoterRole(s), RolesMisconfigured, Gate, GateCellAction
  matrix/                     # pure-data FR-4/FR-5 row/column shaping shared by dashboard + deployments (no gRPC calls)
  pages/                      # screen-level templ components
    dashboard.templ             # screen 01
    deployments.templ           # screen 10 ("Deployments" in nav)
```

## Auth

Uses `libs/go/htmxauth`'s **DB-backed** session manager exclusively —
unlike `manmanv2/ui`, this UI never falls back to cookie sessions.
`PG_DATABASE_URL` is required at boot; if the `ui_sessions` table is
missing, the DB session manager's preflight (`libs/go/htmxauth`, FR-58)
fails startup with a message naming the table and the migration that owns
it (`tools/app_registry/migrate/schema/migrations/012_ui_sessions.up.sql`).

`AUTH_MODE=none` runs without Keycloak; the developer is treated as
holding every role (`htmxauth.AllRoles` sentinel). See `../ENV.md`.

## gRPC

All registry domain data comes from `app-registry-api` over gRPC,
forwarding the logged-in user's own access token via `libs/go/grpcauth`
(`grpc_client.go`). No service account, no credentials of the UI's own for
registry data. The UI's only direct database use is `htmxauth` session
storage — it never issues SQL against registry domain tables.

## Roles

`libs/go/htmxauth` surfaces the session's realm roles on `UserInfo.Roles`.
`components/roles.go`'s `HasRole` is a literal-membership check — no role
implies another, matching `tools/app_registry/server/auth`'s
enforcement-side design. This check is presentation-only; the server
(`libs/go/grpcauth`, `tools/app_registry/server/auth`) remains the sole
enforcement path (NFR-14).

`EnvironmentPromoterRole(envKey)` derives the per-environment promoter role
(`app-registry-promoter-<environment_key>`) — always call it with a key
from a live `ListEnvironments` result (`EnvironmentPromoterRoles` does this
for a whole response), never a hardcoded dev/stage/prod list, since
environments are operator-defined data.

`Gate(user, role)` returns a `GateDecision{Allowed, MissingRole}`. Screens
render denied actions with `components.GatedAction` (disabled, with the
missing role named) or omit them outright — never as an enabled control
guaranteed to be denied (FR-44).

## Design system (NFR-10, NFR-11)

`components/` is the single source of truth for the value vocabulary every
screen reuses — no screen may define a second, inline badge mapping:

- **`badges.templ`**: one colour + label per `Promotability`,
  `ArtifactState`, and `ArtifactProvenance` value. `ProvenanceBadge` must be
  rendered next to every artifact shown on any screen, not only an audit
  screen — `ADOPTED` is a visually distinct badge from `OBSERVED` (FR-33).
  `VIA_CHART` promotability is a visually distinct colour from
  `PROMOTABLE` — a `VIA_CHART` promotion is a legal but different kind of
  promotion (direct-vs-through-chart), not a lesser one.
- **`digest.templ`**: `DigestDisplay(version, digest)` is the one place a
  digest renders anywhere "what's deployed" is shown — truncated for
  density, full value on hover (`title`) and via a one-click copy button
  (NFR-4).
- **`banner.templ`**: `MisconfigBanner` — see "FR-59 misconfiguration
  banner" below.
- **`gate.templ`**: `GatedAction` — see "Roles" above.

**Density convention:** troubleshooting screens (artifact/promotion audit,
run logs) use dense monospace tables — pack information, minimize
whitespace, favour `DigestDisplay`'s truncated form. Calm-day screens
(dashboard, deployments) use daisyUI cards with generous spacing —
`components.Shell`'s default `max-w-4xl space-y-6` wrapper is that calm-day
baseline; a troubleshooting screen should widen the container and drop the
vertical `space-y` rhythm rather than reuse the card-per-section layout.

Badge/status colours map through daisyUI 5 semantic classes (`badge-
success`, `badge-error`, etc.), themed by `htmxui.ThemesCSS` (see "Styling"
below) — never a raw hex colour or a Tailwind colour utility, so a theme
change updates every badge for free.

## FR-59 misconfiguration banner

`components.RolesMisconfigured(user)` distinguishes:

- **roles claim absent** (`user.Roles == nil`) — almost always a deployment
  misconfiguration (missing Keycloak "Add to ID token" realm-roles mapper).
  `components.MisconfigBanner` renders a persistent, explicit error banner
  naming the probable cause on **every** screen (it's rendered once, inside
  `components.Shell`), `templ_render.go`'s `RenderTempl` logs it at error
  level on every render, and every read screen still renders underneath the
  banner — an admin can diagnose without being locked out. This must never
  render as a read-only session.
- **roles claim present but empty** (`user.Roles != nil, len == 0`) — a
  legitimate read-only viewer. No banner.

**Trap (do not rediscover):** this is derived from the loaded
session/`UserInfo` record at render time, not from auth middleware.
`AuthModeNone` short-circuits inside the authenticator
(`libs/go/htmxauth/auth.go:158`, `:206`) and sets `Roles` to the non-nil
`AllRoles` sentinel, so deriving the check from the loaded record (as
`RolesMisconfigured` does) gets the `AUTH_MODE=none` exemption for free —
no separate mode branch needed here.

## Styling

- Tailwind's browser build (`@tailwindcss/browser@4.3.3`) and daisyUI
  (`daisyui@5.6.18`) load from pinned CDN URLs — no Node, no bundler, no
  CSS toolchain (NFR-20). The image build stays a pure Go cross-compile.
- Shared UI chrome and primitives come from `//libs/go/htmxui` (issue
  #1005, FR2): `components.Shell` is a thin app-registry wrapper around
  `htmxui.Shell`, supplying this app's own nav list, `MisconfigBanner`, and
  user-identity label as `htmxui.Shell`'s app-owned slots; `badges.templ`
  expresses this app's promotability/artifact-state/provenance vocabulary
  in terms of the generic `htmxui.Badge` primitive; and the three
  previously-independent confirm/danger-zone blocks in `pages/promote.templ`,
  `pages/rollback.templ`, and `pages/environment_form.templ` now all route
  through the single shared `htmxui.Confirm` component. This package no
  longer maintains its own local Shell/badge/confirm implementations.
- `htmxui.ThemesCSS` (`//libs/go/htmxui`, `themes.css` embedded via
  `//go:embed`) is the single source of truth for the daisyUI
  theme-variable overrides — **not** a copy maintained here. `RenderTempl`
  in `templ_render.go` renders it directly (`<style>{htmxui.ThemesCSS}</style>`).
- **Trap (NFR5 — do not rediscover):** `libs/go/htmxbase/layout.go` renders
  its `CustomCSS` slot *before* `CustomHead`. `htmxui.ThemesCSS` must load
  after daisyUI's stylesheet, so `templ_render.go` puts the daisyUI
  `<link>` and the `htmxui.ThemesCSS` `<style>` both in `CustomHead` (which
  renders last), in that order — never `CustomCSS`. Splitting them across
  `CustomCSS`/`CustomHead` silently yields daisyUI's default palette with
  no error. `templ_render_test.go`'s `TestRenderTempl_ThemesCSSLoadsAfterDaisyUILink`
  guards this directly.

## Generated templ files

This package does **not** commit the generated `*_templ.go` files.
Bazel regenerates them at build time via the `templ_library` macro
(`tools/templ.bzl`), which runs `templ generate` in a genrule regardless
of what's on disk — `*_templ.go` is gitignored. For IDE/`gopls` support
outside Bazel, run `bazel build //tools/app_registry/ui/...` and copy the
`bazel-bin` output back locally, or run `templ generate` directly; either
way, do not check the result in.

## Local development

```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
SECRET_KEY=dev-secret
REGISTRY_API_URL=localhost:50051
PG_DATABASE_URL=postgres://user:pass@localhost:5432/app_registry?sslmode=disable
```

See `../ENV.md` for the full variable list.
