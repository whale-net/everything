# Current state

Part of the [LeafLab product brief](../PRODUCT.md). Written by architect against the tree as of 2026-08-29; where it disagrees with `README.md`, `ARCHITECTURE.md`, or `DATA.md`, this section is the one that was verified.

## The thing nobody mentioned: plan #1166 was built and reverted three commits ago

`71d828b0 Revert plan #1166 v2 merged scaffolding (9 PRs) (#1481)` — merged 2026-08-29, immediately before the project-manager product loop landed. It removed ~9,900 lines: `leaflab/ui/` (a real HTMX BFF with OIDC login, DB-backed sessions, a boards screen, templ components, conformance tests), `leaflab/api/contract/`, `leaflab/api/ratelimit/`, `leaflab/api/auth.go`, `leaflab/hwkey/`, `leaflab/conformance/`, migrations `013_sensor_hw_history_i2c_address` and `014_htmxauth_sessions`, and `libs/go/grpcauth/device_flow.go`. Issue #1166 ("LeafLab V1 API", 61 FRs + 17 NFRs, `plan:approved`) is closed; the revert PR says it "was abandoned at the requester's direction," with 25 further open PRs closed unmerged. Abandoned branch work still sits in `.pm-worktrees/1472/` (untracked, not on `main`).

This is directly relevant to the brief for three reasons:

1. **This product brief exists because that plan was product-sized and got specced as one plan.** That is the failure mode this whole loop exists to prevent. The abandoned plan's own summary is worth reading as a scope warning, not as a spec.
2. **Its three verified defects are still in `main`** — the revert removed the fixes, not the bugs. I re-verified all three against the current tree; they are in the "What is broken" section below.
3. **Its persona set is finer-grained than this draft's** (Super Admin / Tinkerer / Household Owner / Gawker / Grower, ~110 user stories on branch `worktree-leaflab-api-v1-plan`). Not saying adopt it; saying the draft's three personas should be a deliberate simplification rather than an accident.

## What is real and working

| Area | State |
|---|---|
| Firmware ingest path | Real. `leaflab/sensorboard/` + `//firmware` libs, MQTT/TLS, dynamic sensor instantiation from a pushed `DeviceConfig` persisted to NVS. Host-testable with fakes. |
| Processor | Real. `leaflab/processor/` — AMQP consumer, upserts board/sensor/sensor_type, writes `sensor_reading`, records `device_config`, applies region assignments on ack. Unit-tested (`handler_test.go`, 402 lines). |
| Schema | Real, 12 migrations, TimescaleDB hypertable on `sensor_reading`. SCD2 tables `sensor_name_history`, `sensor_region_history`, `sensor_hw_history` all use canonical `valid_from`/`valid_to` with open-interval partial indexes (migration 011 did that cleanup deliberately). |
| Analytical views | Real DDL, 7 views in `012_views.up.sql`. See caveat below. |
| gRPC API | Small and real: `PushDeviceConfig`, `GetDeviceConfig`, `ListBoards` (`leaflab/api/proto/api.proto`, 54 lines). **No authentication, no authorization, no interceptors** — `main.go` registers the service and gRPC reflection with nothing in front of it. |
| Release wiring | Real. `leaflab/BUILD.bazel` ships a `release_helm_chart` with three apps (`leaflab-api`, `migrate`, `processor`). A UI would be a fourth `release_app` + a chart entry. |
| Shared auth library | Real and reusable — `libs/go/htmxauth` (OIDC + no-auth modes, cookie or DB-backed sessions, `RequireAuth`, `WithAccessToken`), `libs/go/grpcauth` (JWT validation, `realm_access.roles` → `Claims.Roles`), `libs/go/htmxui` (Shell/ThemeSwitcher/UserMenu primitives), `libs/go/htmxbase`. Two production consumers: `manmanv2/ui`, `tools/app_registry/ui`. |

## What is scaffolded, not built

- **`leaflab/ui/` is wireframes only.** 19 static HTML screens under `ui/design/wireframes/screens/` plus `_shell.html` and a stale `preview.html`. No `BUILD.bazel`, no Go, no templ, no deployable. Screens 02/14 and 03/10 are duplicate-and-conflicting takes on the same views, and 19 (alerts) is not in the shell nav.
- **`plant` / `plant_type` are DDL with no writer.** They exist in `001_initial_schema.up.sql`, are joined by `v_sensor_reading_with_plant`, and are **referenced by exactly nothing else in the repo** — no Go code inserts, updates or reads them; there is no seed data; and `DATA.md`'s ER diagram omits both tables entirely. `ARCHITECTURE.md:144` lists them as if they were part of the working model. **C12/C13 are greenfield, not "extend the existing plant model."**
- **`region` has no writer either.** Nothing in `leaflab/api` or `leaflab/processor` inserts a `region` row. Regions exist today only if someone wrote SQL by hand. `sensor.region_id` gets *assigned* by the processor but regions themselves must be created out of band.
- **The `v_` views have no consumer on `main`.** No Grafana provisioning is committed, and no Go code selects from them. Intake's framing ("the views become the UI's data interface") is a forward-looking statement, not a reuse of something load-bearing today. That's fine — but it means the views' shape is still cheap to change, which is worth knowing while it's true.
- **No `ui_sessions` table.** `htmxauth`'s DB session manager probes for it at boot and hard-fails if absent; `manmanv2` and `app_registry` each own a migration for it. leaflab would need one (migration `014_htmxauth_sessions` did exactly this and was reverted).

