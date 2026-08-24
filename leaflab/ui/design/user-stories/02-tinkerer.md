# LeafLab — Tinkerer User Stories

Source material for `leaflab/ui/design/wireframes/` — home screen is
[13-tinkerer-sensor-config](../wireframes/screens/13-tinkerer-sensor-config.html),
with [04](../wireframes/screens/04-device-edit-config.html),
[18](../wireframes/screens/18-config-diff.html),
[09](../wireframes/screens/09-sensor-detail.html) and
[11](../wireframes/screens/11-register-device.html) in the same loop.

## Persona

**The Tinkerer** — owns the boards on their own bench. Solders headers, moves a
BH1750 from the root I2C bus onto channel 3 of a TCA9548A, swaps an SHT3x for a
CCS811, and wants to know within seconds whether the firmware liked it. They
have already read `firmware/proto/config.proto`; they know `SensorConfig` has
`mux_path`, `i2c_address`, `poll_interval_ms`, `sensor_type`, `chip_type`, and
that `region_id` is "server-side only; device ignores this field." They do not
want a wizard — they want the raw knobs, and a text field where a dropdown
would lie to them.

Today their loop is `leaflab/scripts/push-config.sh <device_id> <scenario>` with
grpcurl, plus `mosquitto_sub -t 'leaflab/<device_id>/config/ack'` in a second
terminal because the API will not tell them what the device said. Any UI that
replaces that must be as fast and as honest — so every story below must also be
reachable as an RPC, not only as a click. The distinction they care about most:
*accepted* (the board acked version 48) is not *applied* (rows landing in
`sensor_reading` stamped `config_version = 48` at the new interval). Both must
be visible, and visibly different.

## Epic A — The edit → push → ack → verify loop

1. As the tinkerer, I want the push call to tell me what the device said about
   my config, so I don't have to leave a `mosquitto_sub` running in another
   terminal to find out whether it worked.
   - Acceptance: a `GetConfigStatus(device_id, version)` RPC returns the
     `device_config` row's `accepted`, `pushed_at`, `acked_at` and
     `rejection_reason` for that exact version — the columns the processor
     already writes in `repository.go` on `DeviceConfigAck`. A version that has
     been pushed but not acked is reported as *pending*, distinctly from
     *rejected*.

2. As the tinkerer, I want the verbatim firmware rejection reason, not a
   generic failure, so I can fix the actual mistake instead of bisecting my
   scenario JSON.
   - Acceptance: `DeviceConfigAck.reason` is surfaced verbatim as
     `device_config.rejection_reason`. A rejected push never shows as "pending
     forever" and never silently vanishes.

3. As the tinkerer, I want to watch a push resolve without polling, so the
   edit→ack loop feels like a compile, not a cron job.
   - Acceptance: a server-streaming `WatchConfig(device_id)` (or long-poll
     equivalent) emits an event when `device_config.acked_at` transitions from
     NULL, carrying accepted/rejected and the reason. Polling `GetDeviceConfig`
     and diffing `version` is not an acceptable implementation of this story.

4. As the tinkerer, I want to see the config I just pushed even while it is
   still pending or after it was rejected, so I can compare what I sent against
   what the board is actually running.
   - Acceptance: a read path returns any `device_config` row by
     `(board_id, version)` regardless of `accepted`. Today
     `GetDeviceConfig` calls `GetLatestAcceptedConfig` and returns
     `found = false` for a board whose only push was rejected — that is
     indistinguishable from a board that was never configured.

5. As the tinkerer, I want to confirm the new poll interval is actually in
   effect, not just acknowledged, so "accepted" and "applied" stop being the
   same word.
   - Acceptance: a readings read-path exposes recent `sensor_reading` rows with
     their `config_version` stamp (the column added in migration 009), so I can
     see arrival cadence under version 48 versus 47. "Applied" is defined as
     readings arriving stamped with the new `config_version`.

## Epic B — Validate before you push

6. As the tinkerer, I want to dry-run a config and have the server tell me what
   it would change, so a typo costs me a round trip instead of a rejected board.
   - Acceptance: `PushDeviceConfig` accepts a `dry_run` flag: it validates,
     returns the computed diff against the current accepted config and the
     version it *would* assign, and writes no `device_config` row and publishes
     nothing to the `leaflab.<device_id>.config` routing key.

