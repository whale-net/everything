# App Registry UI

HTMX-based admin UI for the App Registry. It records promotions — it never
deploys anything (NFR-1). Structural pattern mirrors `manmanv2/ui/`
(`main.go`, `pages/`, `components/`, `templ_render.go`, the `templ_library`
Bazel macro), but this is the repo's **first daisyUI adopter**; styling
deliberately differs from `manmanv2/ui`'s Tailwind-only approach.

## Layout

```
tools/app_registry/ui/
  main.go            # config (env vars only), App wiring, HTTP server, graceful shutdown
  grpc_client.go      # typed app-registry-api client forwarding the user's own token
  roles.go            # HasRole: literal, presentation-only role membership check
  templ_render.go     # wraps a templ.Component in libs/go/htmxbase's layout
  handlers_home.go    # the one placeholder authenticated screen
  themes.css           # synced copy of tools/wireframe/themes.css (see below)
  components/          # shared chrome (navbar shell)
  pages/                # screen-level templ components
```

## Auth

Uses `libs/go/htmxauth`'s **DB-backed** session manager exclusively —
unlike `manmanv2/ui`, this UI never falls back to cookie sessions.
`PG_DATABASE_URL` is required at boot; if the `ui_sessions` table is
missing, the DB session manager's preflight (`libs/go/htmxauth`, FR-58)
fails startup with a message naming the table and the migration that owns
it (`tools/app_registry/migrate/schema/migrations/011_ui_sessions.up.sql`).

`AUTH_MODE=none` runs without Keycloak; the developer is treated as
holding every role (`htmxauth.AllRoles` sentinel). See `ENV.md`.

## gRPC

All registry domain data comes from `app-registry-api` over gRPC,
forwarding the logged-in user's own access token via `libs/go/grpcauth`
(`grpc_client.go`). No service account, no credentials of the UI's own for
registry data. The UI's only direct database use is `htmxauth` session
storage — it never issues SQL against registry domain tables.

## Roles

`libs/go/htmxauth` surfaces the session's realm roles on `UserInfo.Roles`.
`roles.go`'s `HasRole` is a literal-membership check — no role implies
another, matching `tools/app_registry/server/auth`'s enforcement-side
design. This check is presentation-only; the server (`libs/go/grpcauth`,
`tools/app_registry/server/auth`) remains the sole enforcement path
(NFR-14).

## Styling

- Tailwind's browser build (`@tailwindcss/browser@4.3.3`) and daisyUI
  (`daisyui@5.6.18`) load from pinned CDN URLs — no Node, no bundler, no
  CSS toolchain (NFR-20). The image build stays a pure Go cross-compile.
- `themes.css` supplies the daisyUI theme-variable overrides. Go's
  `//go:embed` directive cannot reach across a Bazel package boundary, so
  `themes.css` here is a **synced literal copy** of
  `tools/wireframe/themes.css`, not a shared reference. Keep the two
  byte-for-byte identical when either changes; `themes.css`'s own header
  comment repeats this.
- **Trap:** `libs/go/htmxbase/layout.go` renders its `CustomCSS` slot
  *before* `CustomHead`. `themes.css` must load after daisyUI's
  stylesheet, so `templ_render.go` puts the daisyUI `<link>` and the
  `themes.css` `<style>` both in `CustomHead` (which renders last), in
  that order. Splitting them across `CustomCSS`/`CustomHead` silently
  yields daisyUI's default palette with no error.

## Generated templ files

Like `manmanv2/ui`, this package **commits** the generated `*_templ.go`
files (`components/layout_templ.go`, `pages/home_templ.go`) alongside their
`.templ` sources, for IDE/`gopls` support without invoking Bazel. Bazel
itself regenerates these files at build time via the `templ_library` macro
(`tools/templ.bzl`) regardless of what's checked in — the committed copies
are for human/editor convenience only, and must be regenerated (`bazel
build //tools/app_registry/ui/...` and copy the `bazel-bin` output back)
whenever the corresponding `.templ` file changes.

## Local development

```bash
AUTH_MODE=none
GRPC_AUTH_MODE=none
SECRET_KEY=dev-secret
REGISTRY_API_URL=localhost:50051
PG_DATABASE_URL=postgres://user:pass@localhost:5432/app_registry?sslmode=disable
```

See `ENV.md` for the full variable list.
