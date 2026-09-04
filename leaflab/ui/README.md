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
  pages/                      # screen-level templ components — see "Screens" below
```

## Screens

| Route | Page | Notes |
|-------|------|-------|
| `/` | Home | Landing page. |
| `/boards` | Boards list | FR4, FR5 — every board, reporting state. |
| `/boards/{board_id}` | Board detail | FR6-FR9 — sensors, claim, rename, reading history. |
| `/sensors/{sensor_id}/history` | Sensor reading history | FR9. |
| `/admin/boards` | Admin ownership | FR11-FR14 — **admin-only**: lists every currently-owned board and lets an admin reassign or clear its ownership. The "Admin" nav link is hidden for a non-admin (presentation only); the real gate is `requireAdmin` on `leaflab-api`'s `ListOwnedBoards`/`ReassignBoardOwner`/`ClearBoardOwner`/`ListUsers` RPCs — a non-admin reaching this route directly gets a 403-style page (`pages.AdminForbidden`), and calling the RPCs directly (bypassing this UI) is denied server-side regardless of what this UI shows. Admin-only mistake-correction, not a self-service release action (C25 boundary) — there is no "release my board" affordance anywhere in this UI. |

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
default. Being authenticated is the only requirement most routes enforce;
the one exception is `/admin/boards` (see "Screens" above), which also
requires the admin role — enforced server-side by `leaflab-api`'s
`requireAdmin`, never re-derived locally by this UI.

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
