# ManManV2 UI

Type-safe, component-based UI built with Go + templ + HTMX + Tailwind CSS.

## Quick Start

### Development Workflow

1. **Write templ files**:
   ```bash
   # Create component
   vim components/ui/mycomponent.templ
   ```

2. **Generate Go code**:
   ```bash
   cd components/ui
   ~/go/bin/templ generate
   ```

3. **Build with Bazel**:
   ```bash
   bazel build //manmanv2/ui/...
   ```

### Creating a New Page

1. **Define types** in `types/`:
   ```go
   type MyPageData struct {
       Layout LayoutData
       Items  []*manmanpb.Item
   }
   ```

2. **Create template** in `pages/`:
   ```templ
   package mypage
   
   templ List(data types.MyPageData) {
       @layout.Base(data.Layout) {
           @layout.Hero("Title", "Subtitle")
           <!-- content -->
       }
   }
   ```

3. **Generate and build**:
   ```bash
   cd pages/mypage
   ~/go/bin/templ generate
   bazel build //manmanv2/ui/pages/mypage:mypage
   ```

## One-Click Deployment Actions (M2, C21)

The `/sessions` list renders a "Game Server Containers (GSCs) Status" table
(`pages.GSCStatusTable`/`pages.DeploymentRow`) with Start/Stop/Restart
controls directly on each row -- no navigation to a detail page is required
for any of the three actions.

**Endpoints** (`manmanv2/ui/handlers_deployment_actions.go`):

- `POST /sessions/deployments/{sgcID}/start` -- starts a session for the
  deployment. Always calls the existing `StartSession` RPC with
  `force=false`; the `env_template` used is whatever the `ServerGameConfig`
  already has. **Env-var layering is not wired in**: `ConfigurationPatch`/
  `env_vars` overrides are out of scope for M2 -- see "M3 boundary" below.
