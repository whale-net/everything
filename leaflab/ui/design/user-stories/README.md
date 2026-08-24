# LeafLab — V1 API User Stories

Source material for the LeafLab V1 API plan. One file per persona, written
against the system as it actually exists (three RPCs, no auth), not as the
wireframes imply it exists.

Companion to [`../wireframes/`](../wireframes/) — the wireframes name the
personas but only in prose notes, and no role or auth model is drawn anywhere.
These docs turn those notes into stories with acceptance criteria.

## Personas

| # | Persona | File | One line |
|---|---------|------|----------|
| 1 | Super Admin | [01-super-admin.md](01-super-admin.md) | Cross-tenant operator who repairs other people's setups |
| 2 | Tinkerer | [02-tinkerer.md](02-tinkerer.md) | Power user who owns their boards and wants the raw knobs |
| 3 | Household Owner | [03-household-owner.md](03-household-owner.md) | Non-technical owner — "is the fern ok, and push the update" |
| 4 | Gawker | [04-gawker.md](04-gawker.md) | Account-less spectator; a privacy boundary, not a feature |
| 5 | Grower | [05-grower.md](05-grower.md) | Cares about plants, not hardware; schema-anticipated, code-absent |

Personas 1, 2 and 4 are named in the wireframes (screens 08, 13, 12). Screen
08's closing note also names a "regular user" tier — that is persona 3. Persona
5 is not in any wireframe; it exists because `plant` / `plant_type` /
`v_sensor_reading_with_plant` have existed since migration 001 and no code has
ever touched them.

## Cross-cutting blockers

Ranked by how many personas are blocked by each. Nothing in this table exists
today.

| # | Blocker | Blocks | Notes |
|---|---------|--------|-------|
| 1 | **No ownership model** | all 5 | No owner/tenant column in any of the 12 migrations. "My data" is inexpressible; `ListBoards` is global. Gates almost everything else. |
| 2 | **No authentication at all** | all 5 | No interceptor, no user table, reflection on, server fully open. `libs/go/grpcauth` + `libs/go/htmxauth` are reusable; `tools/app_registry` is the wiring reference. |
| 3 | **No browser-callable transport** | all 5 | gRPC on `:50051` only — no grpc-gateway, no grpc-web, no HTTP/JSON. Every wireframe screen is unreachable. |
| 4 | **No readings read-path** | all 5 | Seven analytical views exist and the API reads none of them. Grafana queries the DB directly. |
| 5 | **Config ack outcome never exposed** | 1, 2, 3 | `device_config.accepted` / `acked_at` / `rejection_reason` are written by the processor over MQTT and read by no RPC. A rejected push is indistinguishable from no push. |
| 6 | **Regions and plants have no code path** | 1, 3, 5 | Manual SQL only. `SensorConfig.region_id` is unusable from a UI. |
| 7 | **No alerts subsystem** | 1, 3, 5 | No table, no RPC, no evaluator, no delivery. Screen 19 is a wireframe over nothing. |
| 8 | **No structured error model** | 2, 3, 4, 5 | Interpolated `status.Errorf` strings. UIs cannot map failures to sentences. |
| 9 | **No pagination or batch** | 1, 2, 4 | A 200-board push cannot report that 40 failed. |
| 10 | **No device lifecycle commands** | 1, 3 | Reboot, decommission, reset-to-defaults, OTA/firmware version have no path. Screen 03's Danger Zone is decorative. |

## Data-model defects found while writing these

Two pre-existing correctness bugs surfaced, both in SCD2 tables whose entire
purpose is to prevent the error they permit. Neither is an API gap — both need
a migration and a decision in V1.

1. **A rewire silently forks sensor identity.** `idx_sensor_hw_address` is
   unique on `(board_id, i2c_address, sensor_type_id, mux_path::text)`. Changing
   an I2C address or mux channel through a config push therefore creates a new
   `sensor_id` with empty history, because no code path updates a sensor in
   place — so `sensor_hw_history` cannot record the exact event it was built
   for. See [02-tinkerer.md](02-tinkerer.md) blocker 3.
2. **Moving a plant retroactively rewrites its history.** There is no
   `plant_region_history`; `plant.region_id` is a plain mutable column, and
   `v_sensor_reading_with_plant` joins `p.region_id = e.region_id`. A repot
   re-attributes every past reading to the new location. The view is
   point-in-time in the time dimension only, not the place dimension. See
   [05-grower.md](05-grower.md) blocker 2 and modelling question 1.

## Open modelling questions

Carried up from the persona docs; the plan must answer these before the
affected epics are sound.

1. Does a plant's placement have history at all — add SCD2 placement history
   and repoint the view, or declare plants immovable? (Grower Q1)
2. Two plants in one region means a reading maps to both; the view fans out.
   What does "this plant's light history" mean then? (Grower Q2)
3. Region depth rules are Room → Shelf → Pot, so a pot *is* a region. Is a
   plant bound 1:1 to a pot-region, or placeable anywhere in the tree? (Grower Q3)
4. Where do care thresholds live? `plant_type` is `common_name` + `species` and
   nothing else. (Grower Q4)
5. Is ownership per-board, per-region, or per-household? Personas 3 and 5 want
   household scope; persona 2 wants per-board.
6. What does "push an update" mean for a non-technical owner — a firmware OTA
   (does not exist) or an idempotent re-send of the last accepted config?
   (Household Owner Epic D)
7. Does the gawker lane get anonymous access via signed share tokens, a public
   BFF route, or neither in V1? (Gawker Epic A)

## Wireframe conflicts to resolve

- **Device list:** screen 02 self-labels "(legacy)"; screen 14 owns the
  canonical `#/devices` route and is richer. 14 is canonical.
- **Device detail:** screen 03 links to 10 as "preview redesign (v2)", but 10 is
  view-only and drops config history and the Danger Zone, while 19 other screens
  still link to 03. Unresolved.
- **Alerts:** screen 19 is absent from both the shell nav and `preview.html`.
- `preview.html` is stale (18 sections, omits alerts) and is gitignored.