## What does not exist at all

- **Any notion of a user, an owner, or an account.** No `user` table, no `owner_id` on `board`, `region`, `sensor`, or `plant`. `board` has only `device_id` (eFuse MAC) — **there is no board name column**, so C9's "rename a board" has nothing to rename.
- **Any authorization.** Nothing checks who is calling anything, anywhere in the domain.
- **Board-level placement.** Placement is per-*sensor* (`sensor.region_id` + `sensor_region_history`). There is no `board.region_id` and no board placement history. C11's "move a board to a different location" has no schema behind it.
- **Any OTA path for firmware.** Flashing is USB (`bazel run //leaflab/sensorboard:flash -- /dev/ttyUSB0`). Every wire-contract change is a physical-access change to the whole fleet.
- **A leaflab realm role.** See below.

## What is broken (verified in the current tree, not inferred)

1. **A plant move retroactively rewrites every reading it ever produced.** `v_sensor_reading_with_plant` joins `p.region_id = e.region_id` on the plant's *current* placement, bounded by `created_at`/`removed_at`. `plant.region_id` is a plain mutable column with no history table. The first `UPDATE plant SET region_id = ...` re-attributes the plant's entire history, and the pre-move truth is recorded nowhere, so no later migration can recover it. Directly gates C13.
2. **The processor's `SensorCache` serves a stale `region_id` after every placement change.** `handleConfigAck` (`processor/handler.go:255`) calls `ApplyConfigRegions`, which writes `sensor.region_id` in Postgres — and then only calls `cache.SetConfigVersion`. Nothing invalidates the cached `SensorInfo.RegionID`, and the device's post-apply manifest arrives *before* the ack, so the cache reloads the pre-apply value. Every subsequent `sensor_reading.region_id` is stamped with the old region until the board reboots. That column is the sole input to region attribution in every view. Directly gates C11.
3. **A config push can write any region onto any sensor with no check.** `PushDeviceConfig` (`api/server.go:49`) does no authentication and no validation of `SensorConfig.region_id`; `ApplyConfigRegions` (`processor/repository.go:296`) writes whatever arrives. Once more than one person has boards, this is the isolation hole under C15.
4. **`sensor_hw_history` cannot record an I2C address change.** Migration 009 dropped `mux_address`/`mux_channel` and added `mux_path`, but never added `i2c_address` to the history table — the migration that added it (`013`) was reverted. A rewire that changes the chip's address is unrecordable.
5. **A simultaneous rename + rewire mints a second `sensor_id`.** `UpsertSensor` looks up by hardware address first, falls back to `UNIQUE(board_id, name)`; when both change in one manifest, neither key matches and a new sensor row (and thus a broken reading history) is created.

Items 4 and 5 mean **C4 as written ("rename or rewire a sensor without breaking the continuity of that sensor's reading history") is overclaimed in the `Now` bucket.** Rename alone is genuinely solid. Rewire is partially recorded at best; rename+rewire together breaks continuity outright.

## Two smaller factual corrections that affect the draft

- **`board.last_seen_at` is not a liveness signal.** `UpsertBoard` bumps it only on a manifest or a config ack — never on a reading (`handleSensorReading` goes cache → insert and never touches `board`). C6's "whether each one is currently reporting" has to come from `MAX(sensor_reading.recorded_at)`. `DATA.md`'s last example query uses `b.last_seen_at` in a way that implies otherwise.
- **`sensor_reading.recorded_at` is `time.Now()` in the processor**, not a device timestamp. `firmware.SensorReading` carries only `value` and `uptime_ms` (`firmware/proto/firmware.proto`). There is no device-side buffering, so a broker or network outage silently loses that window rather than backfilling it. C1's "unattended for months" is true of the board; it is not a durability claim about the data.
- **The manifest can only express single-hop mux.** `SensorDescriptor` has scalar `mux_address`/`mux_channel` while `sensor.mux_path` and `SensorConfig.mux_path` are multi-hop. `processor/handler.go` has the TODO inline. A sensor behind cascaded muxes cannot be identified from a manifest, which partially forecloses C14 today.
