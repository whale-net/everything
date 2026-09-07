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
- `POST /sessions/deployments/{sgcID}/restart` -- **literally stop-then-start
  over the same helpers** the plain Stop/Start endpoints use: no distinct
  RPC, no distinct config resolution. It resolves the live session and
  dispatches the same bounded-timeout `StopSession` call as the plain Stop
  endpoint above. If that dispatch fails or times out, it returns the same
  inline error and never attempts to start (#1664). Otherwise -- since
  `StopSession` is a fast, ack-only dispatch (#1663) -- the request returns
  immediately, rendering the transitional "stopping" row; the actual wait for
  the live session to disappear (polling `app.deploymentStopPollInterval`/
  `deploymentStopTimeout`, overridable for tests) and the subsequent
  `StartSession` call (the same helper the plain Start path uses,
  `force=false`) finish in a background goroutine using `context.Background()`
  rather than the request's own (about-to-be-cancelled) context, mirroring
  the host manager's async-dispatch pattern from #1663. The row's own
  self-terminating poll (see below) picks up convergence from "stopping"
  through to stopped/crashed or starting/running with no additional client
  logic. A deployment with no live session (crashed/lost) degenerates to the
  start step alone, synchronously, exactly as before -- that path was
  already fast and isn't implicated in #1662/#1664.
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
