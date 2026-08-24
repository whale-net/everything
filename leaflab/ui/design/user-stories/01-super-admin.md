# LeafLab — Super Admin User Stories

Source material for `leaflab/ui/design/wireframes/` — screen 08 is this
persona's home; 03, 09, 14, 18 and 19 are their working screens.

## Persona

**Super Admin** — the operator who can log in and repair *someone else's*
setup. On a calm day the job is small and read-only: glance at
`08-admin-dashboard`, confirm every `board` has been seen recently, notice one
device still running a config two versions behind, push it, done. The questions
are fleet-shaped ("is anything offline?"), not account-shaped.

On a bad day the job is forensic and cross-account. An owner reports "my sensors
stopped working" and the admin needs that device's *real* state — the
`device_config` rows pushed and never `acked_at`, the `sensor_reading` rows
still arriving stamped with a stale `config_version`, the `sensor_hw_history`
row showing an I2C address that changed under the owner's feet — none of which
the owner's trimmed view shows. Or worse: a fleet push an hour ago and 40 boards
have not ACKed. This is the only persona that is cross-tenant by definition,
which means every read and write lands in another user's data and must be
attributable afterwards. LeafLab has no ownership model, no audit trail, and no
authentication at all today, so this persona needs the *concepts* built before
the RPCs.

## Epic A — Fleet at a glance, across every owner

1. As the super admin, I want one list of every `board` regardless of owner —
   device_id, `last_seen_at`, active config version, sensor count — so I can
   answer "is anything broken?" without querying per owner.
   - Acceptance: one call joins `board` to `v_board_state_current` (latest
     accepted `device_config`) plus a `sensor` count. `ListBoards` returns only
     `{device_id, board_id}` today — no `last_seen_at`, no version.

2. As the super admin, I want boards that stopped reporting to be loud on that
   list, not something I sort for.
   - Acceptance: a board whose `board.last_seen_at` exceeds a stated threshold
     is flagged inline with the actual age. Screen 08's "2 online · 1 offline"
     stat and screen 14's status column both need this field returned.

3. As the super admin, I want to filter the fleet by device_id prefix, owner,
   region, and online state, so hundreds of boards stay navigable when a user
   reads me half a MAC over chat.
   - Acceptance: the board-list call takes filters and returns a page token.
     `ListBoards` is unfiltered, global, and unpaginated today.

4. As the super admin, I want to look up everything belonging to one *person*,
   so support starts from an owner rather than a device_id.
   - Acceptance: one search returns every `board`, `sensor`, `region`, and
     `plant` attributable to a principal. No table has an owner column
     (blocker 2).

## Epic B — Seeing a device's real state

5. As the super admin, I want the whole push log for a board — every
   `device_config` row with `version`, `pushed_at`, `accepted`, `acked_at`,
   `rejection_reason` — so I can tell a rejected push from an unanswered one.
   - Acceptance: config history returns all rows descending by `version`.
     `GetDeviceConfig` returns only the current config and `found`;
     `rejection_reason` and `acked_at` are invisible to every caller today.

6. As the super admin, I want *pending* and *rejected* rendered as different
   things, so "the device never answered" is never read as "the device said no."
   - Acceptance: `acked_at IS NULL AND accepted = FALSE` renders as pending with
     elapsed time since `pushed_at`; a non-null `rejection_reason` renders as
     rejected, verbatim.

7. As the super admin, I want to diff any two `device_config` versions, so when
   an owner says "it worked yesterday" I can see what changed.
   - Acceptance: per-sensor added/removed/changed diff of two
     `device_config.config_json` payloads as in `18-config-diff`, reachable from
     any two rows of the history.

8. As the super admin, I want readings arriving with a `config_version` older
   than the board's active version called out, so I can prove a board is running
   stale config while it looks healthy.
   - Acceptance: compares recent `sensor_reading.config_version` against the
     active accepted `device_config.version`. `v_sensor_reading_with_config_debug`
     already computes this join and no RPC reads it.

9. As the super admin, I want `sensor_name_history`, `sensor_region_history` and
   `sensor_hw_history` on one timeline, so "when did this sensor's I2C address
   change?" is one lookup, not three SQL queries.
   - Acceptance: per-sensor history returns all three tables' rows with
     `valid_from` / `valid_to`, aligned, as in `09-sensor-detail`.

10. As the super admin, I want raw `sensor_reading` rows including
    `valid = FALSE`, so an out-of-range sensor stays visible to me even where a
    trimmed owner view would smooth it away.
    - Acceptance: a readings read-path over `v_sensor_reading_enriched` exposes
      the `valid` flag and filters to invalid-only. No readings read-path exists.

## Epic C — Fleet push, and the bad day it causes