- `POST /sessions/deployments/{sgcID}/stop` -- resolves the deployment's
  current live session (via `ListSessions` with `LiveOnly: true`) and stops
  it. A deployment with no live session any more (raced with a crash/stop)
  is not an error -- it renders an inline notice, and `StopSession` is never
  called. The `StopSession` RPC itself is wrapped in a bounded
  `context.WithTimeout` (`app.deploymentActionTimeout`, defaulting to 8s --
  comfortably under `main.go`'s 15s `http.Server.WriteTimeout`) as
  defense-in-depth (#1664): a command that times out renders a distinct
  inline "taking longer than expected" message rather than a generic
  failure, or -- if the host/API layer ever regresses on its fast-ack
  behavior -- a dropped connection.
- `POST /sessions/deployments/{sgcID}/restart` -- dispatches a single
  `RestartDeployment` RPC to control-api (#1730) and returns as soon as
  it's been durably recorded, wrapped in the same bounded
  `context.WithTimeout` (`app.deploymentActionTimeout`) as Stop/Start
  above. The UI holds **no restart state** of its own to *orchestrate* a
  restart with: control-api's own consumer (#1731) owns waiting for the old
  session to actually stop and then starting the new one, entirely
  server-side, so killing the `manmanv2/ui` pod immediately after a restart
  click no longer strands the deployment stopped. A response with
  `already_in_flight: true` is a success, not an error -- it means a
  restart was already running for this deployment (a double click, or the
  operator retrying after a pod restart) -- and renders the same
  transitional row with no inline error. The UI does, however, *read* that
  server-side state back out for display -- see "Restart State Visibility"
  below.
  An RPC error or a bound timeout renders the usual inline error (FR8). The
  row's own self-terminating poll (see below) picks up convergence from
  "stopping" through to stopped/crashed or starting/running with no
  additional client logic. (Previously this endpoint was a client-side
  stop-then-start with the wait-then-start step finished in a background
  goroutine -- removed by #1733 once control-api took over that
  orchestration.)
- `GET /api/deployments/{sgcID}/row` -- returns just that deployment's
  `<tr id="deployment-row-{sgcID}">` fragment (`pages.DeploymentRow`), never
  a full page. This is the target of the row's own self-terminating poll
  (see below) and is reused by the three action endpoints above to re-render
  the row after an action.

All four endpoints require the same `RequireAuthFunc`/`WithAccessToken`
auth wrapping as every other protected route -- Admin and Server Manager get
identical behavior; there is no separate authz check for these actions.

**Swap target convention**: every action form/`hx-post` control targets
`hx-target="#deployment-row-{sgcID}"` with `hx-swap="outerHTML"`, so a
successful (or failed) action response replaces just that row in place.
`{sgcID}` is always the numeric `ServerGameConfigId`, matching the `id`
`pages.DeploymentRow` renders on its own `<tr>`.

**Self-terminating poll**: while a deployment's latest session is in a
transient status (`pending`, `starting`, `stopping` --
`components.IsTransientStatus`), its row carries
`hx-trigger="every 3s"`/`hx-get="/api/deployments/{sgcID}/row"`/
`hx-target="this"`/`hx-swap="outerHTML"`. Because the poll's own response is
itself a freshly-rendered `pages.DeploymentRow`, a row that has settled
(`running`, `stopped`, `crashed`, `lost`) comes back with no `hx-trigger` at
all and the polling loop stops on its own -- no client-side timer
bookkeeping required.

**M3 boundary**: restart's start step and the plain Start endpoint both call
`StartSession` with the `ServerGameConfig`'s existing `env_template`
unmodified -- `ConfigurationPatch`/`env_vars` overrides are **not** resolved
or applied by any M2 action. That work is scoped to M3/C22; see
[`manmanv2/docs/DESIGN_SGC_ENV_OVERRIDES.md`](../docs/DESIGN_SGC_ENV_OVERRIDES.md).

## Restart State Visibility (FR12, #1735)

Moving restart orchestration server-side (#1733) means a failure after the
`RestartDeployment` RPC returns is no longer visible inline the way the old
client-side background goroutine's error was. FR12 requires that durable
restart not make failure *less* visible than that -- so the deployment row
reads back control-api's own durable restart record (`pending_restarts`,
#1729-#1732) and renders a badge distinguishing "in progress" from
"stalled"/"failed", never a single generic "restart" indicator:

| `pending_restarts.status` | Badge | Meaning |
|---|---|---|
| `pending` | "Restarting" (info) | The Stop dispatched by `RestartDeployment` hasn't reached a terminal status yet; the deferred Start hasn't been claimed. |
| `started` | *(none)* | The record was claimed and the deferred Start was dispatched -- the normal session-status badge (`starting`/`running`) already tells this story. |
| `failed` | "Restart failed" (error) | The Stop dispatch or the deferred Start itself failed. The failure reason is the badge's `title` tooltip. |
| `expired` | "Restart stalled" (warning) | The gating Stop never reached a terminal status before the reaper's (#1732) stall deadline, so the Start was never dispatched. |
| *(no record)* | *(none)* | No restart in flight or recently resolved for this deployment. |

The mapping lives in `components.RestartBadge` (`manmanv2/ui/components/restart_state.go`)
so it is unit-testable independent of template rendering.

**Read path, one batched RPC per render**: `ControlClient.ListPendingRestarts`
(`manmanv2/ui/grpc_client.go`) wraps control-api's `ListPendingRestarts` RPC,
which takes every rendered SGC id in one call -- never a per-row RPC. Both
row-building paths populate `pages.DeploymentRowData.RestartState` from it:
`handleSessions` (`handlers_sessions.go`, the full-page render) and
`buildDeploymentRowData` (`handlers_deployment_actions.go`, the single-SGC
path shared by the #1628 poll, the three action endpoints, and -- via
`handleDeploymentsLiveSSE` -- the #1724 SSE fragment). A `ListPendingRestarts`
failure is not fatal to the row/page: it logs at WARNING and leaves
`RestartState` nil, the same degradation posture as the live-session
fallback above.

**Terminal states age out**: control-api's `GetLatestBySGCIDs`
(`manmanv2/api/repository/postgres/pending_restart.go`) excludes a resolved
(`failed`/`expired`) record more than `pendingRestartVisibilityWindow` (5
minutes) old, so a stale failure badge doesn't sit indefinitely on a
deployment that has since been restarted by hand. Unresolved (`pending`/
`started`) records are never excluded by the window.

**NFR11 byte-stability**: this content renders inside the SSE-pushed row
fragment (#1724), so the badge never renders a relative/formatted
timestamp -- only the four static (status, label, title) combinations
above. Two consecutive renders of an unchanged `RestartState` are
byte-identical, preserving the no-swap guarantee `libs/go/htmxsse/README.md`
documents.

**Read-only**: this is a display concern only. No UI code writes to
`pending_restarts` -- that stays entirely on control-api's `RestartDeployment`
handler and its consumer/reaper.

## Live Row Updates over SSE (#1726)

`/sessions` opens one SSE connection (`GET /api/live/deployments`, the
`handleDeploymentsLiveSSE` handler in `handlers_sessions_live.go`, #1724)
that keeps every visible deployment row current with no reload -- including
rows in a transient status and rows another user's action changed. The
initial server-side render of the GSC status table is unaffected by whether
that connection succeeds (it always renders the freshest data the page
handler already fetched); the SSE connection only keeps it current
afterward.

**Markup shape** (`pages.DeploymentsLiveRegion`/`pages.DeploymentRow` in
`pages/sessions.templ`): the table is wrapped in one ancestor `<div
hx-ext="sse" sse-connect="/api/live/deployments">` -- never itself a swap
target, per `libs/go/htmxsse/README.md`'s reconnect-baseline note -- and
each `<tr id="deployment-row-{sgcID}">` carries `sse-swap="deployment.
{sgcID}"` (`events.TopicForDeployment`), matching the routing key
`handleDeploymentsLiveSSE` publishes on. A row rendered via the #1628
self-terminating poll (`GET /api/deployments/{sgcID}/row`) or any of the
Start/Stop/Restart action endpoints re-renders through the same
`pages.DeploymentRow`, so it always carries `sse-swap` too and never drops
out of the live stream.

**Live / Not Live indicator**: a badge and hidden "Reload" link (both
outside the swapped rows, inside the same `hx-ext="sse"` container) flip to
"Not Live" after one heartbeat interval (`MANMANV2_SSE_HEARTBEAT_INTERVAL`,
passed to the page as `data-heartbeat-ms`) with no `htmx:sseOpen`/row
update -- covering both an explicit `htmx:sseError`/`htmx:sseClose` and a
silently stalled connection (no event within `2 * heartbeatMs`). The
debounce avoids flapping the indicator on a single dropped beat. Reconnect
itself is the browser's native `EventSource` retry, driven by the interval
`htmxsse` advertises -- there is no hand-rolled reconnect loop; the Reload
link is the documented manual fallback while not-live.

**When live updates are unavailable** (`app.sseHub == nil` -- no
`RABBITMQ_URL`, or the broker was unreachable at startup, see `ENV.md`):
`SessionsPageData.LiveUpdatesEnabled` is `false` and the page omits the
`hx-ext="sse"`/`sse-connect`/indicator markup entirely rather than pointing
it at a route that would only 503. The #1628 per-row poll remains the
update path in that case, unchanged.

## Workshop Top-Level Page (M6, #2362)

`GET /workshop` (`handleWorkshopPage`, `handlers_workshop_page.go`) is the
redesigned Workshop top-level page: the one place a Server Manager manages
Workshop content fleet-wide (US8, FR6/FR7). It is additive to the
pre-existing sub-routes below -- registering `/workshop` does not remove or
redirect `/workshop/library` or any other `/workshop/*` route; the nav
entry swap and `/workshop/library` redirect land in a dependent
navigation/disposition task.

`pages.WorkshopPage` (`pages/workshop.templ`) renders inside the shared M5
nav shell (`components.Layout`) and integrates the following fleet-scale
actions, each targeting its pre-existing handler unchanged rather than
duplicating any logic:

| Route | Handler | Purpose | On this page |
|-------|---------|---------|---------------|
| `/workshop/create-library` | `handleCreateLibrary` | Create a library | Toggle form in the page header |
| `/workshop/update-library`, `/workshop/delete-library` | `handlers_workshop.go` | Library update/delete | Per-library Manage Blade |
| `/workshop/add-addon-to-library`, `/workshop/remove-addon-from-library`, `/workshop/add-library-reference`, `/workshop/remove-library-reference` | `handlers_workshop.go` | Addon/reference management | Per-library Manage Blade |
| `/workshop/bulk-add-collection` | `handleBulkAddCollection` | FR7 collection-add (C33) | Page-level "+ Add Collection" Blade, zero-request open |
| `/workshop/batch-create-addons` | `handleBatchCreateAddons` | FR7 batch-addon-create (C34) | Page-level "+ Batch Create" Blade, zero-request open |
| `/workshop/batch-status` | `handleWorkshopBatchStatus` | Shared progress surface for both batch flows above | Linked from "Recent Batch Jobs" and from a successful collection-add/batch-create submit |
| `/workshop/cache-blade` | `handleWorkshopCacheBlade` | FR7 cache-backed install (C35) | Cache Blade, opened via hx-get for a chosen addon |
| `/workshop/cache/verify`, `/workshop/cache/evict` | `handlers_workshop.go` | Verify/evict a cache entry | Cache Blade's hx-post forms swap the blade body in place (no navigation) -- the pre-existing plain-POST full-page behavior on `/workshop/cache` is unchanged (NFR6), gated on the `HX-Request` header |
| `/workshop/search`, `/workshop/fetch-metadata`, `/workshop/create-addon` | `handlers_workshop.go` | Addon search/create | Search links out; fetch-metadata is an inline form in the Cache section |

Each library's Manage Blade (`workshopLibraryPanel`) and the two
collection-add/batch-create Blades are pre-rendered into `<template>`
elements at the bottom of the page and opened via the same
`data-open-blade-template` click delegate `games.templ`'s per-deployment
Customize blade uses (see `workshopBladeOpenerScript`'s doc comment) --
opening them costs no request. The Cache Blade is the one exception: its
data (cache entries for a chosen addon) isn't known until an addon is
picked, so it opens via a real `hx-get` to `/workshop/cache-blade`,
matching the Config Editor blade's `hx-get`/`hx-swap="beforeend"` shape.

GC-level library attachment (FR10) is deliberately not part of this page --
it lands on the Games page panel in its own task.

## Documentation

- **[ARCHITECTURE.md](ARCHITECTURE.md)** - System architecture and patterns
- **[COMPONENTS.md](COMPONENTS.md)** - Component usage guide
- **[TEMPL_MIGRATION.md](TEMPL_MIGRATION.md)** - Migration progress

## Features

- Type-safe templates with compile-time checks
- Component reusability with Props pattern
- HTMX-first architecture for dynamic interactions
- Dark mode support (light/night/oled themes)
- Tailwind CSS with tailwind-merge-go
- Alpine.js for client-side state

## Critical Gotchas

### JavaScript and Template Expressions

**Problem**: Templ expressions `{ }` inside `<script>` tags are treated as **literal text**, not evaluated.

**Wrong**:
```templ
<script>
  const sessionId = { fmt.Sprintf("%d", data.Session.SessionId) };  // Outputs literal string!
</script>
```

**Correct**: Use HTML data attributes (which ARE evaluated), then read in JavaScript:
```templ
<div id="my-script" data-session-id={ fmt.Sprintf("%d", data.Session.SessionId) }></div>
<script>
  const sessionId = parseInt(document.getElementById('my-script').dataset.sessionId);
</script>
```

**Why**: Templ treats script content as raw strings to avoid breaking JavaScript syntax. Dynamic values must be injected via HTML attributes.

## Build System

Uses custom `templ_library` Bazel macro:
- Accepts `.templ` files and optional `.go` files
- Automatically includes templ dependencies
- Generates `_templ.go` files via `templ generate`