7. As the tinkerer, I want server-side validation of the things the firmware
   will reject anyway, so I find out before the round trip.
   - Acceptance: validation rejects `i2c_address` outside 0x08–0x77, a
     `chip_type` of `CHIP_TYPE_UNKNOWN` paired with an address that has no
     existing `sensor` row, a `sensor_type` that the chip's catalog entry in
     `firmware/sensor/catalog/chips.yaml` does not produce (e.g.
     `SENSOR_TYPE_HUMIDITY` on `CHIP_TYPE_BH1750`), and two entries colliding
     on the same `(mux_path, i2c_address, sensor_type)` key.

8. As the tinkerer, I want to know whether my push is a merge or a replace
   before I send it, so I stop guessing whether omitting a sensor disables it.
   - Acceptance: the dry run returns the *effective* resulting config, not the
     partial payload. The proto comment says "sensors with no matching entry
     are unchanged," but the row persisted in `device_config.config_json`
     contains only the entries I sent — those two facts must be reconciled and
     stated. A config entry matching no `sensor` row for that `board_id` and no
     address in the board's last `DeviceManifest` returns a non-fatal warning.

## Epic C — Sensor identity, wiring, and history

9. As the tinkerer, when I move a sensor to a different mux channel or change
   its I2C address, I want to say "this is the same sensor, rewired," so its
   readings history and its name follow the hardware.
   - Acceptance: a `RewireSensor(sensor_id, new_i2c_address, new_mux_path)`
     RPC updates the existing `sensor` row in place and closes/opens a
     `sensor_hw_history` pair (`valid_to = NOW()`, new row with the new
     `mux_path` JSONB). It does not create a second `sensor` row.

10. As the tinkerer, I want the system to tell me plainly when a change *does*
    create a new sensor identity, so I am never surprised by a sensor that
    appears to have lost its history.
    - Acceptance: because `idx_sensor_hw_address` is unique on
      `(board_id, i2c_address, sensor_type_id, mux_path::text)`, changing any
      of those three via config push produces a *new* `sensor_id` with a fresh
      `sensor_name_history` and no prior `sensor_reading` rows. A dry run must
      label such an entry "new sensor identity — history will not follow," and
      offer story 9's rewire path as the alternative.

11. As the tinkerer, I want the three SCD2 timelines on the sensor detail
    screen to be independently readable and independently writable, so
    relocating a plant shelf doesn't look like a rewiring event.
    - Acceptance: `sensor_name_history`, `sensor_hw_history` and
      `sensor_region_history` are each exposed as their own ordered
      `valid_from` / `valid_to` list for one `sensor_id`, matching the three
      tables on screen 09. Renaming writes only `sensor_name_history`.

12. As the tinkerer, I want to rename a sensor without pushing a config to the
    board, so a label fix doesn't cost a device round trip and a version bump.
    - Acceptance: a `RenameSensor(sensor_id, name)` RPC closes and opens a
      `sensor_name_history` row and refreshes the `sensor.name` cache without
      inserting a `device_config` row. Because the logical name is also the
      MQTT topic segment, the response states whether a push is still needed.

13. As the tinkerer, I want the `mux_path` format to be exactly one thing
    everywhere, so I stop losing an afternoon to a config that "looks identical"
    but never matches.
    - Acceptance: `mux_path` is documented and validated as the protojson form
      `[{"muxAddress":112,"muxChannel":1}]`, ordered outer→inner, `[]` for the
      root bus — the same encoding stored in `sensor.mux_path` JSONB and
      compared by `mux_path::text` in the unique index. The API normalises key
      order and integer formatting on write, so two semantically equal paths
      cannot cast to different text.

14. As the tinkerer, I want to see what the board reports as physically present
    on its I2C bus, so I know whether the chip I just soldered is even alive.
    - Acceptance: the addresses and chip types from the board's most recent
      retained `DeviceManifest` are readable per device, matching the "Detected
      Sensors (on I2C bus)" table on screen 11, with the timestamp of that
      manifest so I can tell a stale probe from a fresh one.