11. As the super admin, I want to push config to many boards in one action
    (screen 08's "Push Config to All"), so a fleet change isn't one
    `PushDeviceConfig` call per device.
    - Acceptance: a batch push takes a set of `board_id`s and returns a
      *per-board* result, never one aggregate success.

12. As the super admin, I want the blast radius previewed before anything is
    written — how many boards, which, and the per-board diff — so I learn I'm
    about to break 40 boards before I break them.
    - Acceptance: dry-run returns the same diffs the real push would and writes
      no `device_config` row; its result never looks like a completed push.

13. As the super admin, I want a live ack tracker after a fleet push — acked /
    rejected / silent per board — instead of polling `GetDeviceConfig` per
    device and comparing version numbers by eye.
    - Acceptance: the batch view groups boards by `acked_at` and `accepted`,
      updating as MQTT acks land. The API is never notified of an ack today
      (blocker 4).

14. As the super admin, I want to roll affected boards back to their previous
    accepted config in one action, without looking up each prior version.
    - Acceptance: rollback targets, per board, the highest `device_config.version`
      with `accepted = TRUE` below the current one, shows that exact target
      before confirming, and writes it as a **new** version — the log stays
      append-only.

15. As the super admin, I want a fleet push to require a reason and record who
    issued it, so the config log explains itself next week.
    - Acceptance: actor and reason are stored and returned alongside `pushed_at`.
      `device_config` has neither column today.

## Epic D — Alerts and triage

16. As the super admin, I want a cross-fleet alert list with unacked /
    acknowledged / resolved states and a device filter, so triage starts from
    severity, not from whichever device I happened to open.
    - Acceptance: screen 19's counts and filters are backed by real rows
      carrying device, severity, message, and start time.

17. As the super admin, I want ack / dismiss / resolve to record *me*, so the
    acknowledged-by column reads `admin` because an admin acted — not by
    default.
    - Acceptance: each action stores the authenticated subject and a timestamp,
      distinguishable from the `auto` actor screen 19 also shows.

18. As the super admin, I want alerts for the two failure modes I actually
    chase — a board past its heartbeat threshold, and a config pushed but
    unacked too long — so the dashboard tells me before an owner does.
    - Acceptance: both derive from `board.last_seen_at` and
      `device_config.acked_at`. No alert table, RPC, or evaluator exists
      (blocker 5).

## Epic E — Acting inside someone else's account

19. As the super admin, I want to act on another user's board explicitly as
    myself-acting-for-them, rather than by silently holding global write access,
    so a cross-account write is never indistinguishable from the owner's own.
    - Acceptance: act-as is entered deliberately, visible in the UI chrome for
      its whole duration, and stamps both admin subject and target owner on
      every resulting write.

20. As the super admin, I want an audit log of everything I did in someone
    else's account — reads included during break-glass — so the owner can be
    told exactly what was touched.
    - Acceptance: an append-only audit table records actor, target owner,
      action, entity, timestamp, and reason; queryable per admin and per owner.

21. As the super admin, I want break-glass to require a stated reason and expire
    on its own, so standing cross-tenant power isn't the resting state of my
    login.
    - Acceptance: elevation is time-boxed, its reason lands in the audit log,
      and scope drops back automatically.

22. As the super admin, I want my role to come from the identity provider, so
    admin access is granted and revoked in one place.
    - Acceptance: the server enforces a realm role via `libs/go/grpcauth`;
      `AuthModeNone` is reachable only in local dev. The server runs fully open
      with reflection on today (blocker 1).

## Epic F — Destructive operations, lifecycle, and export

23. As the super admin, I want to decommission a device (screen 03's "Delete
    Device") with its readings retained, so an unplugged board stops polluting
    the fleet view without destroying history.
    - Acceptance: decommission is a soft state on `board`, never a `DELETE` —
      every FK is `ON DELETE RESTRICT`, so a hard delete is impossible anyway.
      Requires typed confirmation of the device_id plus a reason.

24. As the super admin, I want "Reset Config" to be an ordinary append to
    `device_config` with a factory-default payload, so a reset is diffable and
    reversible like any other version.
    - Acceptance: reset writes a new version and shows up in history and diff
      like any push.

25. As the super admin, I want reboot disabled with a stated reason when a board
    is offline, so I don't queue an action that can never be delivered.
    - Acceptance: reboot reports a delivery outcome; screen 08's "Offline —
      controls disabled" names `board.last_seen_at` age as the reason.

26. As the super admin, I want to repair another owner's regions and plants —
    including moving a sensor's `region_id` — without opening psql.
    - Acceptance: region create/move/rename and plant assignment go through the
      API, enforce screen 05's rules (max 12 children per parent, min depth
      Room → Shelf → Pot), and close/open `sensor_region_history` rows per the
      SCD2 write path. `region` and `plant` have no code path at all today.

27. As the super admin, I want to export readings for a device, sensor, or
    region over a time range as CSV (screen 08), so I can hand an owner the raw
    data behind an incident.
    - Acceptance: streams from `v_sensor_reading_enriched` with region path and
      `config_version` included, bounded by an explicit range, and writes an
      audit row because it removes another user's data from the system.

## What this persona must NOT be able to do

- **Act invisibly.** No cross-account write without an audit row naming admin
  subject, target owner, and reason. Untraceable global power is the failure
  mode this persona creates, not a convenience.
- **Hold standing cross-tenant access.** Cross-account reach is entered
  deliberately (act-as / break-glass) and expires; it is not the resting state.
- **Read device secrets.** Wi-Fi and MQTT credentials are provisioned over USB
  (`//leaflab/sensorboard:provision`) and must never be readable through the
  API in any role.
- **Hard-delete history.** Nothing may remove `sensor_reading`, `device_config`,
  or any `*_history` row. Decommission is a state change; retention is a
  separate non-interactive policy.
- **Edit or delete audit rows.** The audit log is append-only to the admin too.
- **Rewrite the config log.** `device_config` is append-only — rollback and
  reset write a new `version`, never mutate an existing row.
- **Forge an ack.** `accepted`, `acked_at`, and `rejection_reason` are written
  only from the device's MQTT ack. An admin may re-push, never mark accepted on
  a device's behalf.
- **Be the only way things happen.** Any capability the admin needs merely
  because owners can't self-serve is a gap in the owner persona, not an admin
  feature.

## API surface this persona needs

| Endpoint | Purpose | Auth requirement |
|---|---|---|
| `ListBoards` (+ filters, paging, owner, `last_seen_at`, active version) | Epic A fleet view | admin role; cross-tenant |
| `GetBoard` | One board's full state, sensors, manifest | admin, or owner |
| `SearchByOwner` | Cross-account lookup from a person | admin only |
| `ListDeviceConfigs(board_id)` | Full push log incl. `acked_at`, `rejection_reason` | admin, or owner |
| `GetDeviceConfig` *(exists)* | Current config | owner or admin |
| `DiffDeviceConfig(board_id, from, to)` | Screen 18 | admin, or owner |
| `PushDeviceConfig` *(exists)* + actor/reason | Single-board push | owner or admin (act-as) |
| `PushDeviceConfigBatch(board_ids, dry_run, reason)` | Fleet push + blast-radius preview | admin only |
| `GetPushBatchStatus(batch_id)` | Ack tracker: acked / rejected / silent | admin only |
| `RollbackDeviceConfig(board_ids)` | Revert to prior accepted version | admin only |
| `ListSensorHistory(sensor_id)` | Three SCD2 timelines (screen 09) | admin, or owner |
| `ListReadings(filter, range)` | Readings read-path incl. `valid = FALSE` | owner or admin |
| `GetConfigDrift(board_id)` | Readings whose `config_version` lags active | admin |
| `ExportReadingsCSV(filter, range)` | Screen 08 export; audited | admin only |
| `ListAlerts` / `Ack` / `Dismiss` / `Resolve` | Screen 19 | admin (owners see own) |
| `RebootDevice(device_id)` | Screens 08 / 03 control | admin only |
| `DecommissionBoard(device_id, reason)` | Danger zone, soft state | admin only |
| `Region*` / `Plant*` CRUD | Repair another owner's tree | admin (act-as); owner for own |
| `BeginBreakGlass(target_owner, reason)` / `EndBreakGlass` | Time-boxed elevation | admin only |
| `ListAuditEvents(actor?, owner?, range)` | What the admin did | admin only |

## Top blockers

None of the following exists anywhere in the system today.

1. **No authentication or authorization at all** — no interceptor, no user
   table, no roles. There is no way to *be* a super admin. `libs/go/grpcauth` /
   `libs/go/htmxauth` are reusable and `tools/app_registry` is the wiring
   reference, but nothing is wired into LeafLab.
2. **No ownership model** — no owner or tenant column in any of the 12
   migrations. "Cross-tenant" is undefined because there are no tenants, so
   every "repair someone else's setup" story is blocked here first. It is also
   what makes the wide-open server so dangerous: there is no scope to fall back
   to.
3. **No audit trail** — nothing records who did what to whose data. grpcauth
   gives subject and roles but never ownership, so an admin action is
   unattributable by construction.
4. **No ack feedback path** — acks update `accepted` / `acked_at` /
   `rejection_reason` over MQTT, but the API never surfaces them; callers must
   poll `GetDeviceConfig` and compare versions. Epic C's tracker is impossible
   until acks are readable.
5. **No alerts subsystem** — no table, no RPC, no evaluator. Screen 19 and
   screen 08's alerts panel are pure wireframe; the `admin` acknowledged-by
   column has nothing behind it.
6. **No read-path for readings, history, or views** — seven analytical views
   exist (`v_sensor_reading_enriched`, `v_board_state_current`,
   `v_sensor_reading_with_config_debug`, `v_region_path`, …) and the API reads
   none of them. Epics B and F are largely "expose what SQL already computes."
7. **No batch, pagination, or structured error model** — `PushDeviceConfig` is
   single-device and `ListBoards` unpaginated, so a 200-board push has no way to
   report that 40 of them failed.
8. **No browser can call the API** — gRPC on `:50051` only; no HTTP/JSON, no
   grpc-gateway, no grpc-web. Every wireframe screen is unreachable until a
   browser-facing transport (or an htmxauth BFF, per `tools/app_registry`)
   exists.
9. **No device lifecycle commands** — reboot, decommission, OTA/firmware
   version, and reset-to-defaults have no path; screen 03's Danger Zone and
   screen 08's Reboot are decorative.
10. **Regions and plants have no code path** — manual SQL only, which is exactly
    the scenario this persona exists to eliminate.
