# LeafLab — Gawker User Stories

Source material for `../wireframes/screens/12-gawker-dashboard.html`. One
persona, one screen, and a hard boundary — everything below is written as much
to define what the gawker *cannot* reach as what they can.

## Persona

**Gawker** — a spectator with no account and, usually, no login at all. A house
guest handed a URL, a friend sent a link in chat, a tablet magneted to the
fridge, a kiosk display in a greenhouse lobby, a Twitch overlay next to a grow
tent. They did not set anything up, they will not fix anything, and they will
never come back to a settings page. They want to look at pretty plant data for
thirty seconds — or, in the kiosk case, forever, at 5-second intervals, from an
unattended browser nobody will ever log into.

That is the whole job, and it is why this persona is the hardest one in the
plan. Every other LeafLab caller is authenticated and owns what it touches;
this one is *anonymous by design* against a database that today has no auth, no
owner column, and one unfiltered global `ListBoards()`. The gawker is therefore
primarily a **security and privacy boundary problem**: the design question is
not "what can we show them" but "what is the smallest, dullest, most
pre-aggregated slice of the system that can be safely handed to an unknown
party over an unauthenticated route, and how do we take it back later." Screen
12 answers the first half — aggregate stats, a current-readings table,
sparklines, and nothing else. No device IDs, no firmware versions, no drill-in
links, no admin surface (screen 08's note: *"Gawkers never see this page at
all."*). These stories answer the second half.

## Epic A — Getting in without an account

1. As a gawker, I want to open a link someone sent me and see plant data
   immediately, with no sign-up, no login, and no cookie banner, so that
   looking at the data costs me nothing.
   - Acceptance: a share URL of the form `/s/<token>` renders the full screen-12
     view on first load with no `Authorization` header, no interactive OIDC
     redirect, and no prior session. `libs/go/grpcauth` is not in this route's
     path; the share token is validated by its own middleware.

2. As a gawker on a kiosk tablet that nobody will ever touch again, I want the
   page to keep working unattended for weeks, so that the lobby screen doesn't
   silently become a login prompt.
   - Acceptance: a share token has an explicit `expires_at`; a token minted for
     kiosk use can be long-lived, and the page renders an unmistakable
     "this link has expired" state (not a stack trace, not a 401 JSON blob, not
     a redirect to Keycloak) once it lapses.

3. As a gawker, I want the page to work in a plain browser with no extension,
   proxy, or CLI, so that "send a link" actually means send a link.
   - Acceptance: the gawker route is HTTP/JSON (or server-rendered HTML), not
     raw gRPC. Today the only surface is `leaflab.api.v1.LeafLabAPI` on :50051
     with no gateway — a browser cannot call it at all (blocker 3).

4. As a gawker following a link that has been revoked, I want to be told
   plainly that the link is no longer shared, so I don't think the greenhouse
   is broken.
   - Acceptance: a revoked or expired token returns a stable, non-enumerable
     "not available" page for every token value — the same response whether the
     token never existed, expired, or was revoked. No distinguishing status
     code, timing, or body between those cases.

## Epic B — What the gawker actually looks at

5. As a gawker, I want a headline row of aggregate stats — active sensor count,
   average illuminance, temperature, and humidity, each with its range — so I
   get the whole picture in one glance without reading a table.
   - Acceptance: the stat row is computed server-side over the share's scope
     only, matching screen 12's four tiles. Averages and ranges are rounded to
     display precision (integer lux, 0.1 °C, 1 % RH) before serialization — the
     API never returns more precision than the screen shows.

6. As a gawker, I want a table of current readings with a human-readable sensor
   label, its type, a coarse location, its value, and roughly how fresh it is,
   so I can see which corner of the room is bright or damp.
   - Acceptance: rows carry a display label (e.g. `BH1750-A1`), sensor type,
     a display location string, value + unit, and a *relative* freshness string
     ("now", "5s ago"). No `device_id`, no `sensor.id`, no absolute timestamp,
     no board or region primary keys (see boundary table).

7. As a gawker, I want sparklines for each metric so I can tell whether things
   are trending up or down without understanding a chart axis.
   - Acceptance: each sparkline is served as a fixed-length bucketed series
     (e.g. 24 points over the share's window) from a pre-aggregated rollup, not
     from a raw `sensor_reading` scan, and carries no per-sensor breakdown.

8. As a gawker, I want the numbers to refresh on their own so the kiosk stays
   live without anyone touching it.
   - Acceptance: the page polls on a server-advertised interval; the response
     carries `Cache-Control: public, max-age=<N>` and an `ETag`, and a poll
     against unchanged data returns `304` without touching the hypertable.

9. As a gawker, I want the page to look intentional when a sensor is missing or
   offline rather than showing a broken tile, so a partly-degraded greenhouse
   still reads as a nice screen.
   - Acceptance: a stale sensor is either omitted or shown with a neutral
     "no recent reading" state; the aggregate tiles suppress themselves entirely
     when the contributing sensor count is below the k-anonymity floor
     (Epic C, story 12), rather than rendering a range over one sensor.

## Epic C — Being shared responsibly (the sharer's side of the gawker)

These stories are written from the gawker's perspective but are *satisfied* by
controls the owner operates. They live here because the gawker is the threat
model.

10. As a gawker, I want the link I was given to show me exactly one greenhouse's
    worth of data and nothing else, so I am never accidentally shown a
    stranger's bedroom sensor.
    - Acceptance: every share token carries an explicit scope — a region
      subtree, a set of boards, or a household — resolved server-side. There is
      no "all data" token. The current unfiltered global `ListBoards()` is never
      reachable from a gawker route (blocker 1).

11. As a gawker, I want the location labels I see to be the ones the owner chose
    to publish, so I'm not shown "Master Bedroom" because that happened to be
    the internal region name.
    - Acceptance: shares carry a per-share display-name override (or a
      publish-safe alias on `region` / `plant`); the raw region name is only
      emitted when the owner has explicitly marked it shareable. Default is
      *not* shareable — an unmarked region renders as a generic ordinal
      ("Room A", "Shelf 1").

12. As a gawker, I want aggregate stats to be genuinely aggregate, so a "range
    23.5–26.1 °C" over two sensors doesn't just hand me two exact readings.
    - Acceptance: min/max and averages are suppressed (tile shows "—") when the
      contributing sensor count is below a configured floor (k, default 3);
      counts are bucketed rather than exact when small.

13. As a gawker, I want the freshness column to be vague, so nobody can infer
    from my screen when a person walked into the room.
    - Acceptance: freshness is rendered as a coarse relative bucket, and the
      underlying value is clamped to a granularity (e.g. 5 s). Exact
      `recorded_at` timestamps are never serialized to a gawker response.

14. As a gawker, I want the link to stop working the moment the owner decides
    it should, so I'm not still watching someone's house a year after the
    dinner party.
    - Acceptance: revocation is per-token and takes effect on the next request
      (cache TTL bounded to the poll interval, not to the token lifetime);
      revoking one share never affects another; tokens can be rotated in place
      so a kiosk can be re-pointed without a human editing a URL.

## Epic D — Being a well-behaved anonymous load

15. As a gawker refreshing enthusiastically, I want the page to stay responsive
    rather than being cut off, so aggressive polling degrades gracefully.
    - Acceptance: the gawker route is rate-limited per token *and* per client
      IP; exceeding the limit serves the last cached payload with a stale marker
      rather than an error, until a hard ceiling is reached.

16. As a gawker, I want the page to load fast on a phone on someone else's
    wifi, so the link is worth opening.
    - Acceptance: a gawker page load performs a bounded, constant number of
      queries against pre-aggregated data — never a per-sensor fan-out and never
      an unbounded time-range scan. The time range is fixed by the share, not
      chosen by the caller (no caller-supplied `start`/`end`/`limit`, which
      would make the endpoint a free query engine over a Timescale hypertable).

## Privacy & exposure boundary

The single most important table in this document. Default for anything not
listed is **no**.

| Field / concept | Exposed to gawker? | Rationale |
|---|---|---|
| `board.device_id` (eFuse MAC) | **No** | A hardware identifier, globally unique and permanent. Leaks a stable device fingerprint, is trivially correlated across any other exposure, and reveals silicon vendor. Never leaves an authenticated route. |
| Board display name / nickname | Only if share-marked | Owner-chosen text; may still contain a person's name or room. Opt-in per share, never by default. |
| Firmware version | **No** | Discloses which CVEs the device is vulnerable to, to an unauthenticated party. Pure attacker value, zero gawker value. Screen 12 shows none. |
| `device_config` version / contents | **No** | Operational config (WiFi SSID, MQTT endpoint, sample intervals). Not a display concern under any reading of the persona. |
| Region names (`region.name`) | Only if share-marked | "Master Bedroom", "Nursery", "Basement" leak home layout and occupancy semantics. Unmarked regions render as generic ordinals. |
| Region tree structure / `v_region_path` depth | Aggregated only | A full path reveals house topology. Show at most the leaf's display label plus one ancestor, as in screen 12's "Room A · Shelf 1". |
| Plant names / `plant_type` | Only if share-marked | Usually benign and often the point of the share; still owner-controlled, since a plant name can be personal and the species itself may be sensitive. |
| Sensor display label (`BH1750-A1`) | Yes | Chip model + ordinal. Reveals hardware model, which is low-value and already implied by the reading types shown. Not a stable cross-share identifier. |
| Sensor / board primary keys | **No** | Enumerable internal identifiers; the gawker has no drill-in path to use them for and they invite ID-guessing against other routes. |
| Sensor type + unit | Yes | Required to render the table at all; carries no identity. |
| Active sensor count | Bucketed | Exact counts over a small fleet distinguish households and reveal build-out. Show a bucketed count; suppress below the k floor. |
| Reading values (current) | Yes, rounded | The entire point of the persona. Rounded to display precision at the server. |
| Min/max aggregates | Yes, above k only | Over small N a "range" is just two raw readings wearing a hat — it de-anonymizes an individual sensor. Suppressed below k = 3 contributors. |
| Averages | Yes, above k only | Same reasoning as min/max. |
| Reading counts / readings-per-minute | **No** | Reading rate is an occupancy and uptime signal (screen 01 shows it; screen 12 deliberately does not). |
| Exact `recorded_at` timestamps | **No** | A timestamped series is a behavioral trace; a lux spike at an exact instant is "someone turned the light on at 23:14". Relative, clamped freshness only. |
| `last_seen` / board online-offline state | **No** | An availability oracle for an unauthenticated party, and a proxy for whether anyone is home. Screen 12 shows sensor freshness, not board liveness. |
| Historical series | Bucketed, fixed window | Sparklines only, from a rollup, over the share's window. No caller-controlled range. |
| Alerts / thresholds | **No** | Not implemented, and would leak that something is wrong to a stranger before the owner sees it. |
| Anything from screens 02–11, 13–19 | **No** | Device management, config, registration, admin. Screen 08's note is explicit: gawkers never see it. |

## What this persona must NOT be able to do

- Authenticate, hold an account, or be assigned a role. A gawker who logs in is
  no longer a gawker.
- Reach any write RPC — `PushDeviceConfig` is not merely unauthorized for this
  persona, it must not be routable from the gawker surface at all.
- Enumerate the fleet. No path from a gawker route to a global board, sensor,
  region, or plant list. `ListBoards()` in its current unfiltered global form is
  disqualifying.
- Supply a query. No caller-chosen time range, limit, offset, sensor id, board
  id, region id, aggregation function, or sort — every parameter is fixed by the
  share token's scope.
- Escalate from one share to another by mutating the URL, guessing a token, or
  swapping an id in a query string. Token space must be large and opaque; ids
  must not appear in gawker responses to be swapped in the first place.
- Read raw `sensor_reading` rows, or any response carrying row-level timestamps.
- Learn anything about the *system*: device count across all households,
  firmware versions, config versions, server version, gRPC reflection, error
  detail, or stack traces. Reflection stays off the public route.
- Persist anything. No comments, no favorites, no writes of any kind, including
  analytics that would tie a viewer to a share beyond aggregate counts.
- See another share's data, or the union of shares, under any circumstance.

## API surface this persona needs

All gawker endpoints are HTTP/JSON on a **separate public BFF route**, not the
gRPC service. None of them accept a caller-supplied filter.

| Endpoint | Purpose | Auth requirement |
|---|---|---|
| `GET /s/{token}` | Render the shared dashboard shell (screen 12) | **Anonymous** — the share token in the path is the only credential |
| `GET /api/public/{token}/summary` | Aggregate tiles: bucketed active-sensor count, avg + range per metric | **Anonymous**, token-scoped, cached + ETagged |
| `GET /api/public/{token}/current` | Current-readings table rows (label, type, display location, value, coarse freshness) | **Anonymous**, token-scoped, cached + ETagged |
| `GET /api/public/{token}/series?metric=` | Fixed-length bucketed sparkline series; `metric` restricted to the share's published metric set | **Anonymous**, token-scoped, served from a continuous aggregate |
| `GET /api/public/{token}/meta` | Share display title, published metric list, poll interval, expiry state | **Anonymous**, token-scoped |
| `POST /v1/shares` | Owner mints a share: scope, window, published metrics, display-name overrides, expiry | **Authenticated** (`grpcauth`, owner) — not a gawker endpoint |
| `GET /v1/shares` | Owner lists live shares with scope, last access, expiry | **Authenticated** (owner) |
| `DELETE /v1/shares/{id}` / `POST /v1/shares/{id}:rotate` | Revoke or rotate a share token | **Authenticated** (owner) |

Everything on the existing `leaflab.api.v1.LeafLabAPI` service —
`PushDeviceConfig`, `GetDeviceConfig`, `ListBoards` — remains
authenticated-only and unreachable from the public route.

## Top blockers

Nothing in this section exists today.

1. **There is no way to be anonymous, and no way to be an owner.** There is no
   auth at all — no interceptor, no user table, no owner or tenant column in any
   of the 12 migrations, reflection on, server fully open. Both halves are
   missing: LeafLab needs authentication for every other persona *and*, on top
   of it, a deliberate anonymous lane. `libs/go/grpcauth` and
   `libs/go/htmxauth` give subject + realm roles only — neither models ownership
   and neither has a concept of anonymous or public access. Until there is an
   ownership column, "this share covers *my* greenhouse" is not expressible, and
   the only scope the API can currently produce is *everything*, globally. That
   is the catastrophic default this persona exists to prevent.
2. **No share-token concept: no minting, scoping, expiry, revocation, or
   rotation.** No table, no middleware, no owner-facing UI. Every Epic C story
   depends on it, and revocation is the one that turns "I sent a link" from
   permanent into reversible.
3. **No browser-callable surface.** gRPC on :50051 with no grpc-gateway, no
   grpc-web, no HTTP/JSON. A kiosk tablet literally cannot call the API. A BFF
   or gateway is a prerequisite for the persona existing at all.
4. **No readings read-path.** The API reads none of the seven analytical views
   (`v_sensor_reading_enriched`, `v_sensor_current`, `v_region_path`, …) and has
   no RPC that returns a reading. Screen 12 is 100 % readings; every tile, row,
   and sparkline on it is unbacked.
5. **No pre-aggregation for the public path.** Sparklines and stat tiles over a
   Timescale hypertable need continuous aggregates or a materialized rollup;
   without them an unauthenticated endpoint is a free query engine over raw
   time-series.
6. **No rate limiting and no caching.** No per-token or per-IP limiter, no
   `Cache-Control` / `ETag` / `304` handling anywhere. A forever-polling kiosk
   plus an open endpoint is an availability problem before it is a privacy one.
7. **No publish-safe naming.** `region` and `plant` have exactly one name each,
   the internal one. There is no shareable flag, no alias column, and no
   defaulting to generic ordinals — so today "publish the region name" and
   "publish the string 'Master Bedroom'" are the same operation.
8. **No display-precision or k-anonymity layer.** Rounding, count bucketing,
   min/max suppression below k, and timestamp clamping are all presentation
   rules that must live server-side; nothing in the current stack owns them.
9. **No structured errors.** Expired, revoked, and never-existed tokens must be
   indistinguishable to the caller; there is no error-shaping layer today to
   guarantee that, and default gRPC/HTTP behavior leaks the difference.