## Epic D — Versions, diffs, and rollback

15. As the tinkerer, I want the full config history for a board, so I can pick
    the two versions I want to compare instead of only ever seeing "latest."
    - Acceptance: `ListDeviceConfigs(device_id)` returns every `device_config`
      row — `version`, `pushed_at`, `acked_at`, `accepted`,
      `rejection_reason` — newest first, paginated. Rejected and pending
      versions are included and visibly marked.

16. As the tinkerer, I want a server-computed diff between any two config
    versions, so screen 18's "#47 vs #48" is one call and not something the UI
    reinvents.
    - Acceptance: a diff endpoint takes two versions (or one version and an
      unpushed draft) and returns per-sensor `ADDED` / `REMOVED` / `CHANGED` /
      `UNCHANGED` keyed on `(mux_path, i2c_address, sensor_type)`, plus the raw
      `config_json` on both sides for the JSON view.

17. As the tinkerer, I want to roll back to a previous config version in one
    action, so recovering from a bad push doesn't mean hand-editing a scenario
    JSON at 1am.
    - Acceptance: a rollback takes a target `version`, re-pushes that version's
      `config_json` as a *new* higher version (the device rejects
      `version <= current`), and records in the new row that it was derived
      from the target version. The `device_config` log stays append-only.

18. As the tinkerer, I want to edit several sensors and push them as one
    version, so a mux rewire doesn't produce six config versions and six acks.
    - Acceptance: the edit surface accumulates changes across sensors and
      submits a single `PushDeviceConfig` with all `sensors` entries, assigning
      exactly one `device_config.version` — screen 04's "Push Config #48" is
      one write.

## Epic E — Regions, and the API/CLI path

20. As the tinkerer, I want to create and nest regions through the API, so I
    can set `SensorConfig.region_id` without writing SQL against the `region`
    table.
    - Acceptance: create/list/update RPCs over `region` (self-referential
      `parent_region_id`) return the full root→leaf path — the same shape as
      the `v_region_path` view — so the nested picker on screen 13 has a source.

21. As the tinkerer, I want to understand why a region assignment only takes
    effect after the device accepts the config, so a pending ack doesn't look
    like a lost edit.
    - Acceptance: region changes are surfaced as pending until the ack, because
      `ApplyConfigRegions(board_id, version)` runs only on an accepted
      `DeviceConfigAck` and matches sensors by
      `(board_id, i2c_address, mux_path, sensor_type)` before writing
      `sensor.region_id` and `sensor_region_history`. The UI states this rather
      than showing the new region immediately.

22. As the tinkerer, I want every screen action available as a scriptable call
    with the same auth, so I can keep driving the bench from a terminal.
    - Acceptance: no capability is UI-only; the browser transport (JSON/HTTP or
      grpc-web) is an additional door onto the same service, and a bearer token
      from `libs/go/grpcauth` works on both. "Export Config JSON" on screen 13
      yields protojson accepted verbatim as the `sensors` array of a
      `leaflab/scripts/scenarios/*.json` file, and vice versa.

23. As the tinkerer, I want machine-readable errors, so my scripts can branch on
    the failure instead of grepping a string.
    - Acceptance: validation failures return `codes.InvalidArgument` with a
      structured detail naming the offending sensor entry and field
      (`i2c_address`, `mux_path`, `sensor_type`, `poll_interval_ms`), not a
      formatted message. Today every failure in `server.go` is
      `status.Errorf` with an interpolated string.

## What this persona must NOT be able to do

- Push a config to, rewire, rename, or roll back a board they do not own.
  Ownership is per-board; being a tinkerer is not being an admin.
- See or list other people's boards. Today `ListBoards` is global, unfiltered,
  and unpaginated — for this persona it must return only their boards.
- Read `sensor_reading` rows for sensors on boards they do not own.
- Delete a `board`, `sensor`, or `device_config` row. "Remove from Device" on
  screen 13 removes a sensor from the *config* (a new version with that entry
  dropped or `enabled = false`); it must not delete history. `device_config`
  is append-only and `board_id` is `ON DELETE RESTRICT`.
