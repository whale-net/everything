# LeafLab UI

HTMX/templ web app for LeafLab (M1). Signs a user in via OIDC (FR1), keeps
them signed in across visits with a server-side session (FR3), resolves
them to a local `leaflab_user` row on interactive sign-in (FR2), and calls
`leaflab-api` on their behalf, forwarding the signed-in user's own access
token (NFR2). Structural pattern mirrors `manmanv2/ui/` and
`tools/app_registry/ui/` (`main.go`, `pages/`, `components/`,
`templ_render.go`, the `templ_library` Bazel macro).

## Layout

```
leaflab/ui/
  main.go            # config (env vars only), App wiring, HTTP server, graceful shutdown
  grpc_client.go      # typed leaflab-api client forwarding the user's own token
  templ_render.go      # wraps a templ.Component in libs/go/htmxbase's layout
  handlers_home.go      # the "/" landing page
  components/               # shared chrome (Layout — thin wrapper around libs/go/htmxui's Shell/UserMenu)
  pages/                      # screen-level templ components (Home for M1; boards/sensor screens are separate tasks)
```

## Auth (FR1, FR3, NFR3)

Uses `libs/go/htmxauth`'s **DB-backed** session manager exclusively — like
`tools/app_registry/ui`, unlike `manmanv2/ui`, this UI never falls back to
cookie-only sessions. `PG_DATABASE_URL` is required at boot; if the
`ui_sessions` table is missing, the DB session manager's preflight
(`libs/go/htmxauth`) fails startup with a message naming the table and the
migration that owns it
(`leaflab/migrate/migrations/014_ui_sessions.up.sql`).

`AUTH_MODE=none` runs without an OIDC provider, for local development —
see `leaflab/Tiltfile`, which sets `AUTH_MODE=none` and `GRPC_AUTH_MODE=none`
for `leaflab-ui`, matching `leaflab-api`'s own unauthenticated local-dev
default. Being authenticated is the **only** requirement M1 enforces on any
route — no role, grant, or ownership check gates anything here.

## Identity resolution (FR2)

On successful interactive sign-in, this UI upserts a `leaflab_user` row
keyed on the OIDC `sub` claim: created on first sign-in, reused on every
later sign-in. No service caller, background job, or gRPC handler mints a
row — only interactive sign-in does, and signing in claims nothing (no
board, region, or plant ownership is created or implied).

## gRPC (NFR2)

All board/sensor domain data comes from `leaflab-api` over gRPC, forwarding
the logged-in user's own access token via `libs/go/grpcauth`
(`grpc_client.go`). This UI's only direct database use is `htmxauth`
session storage plus the `leaflab_user` upsert above — it never issues SQL
against `board`, `sensor`, `sensor_reading`, or any `v_*` view.

## Local development

```sh
cd leaflab && tilt up
```

The UI is forwarded to `http://localhost:8080`. See [ENV.md](ENV.md) for
every environment variable.
