# App Registry UI — Architecture

This is a **real** architecture doc, not a link that resolves to nothing —
`manmanv2/ui/README.md` links an `ARCHITECTURE.md` that does not exist in
that package; this file exists so `tools/app_registry/ui/` doesn't repeat
that gap. See `ui/README.md` first for layout/setup; this file covers data
access, auth, and the deliberate deviations from the wireframes.

This UI **records** promotions; it never deploys anything (NFR-1). No
section below should be read as implying otherwise — writeback to gitops
is `tools/app_registry/worker/`'s job, not this UI's.

## Data access

### gRPC-only, with one documented exception (NFR-3)

Every piece of registry domain data (apps, artifacts, environments,
promotions, builds/release-runs) is read and written over gRPC against
`app-registry-api`, via the typed clients in `grpc_client.go`
(`RegistryClient.{Environment,App,Promotion,Artifact}`). The UI issues
**no SQL** against registry domain tables — no direct Postgres access, no
second copy of the data model.

The **one exception** is `libs/go/htmxauth`'s DB-backed session manager:
`PG_DATABASE_URL` is required at boot to store UI login sessions
(`ui_sessions` table, owned by `libs/go/htmxauth/migrations/900001_ui_sessions.up.sql`
and merged into this domain's migration sequence via `migrate.WithSource`
in `migrate/main.go`, rather than copied into this domain's own migrations).
This is session storage, not registry domain data — it never appears in a
handler that also talks to `app-registry-api`. Unlike `manmanv2/ui`, this
UI never falls back to cookie sessions; if the DB session manager's
preflight fails (table missing), startup fails loudly rather than
degrading to an insecure fallback (FR-58).

### Token forwarding, no service account

The UI holds no credentials of its own for registry data. Each protected
handler is wrapped by `withAccessToken` (`main.go`), which pulls the
logged-in user's access token out of their `htmxauth` session and injects
it into the outgoing request context via `grpcauth.WithUserToken`. The
gRPC client is dialed once at startup with a `grpcauth`
per-RPC-credentials dial option (`grpcauth.NewUserTokenDialOption`,
`main.go`) that reads that token back off the context on every call. Every
RPC the UI makes to `app-registry-api` therefore runs as the requesting
user, not as a shared UI identity (FR-40) — a user with no promoter role
for an environment gets exactly the same `PermissionDenied` from the
server the CLI would give them.

### As-of / SCD2 read pattern (FR-3, FR-7, FR-20)

Several screens (deployments matrix, environment diff) accept an optional
`at` query parameter — an RFC3339-ish instant parsed by `parseAsOf`
(`handlers_deployments.go`) — and pass it through as the SCD2 as-of
instant on `GetEnvironmentState`/equivalent RPCs. An empty value means
"now"; the server does the `valid_from <= at AND (valid_to IS NULL OR
valid_to > at)` filtering per `AGENTS.md`'s SCD2 convention, the UI never
reimplements it. The UI's own job is limited to: parsing the query param,
threading it through to the RPC, and rendering an explicit "Viewing
historical state as of `<time>` UTC" banner (`deployments.templ`) so a
past-state view can never be mistaken for live state.

## Auth and role gating

`components/roles.go`'s `HasRole` is a literal-membership check mirroring
`tools/app_registry/server/auth`'s enforcement-side design — no role
implies another. This is **presentation-only**: it decides whether a
button renders enabled, disabled-with-reason (`components.GatedAction`),
or omitted. The server (`libs/go/grpcauth`, `tools/app_registry/server/auth`)
remains the sole enforcement path (NFR-14) — every promoter-role check the
UI makes is advisory for UX; a request the UI would have blocked but that
somehow reaches the server anyway is still rejected there. See
`ui/README.md`'s "Roles" section for `Gate`/`EnvironmentPromoterRole`
details.

## UI-layer policies (labeled as such, not server guarantees)

Two rules render in this UI as validation but are **not** fully
server-enforced — do not read them as guarantees the server would also
give a non-UI caller (e.g. the CLI):

- **FR-13 — reason required for every environment, on both promote and
  rollback.** `runPromoteCommit` (`handlers_promote.go`) rejects an empty
  reason before calling `Promote`/rollback regardless of environment rank.
  The server (`promotion.go`) only enforces a non-empty reason **above
  rank 0** (`ARCHITECTURE.md "Authorization"` in the server's own doc set) —
  a dev-rank (rank 0) promotion with no reason is legal at the RPC level.
  This UI's stricter rule is a UX choice, not a contract other callers
  must honor.
- **FR-14 — dry-run-first above dev.** The UI disables "Promote for real"
  until a dry run has been run against the *exact* current form state
  (`promoteFingerprint`, `handlers_promote.go` — hashes owner/kind/env/
  artifact/override/reason so any edit invalidates the prior dry run).
  This is enforced only in the UI's own form flow; the `Promote` RPC
  itself accepts a direct (non-dry-run) call with no prior dry run on
  record.

## Actor identity (FR-19)

`actorFromCtx` (`tools/app_registry/server/handlers/promotion.go:528-533`)
writes `claims.Subject` onto `promotion_event.actor` — nothing more. In
practice this means the actor column mixes two different kinds of value
with no way to tell them apart structurally:

- promotions written by CI (GitHub Actions release workflows) — subject
  is a GitHub username-shaped string;
- promotions written by this UI — subject is a raw OIDC UUID.

There is **no user-directory RPC** to resolve a UUID to a human name.
`actorDisplay` (`pages/app_detail.templ`) renders exactly what the API
stored, with one narrow exception: it resolves the **current session's
own** subject to that user's `preferred_username` for a "that was you"
affordance — it never attempts to resolve anyone else's subject. Do not
go looking for (or add) a lookup for other users; there is nothing on the
server to back it, and adding one is out of this UI's scope.

## Recorded wireframe deviations (NFR-19)

Every deliberate difference from the wireframes in `design/wireframes/`,
with why:

### Screen 30 (Builds) — four dropped elements

The wireframe shows a recording-health badge, an artifact-count column,
row tinting, and a domain label per build row. None of these ship
(`pages/builds.templ`). `Build` (the proto message `ListBuilds` returns)
carries no artifact states, counts, or domain of its own — showing any of
them would require a `GetReleaseRun` call **per row** to fetch the
artifacts a build produced, i.e. an N+1 fan-out on a list screen backed by
exactly one `ListBuilds` call per page load. That tradeoff was rejected:
recording health is exactly what screen 31 (Build Detail) exists to show,
reached via "Open this run" from every row. Screen 30 stays a fast,
single-RPC index; screen 31 is where the detail (and its RPC cost) is
paid, once, for the run the operator actually cares about.

### Screen 31 (Build Detail) — zero-artifact indeterminate verdict

When `GetReleaseRun` returns zero artifacts for a workflow run, the screen
cannot distinguish "recording opt-in was off for this app/chart" from
"recording failed silently" — the registry has no signal that
distinguishes the two (NFR-15 forbids guessing a state the data doesn't
support). Rather than pick one and risk rendering a failure as healthy
(NFR-6), the screen renders an explicit **indeterminate** verdict and a
GitHub Actions deep link (`githubActionsRunURL`, `handlers_builds.go`) so
the operator can go check CI directly for the ground truth this registry
cannot supply.

### Chart detail: `chart_app` vs. `artifact_link` shown separately

`chart_detail.templ` renders two distinct sections rather than merging
them: the chart's **currently declared composition** (`chart_app` —
"what does this chart declare *today*", from `ListCharts`' `app_ids`, a
live/mutable fact) and the **build-time pins** (`artifact_link` — what one
specific, already-recorded chart *version* actually shipped, frozen at
build time). These answer different questions and merging them would
silently misattribute a chart version's frozen composition to whatever the
chart declares right now, or vice versa. See
`design/CONCEPTS_AUDIT.md` #10 for the full concept-level distinction this
mirrors.

### Screen 52 (Adopt) — not implemented

The adopt-artifact write action is deferred; only the read-side audit
(screen 40, Drift & Audit) is implemented. `drift_audit.templ` and
`handlers_drift.go` say so explicitly in-page ("No adopt control lives
here — recording a new adoption is screen 52, delivered separately") and
in code comments, so nobody mistakes the audit screen for a place to
adopt from. There is no scope in this plan (#629) that reintroduces it —
if screen 52 is picked up, it is a new issue, not an extension of screen
40.

## Design system

See `ui/README.md`'s "Design system" and "Styling" sections for the badge
vocabulary (one colour/label per
`Promotability`/`ArtifactState`/`ArtifactProvenance` value), the
troubleshooting-vs-calm-day density convention, and the daisyUI +
`htmxui.ThemesCSS` load-order constraint (NFR-10, NFR-11, NFR5).

This UI is a **consumer** of `//libs/go/htmxui` (issue #1005, FR2), not the
owner of its own daisyUI primitives: `components.Shell` is a thin wrapper
around `htmxui.Shell` supplying this app's nav/banner slots plus a
`HeaderRight` slot composed from `htmxui.UserMenu` (issue #1010, FR8) — the
shared identity + logout dropdown that gave app-registry its first visible
logout control — and `htmxui.ThemeSwitcher`,
`components/badges.templ` expresses this app's badge vocabulary in terms of
the generic `htmxui.Badge`, and the confirm/danger-zone pattern (screens
50-promote, 51-rollback, 53-environment-form) routes through the shared
`htmxui.Confirm`. `libs/go/htmxui/README.md` documents the shared
primitives themselves; this file and `ui/README.md` document only how
app-registry composes them.