- Rewrite history: `sensor_name_history`, `sensor_hw_history`,
  `sensor_region_history` and `device_config` are closed-and-opened, never
  updated in place or back-dated.
- Create or edit `sensor_type`, `sensor_chip` or `sensor_chip_type` catalog
  rows — seeded from `firmware/sensor/catalog/chips.yaml`, owned by firmware.
- Reassign a board to another owner, or claim a board that already has one.

## API surface this persona needs

| Endpoint | Purpose | Auth requirement |
|---|---|---|
| `ListBoards` (owner-scoped, paginated) | Only my boards | Authenticated; filtered to owner |
| `GetDeviceConfig` | Current accepted config | Authenticated; board owner |
| `GetConfigStatus(device_id, version)` | `accepted` / `acked_at` / `rejection_reason` | Authenticated; board owner |
| `ListDeviceConfigs(device_id)` | Version history incl. rejected + pending | Authenticated; board owner |
| `DiffDeviceConfigs(device_id, from, to)` | Screen 18's per-sensor diff | Authenticated; board owner |
| `PushDeviceConfig(..., dry_run)` | Validate without writing or publishing | Authenticated; board owner |
| `PushDeviceConfig` | The one existing write | Authenticated; board owner |
| `RollbackDeviceConfig(device_id, version)` | Re-push an old `config_json` as a new version | Authenticated; board owner |
| `WatchConfig(device_id)` | Stream ack transitions | Authenticated; board owner |
| `RenameSensor(sensor_id, name)` | Write `sensor_name_history` only | Authenticated; board owner |
| `RewireSensor(sensor_id, addr, mux_path)` | Keep identity across a rewire | Authenticated; board owner |
| `GetSensorHistory(sensor_id)` | Three SCD2 timelines (screen 09) | Authenticated; board owner |
| `GetDeviceManifest(device_id)` | Last I2C probe (screen 11) | Authenticated; board owner |
| `ListReadings(sensor_id, range)` | Verify applied, not just accepted | Authenticated; board owner |
| `Region` create/list/update | Populate `SensorConfig.region_id` | Authenticated; owner-scoped |

## Top blockers

Everything here does not exist today.

1. **The ack never reaches the caller.** The processor writes
   `device_config.accepted`, `acked_at` and `rejection_reason`, but no RPC
   reads them. `GetDeviceConfig` returns only the latest *accepted* config, so
   a rejected push is indistinguishable from no push at all. Every story in
   Epic A is blocked on exposing this row.
2. **No authentication or ownership anywhere.** No interceptor, no user table,
   no owner column in any of the 12 migrations. Every "board owner" cell in the
   table above is aspirational. `libs/go/grpcauth` gives subject and realm
   roles but no ownership model — a board→owner relation has to be designed
   and migrated.
3. **A rewire silently forks sensor identity.** `idx_sensor_hw_address` is
   unique on `(board_id, i2c_address, sensor_type_id, mux_path::text)`, so
   changing an I2C address or mux channel through a config push creates a new
   `sensor_id` with empty history. There is no code path that updates a sensor
   in place, which means `sensor_hw_history` cannot currently record the exact
   event it was built for.
4. **No dry-run and no validation.** `PushDeviceConfig` marshals whatever it is
   given, inserts a row, and publishes. Bad addresses, impossible
   chip/sensor-type pairs and duplicate keys are only caught by the firmware,
   after the round trip.
5. **No config history and no diff.** There is no way to list `device_config`
   rows or fetch version 47 — screen 18 has nothing to diff against, and
   rollback has nothing to roll back to.
6. **No browser transport.** gRPC on :50051 only: no grpc-gateway, no
   grpc-web, no HTTP/JSON. Every wireframe here is unreachable from a browser.
7. **No readings read-path.** `sensor_reading.config_version` is stamped at
   insert but nothing reads it, so "accepted" cannot yet be distinguished from
   "applied."
8. **Regions and manifests have no API.** `region` rows are manual SQL only, so
   `SensorConfig.region_id` is unusable from a UI; the retained `DeviceManifest`
   is consumed by the processor and never re-served, so screen 11's I2C probe
   table has no source. Errors are interpolated `status.Errorf` strings, and
   `ListBoards` is global with no filter and no pagination.
