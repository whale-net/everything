# Live-Page Polling Audit (2026-10)

An audit of every htmx "live" page and SSE stream that sends repeated requests
to the server, ranked by how much DB load each can cause. A Tempo trace showed
a live page polling hard enough to load the database, the second incident of
this kind. The policy this audit enforces is in
`.claude/skills/design-htmx-ui/SKILL.md` § Interaction rules → "Live data and
polling policy".

## Findings

Severity combines per-tick DB cost, interval, and how many times the poll is
repeated (per tab, per row, per connection).

| Sev | Where | Interval | DB work per tick | Stops? | Multiplicity |
|---|---|---|---|---|---|
| **High** | manmanv2 `/` dashboard: `pages/home.templ:40` → `handleDashboardSessions` (`handlers_home.go:90`) | `load, every 10s` | N+1: `ListSessions(live)` + `ListServers` + one `ListServerGameConfigs` per server + one `GetGameConfig` per SGC + one `GetGame` per game, i.e. `2+S+C+G` queries. Grows with fleet size. | Never. No visibility gate, so background tabs keep polling. | Per open tab |
| **High** | manmanv2 game overview: `overviewPollAttrs` (`pages/game_detail.templ:89`) → `buildGameOverviewData` (`handlers_games.go:1436`) | `every 3s` | 5 RPCs, including fleet-wide `ListServerGameConfigs(0)` and `ListSessions` (page 100) | Only while a deployment is transient (pending restart, `starting`, `stopping`). A stuck state polls forever. | **One per game row on `/games`.** Collapsed rows are only `x-show`-hidden, so they keep polling. |
| **Med** | `libs/go/htmxsse` heartbeat (`handler.go:226`) | `SSE_HEARTBEAT_INTERVAL` (default 30s) | Calls `fragment(r, topic)` for **every topic on every connection**, then compares hashes. A full DB re-render just to decide on a keepalive. | Never. Runs until `MaxStreamLifetime` (1h). | Per connection × per topic. Affects manmanv2 activity/deployments, app_registry, and whagent_net session UI. |
| **Med** | whagent_net `StreamEvents` (`api/handlers/stream.go:32,165`) | **250ms** | `transcript.Read` + `sessions.GetByID`, about 8 queries/s per stream | On terminal session state | Per open stream |
| **Low-Med** | krill `/ops/claimed`: `claimedPollAttrs` (`krill/ui/pages/ops.templ:405`) → `krill/ui/ops.go:340` | `every 3s` | `soleScopeID` + `ListClaimedTasks` | While a lease is within 5 min of expiry. **Already-expired leases also match** (`ops.go:310`), so a claim that is never reaped polls forever. | Per tab |
| **Low** | manmanv2 `/api/dashboard-summary` (`pages/home.templ:10`) | `load, every 30s` | 3 RPCs | Never | Per tab. Within the cheap-summary rule apart from the missing visibility gate. |
| **Low** | manman (legacy) `templates/home.html:278,292` | `every 10s` / `every 5s` | 1 legacy-API call each | Never | Per tab. Maintenance mode only. |
| **Dormant** | manmanv2 `DeploymentRow` (`pages/deployment_row.templ:66`) | `every 3s` | 6 RPCs | When settled | Would be one per row. No page renders it today, but the design skill names it as the "mutate in place" reference. |
| Info | `libs/go/htmxbase/example_integration.go:88` | `every 5s` | — | — | Example code that teaches unconditional polling |

The two manmanv2 High findings are the likely source of the traced incident.
Both run on pages operators leave open.

**Clean:** leaflab/ui ("NFR1: no hx-trigger polling"), tools/app_registry/ui
(SSE only), whagent_net/ui (htmxsse Hub), audience_score_system/web,
friendly_computing_machine, libs/go/htmxui. The manmanv2 log consoles use
`EventSource` against a shared push consumer and do no DB polling. The
`setInterval` in `liveindicator` only checks staleness on the client.

## Fix plan

Each step is its own PR, in order. Every step must keep or add a test that
pins the interval/condition, as `handlers_deployment_row_refresh_test.go`
already does for the row poll.

1. **manmanv2 dashboard sessions → SSE.** Wire `#active-sessions` to the
   existing `/api/live/activity` stream (session lifecycle events from
   RabbitMQ) with a `liveindicator`. Keep a fallback poll at `every 60s`,
   gated on page visibility. Remove the N+1 in `handleDashboardSessions`
   with a single `ListServerGameConfigs(0)` and one config/game lookup per
   distinct ID, or a batched RPC. Also gate `/api/dashboard-summary` on
   visibility.
2. **manmanv2 game overview.** Render the poll attributes only on the
   expanded `/games` row (lazy-load the overview on expand), or drive the
   transient state from `/api/live/deployments` topics. Lengthen the fallback
   to ≥10s and cap it (e.g. stop after 5 min and show a "Refresh" button) so
   a stuck `stopping` session does not poll forever. Scope
   `buildGameOverviewData` to the game's SGCs instead of the whole fleet.
3. **htmxsse heartbeat.** Add a keepalive-only mode (`WithHeartbeatRender(false)`
   or a config flag) that sends a comment/keepalive without calling
   `fragment`, and make it the default once callers are checked. Callers that
   rely on heartbeat re-render for correctness (no event source for some
   state) opt in explicitly. Cache renders per topic across connections so N
   viewers cost one render, not N.
4. **whagent_net `StreamEvents`.** Raise the status poll to ≥2s, or publish
   session status changes on the existing broadcaster so the ticker becomes
   a slow safety net only.
5. **krill claimed tab.** Exclude already-expired leases from
   `claimedPollingDue`, or cap polling after expiry, so a lease that is never
   reaped does not poll forever.
6. **Examples and dormant code.** Annotate or replace the `every 5s` example
   in `libs/go/htmxbase/example_integration.go`. Bring `DeploymentRow`'s 3s
   fallback in line with the policy before it is reused.

Legacy `manman/` is left alone (maintenance mode).

## Verifying the fixes

- Open manmanv2 `/` with devtools → Network. Before the fix,
  `/api/dashboard-sessions` fires every 10s, including in a background tab.
  After it, only SSE traffic plus at most one request per 60s while the tab is
  visible, and none while hidden.
- Start a deployment, then open `/games` with the row collapsed. Before the
  fix, `/games/{id}/overview` fires every 3s. After it, nothing fires until
  the row is expanded.
- In Tempo, group root spans by `resource.service.name` over a 10-minute
  window with one dashboard tab open. The manmanv2 control-api span rate
  should fall by roughly an order of magnitude.
