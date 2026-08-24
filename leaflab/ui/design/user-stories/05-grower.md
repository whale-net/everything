# LeafLab — Grower User Stories

Source material for the V1 API plan. This persona is unusual: it is the only
one whose data model already exists and whose code path does not.

## Persona

**The Grower** — the person who cares about the *plants*, not the hardware.
They think "my monstera on the east shelf," never "sensor 47 at I2C 0x23."
They acquire plants, put them somewhere, move them when a shelf gets too hot,
repot them, and eventually lose some. Their questions are about a living thing
over time: "did the fern get enough light last month?", "this one died — what
did its last two weeks actually look like?", "which of my plants is in the
worst spot right now?" Sensors are plumbing to them; a sensor's identity is
interesting only insofar as it explains a plant's conditions.

**This persona is schema-anticipated but API-and-UI-absent.** `plant` and
`plant_type` have existed since `001_initial_schema.up.sql`, complete with a
soft-delete column (`plant.removed_at`), a partial index tuned for exactly this
persona's most common query (`idx_plant_active ON plant(region_id) WHERE
removed_at IS NULL`), and a purpose-built analytical view,
`v_sensor_reading_with_plant`, that does the point-in-time join. And nothing
reads or writes any of it. The gRPC service `leaflab.api.v1.LeafLabAPI` exposes
three RPCs — `PushDeviceConfig`, `GetDeviceConfig`, `ListBoards` — none of
which mention a plant. The Go processor writes `sensor_reading` and never looks
at `plant`. No wireframe in `leaflab/ui/design/wireframes/screens/` has a plant
on it; `05-regions.html` counts "6 sensors" per region and not one plant, and
`07-readings.html` filters by device, type, and region only. Rows in `plant`
can today be created only by hand-written SQL. Every story below is therefore a
story about closing that gap, not about improving something.

## Epic A — Knowing what I own

1. As the grower, I want to see every plant I currently have, with what kind it
   is and where it lives, so the system reflects my collection rather than my
   wiring diagram.
   - Acceptance: a list backed by `plant` joined to `plant_type` and
     `v_region_path`, filtered to `plant.removed_at IS NULL` (the case
     `idx_plant_active` already indexes), showing `plant.name`,
     `plant_type.common_name`, `plant_type.species`, and
     `v_region_path.path_name` (e.g. "Room A / Shelf 1 / Pot 3").

2. As the grower, I want a plant's own page — its type, where it is, how long
   it has been there, and its current conditions — so I can check on one plant
   without first working out which sensor covers it.
   - Acceptance: detail view keyed on `plant.plant_id` showing
     `plant.created_at`, current region via `plant.region_id` → `v_region_path`,
     and the latest reading per `sensor_type.name` (`illuminance`,
     `temperature`, `humidity`) drawn from `v_sensor_reading_with_plant`
     filtered to that `plant_id`.

3. As the grower, I want a catalog of plant *kinds* separate from my individual
   plants, so "monstera" is defined once and not re-typed for every pot.
   - Acceptance: CRUD over `plant_type` (`common_name`, `species`), with
     `plant.plant_type_id` selected from that catalog. A `plant_type` still
     referenced by any `plant` row cannot be deleted — the FK is
     `REFERENCES plant_type(plant_type_id)` with no cascade.

4. As the grower, I want to see plants that have no sensor coverage at all, so
   I find out my new cutting is unmonitored before I rely on its charts.
   - Acceptance: any `plant` whose `region_id` has no row in `sensor` (nor in
     any descendant region per `v_region_path.path_ids`) is flagged
     "unmonitored"; the plant detail page states this instead of rendering an
     empty chart that looks like a sensor outage.

## Epic B — Plant lifecycle (none of these writes exist)

5. As the grower, I want to add a plant and place it in a region in one step,
   so acquiring a plant is a normal action and not a SQL insert.
   - Acceptance: one write creating a `plant` row with `region_id`,
     `plant_type_id`, `name`; `created_at` defaults to `NOW()` and is the
     instant from which `v_sensor_reading_with_plant` begins attributing that
     region's readings to this plant.

6. As the grower, I want to move a plant to a different region, so the model
   keeps up when I rearrange a shelf.
   - Acceptance: **undefined today.** `plant.region_id` is a plain mutable
     column and there is no `plant_region_history` table — unlike sensors,
     which get `sensor_region_history` (SCD2 `valid_from`/`valid_to` after
     `011_scd2_naming.up.sql`). An `UPDATE plant SET region_id = …` silently
     rewrites the past: `v_sensor_reading_with_plant` joins
     `p.region_id = e.region_id`, so every reading the plant accumulated in its
     old region instantly re-attributes to the new one. See modelling question
     1 — this story cannot be accepted until that is resolved.

7. As the grower, I want to repot a plant — new pot, same plant — without the
   system treating it as a different plant or losing its history.
   - Acceptance: if a pot is a region (per the min-depth-3 Room → Shelf → Pot
     rule in `05-regions.html`), a repot is a region change and reduces to
     story 6. The plant's `plant_id` and `created_at` must not change.

8. As the grower, I want to record that a plant died or was given away, so it
   leaves my active list without deleting the record of what it experienced.
   - Acceptance: sets `plant.removed_at`; the plant drops out of the active
     list (`removed_at IS NULL`) but every historical reading it was joined to
     stays reachable, because `v_sensor_reading_with_plant` bounds the join by
     `p.removed_at IS NULL OR p.removed_at > e.recorded_at` rather than
     dropping removed plants outright.

9. As the grower, I want to put a new plant in the pot the dead one vacated,
   and have the two histories stay separate.
   - Acceptance: two `plant` rows share one `region_id` with non-overlapping
     `[created_at, removed_at)` intervals. `v_sensor_reading_with_plant`
     already partitions them correctly by `recorded_at`; the UI must not merge
     them into "the pot's history."

10. As the grower, I want to correct a mistake — wrong type, typo in the name,
    placed in the wrong pot on day one — without that correction looking like
    a move.
    - Acceptance: an edit path distinct from story 6's move, explicit about
      whether it is rewriting history or recording a change (question 1 again).

## Epic C — Conditions through a plant's eyes

11. As the grower, I want a plant's light/temperature/humidity history over a
    time range, so I can answer "did the fern get enough light last month?"
    - Acceptance: a time-series read over `v_sensor_reading_with_plant`
      filtered by `plant_id` and a `recorded_at` range, grouped by
      `sensor_type_name`. This view exists and is read by nothing today.

12. As the grower, I want that history to reflect where the plant *was* at each
    moment, not where it is now, so moving a plant doesn't retroactively
    rewrite its past.
    - Acceptance: readings are attributed via `sensor_reading.region_id` — the
      snapshot the processor stamps at insert, which is already historically
      correct for *sensors* — joined against the plant's occupancy interval.
      The sensor half of this is right; the plant half is not (blocker 2).

13. As the grower, I want to compare two of my plants over the same window, so
    "the east shelf is brighter" stops being a guess.
    - Acceptance: multi-`plant_id` series over `v_sensor_reading_with_plant`
      on one shared `recorded_at` range and one `sensor_type_name`.

14. As the grower, I want to know when a chart is showing me a gap versus a
    zero, so a dead sensor doesn't read as a dark shelf.
    - Acceptance: readings with `sensor_reading.valid = FALSE` (exposed as
      `valid` on `v_sensor_reading_enriched`, inherited by
      `v_sensor_reading_with_plant` via `e.*`) render as gaps, never values.

15. As the grower, when two of my plants share a pot or a shelf, I want the UI
    to tell me the readings are shared rather than pretending each plant has
    its own sensor.
    - Acceptance: `v_sensor_reading_with_plant` fans out to one row per
      (reading × active plant) — its own header comment says so. Where more
      than one active `plant` maps to a reading's `region_id`, the plant page
      states that the series is shared, and names the siblings. See question 2.

## Epic D — Care expectations and postmortems

16. As the grower, I want to know what conditions a *kind* of plant wants
    ("monstera: 200–800 lx"), so a number on a chart means something to me.
    - Acceptance: **no home for this exists.** `plant_type` has exactly
      `plant_type_id`, `common_name`, `species` — no range columns. Either
      `plant_type` gains per-`sensor_type` ranges, or a new table does. See
      question 3.

17. As the grower, I want a plant's chart to show its target band, so
    "under-lit" is visible without me remembering the number.
    - Acceptance: depends on 16; a shaded band per `sensor_type_name` on the
      plant's series, rendered from the plant's `plant_type_id`.

18. As the grower, I want to be told when a plant has been outside its band for
    a sustained period, so I find out from the system rather than from the
    plant.
    - Acceptance: **alerts do not exist** anywhere in the schema, the
      processor, or the API. `19-alerts.html` is a wireframe with no backing
      table. Out of scope for V1 unless explicitly added.

19. As the grower, when a plant dies I want to look at its final weeks, so I
    learn something instead of guessing.
    - Acceptance: given `plant.removed_at`, a retrospective over
      `v_sensor_reading_with_plant` for `[removed_at - N days, removed_at)`
      across all three `sensor_type` values, remaining reachable after the
      plant leaves the active list.

20. As the grower, I want to export a plant's readings as a file, so I can keep
    a record or ask someone else about it.
    - Acceptance: CSV of that plant's `v_sensor_reading_with_plant` rows for a
      range. No export path exists today for any entity.

## Modelling questions this persona forces

These are **unresolved**. The plan must answer them; the schema currently
implies one answer and the region rules imply another.

1. **Does a plant's placement have history?** It does not today.
   `plant.region_id` is a mutable column with no `plant_region_history`
   sibling, so a move is a destructive `UPDATE` that retroactively reassigns
   every past reading — the exact failure `sensor_region_history` was built to
   prevent for sensors. Either add SCD2 placement history (`valid_from` /
   `valid_to`, per AGENTS.md) and repoint `v_sensor_reading_with_plant` at it,
   or state that plants may never move and enforce it. There is no third option
   that keeps history honest. Note also that `plant.created_at` /
   `plant.removed_at` are precisely the synonym naming AGENTS.md forbids for
   SCD2 intervals.

2. **What does "this plant's history" mean when a region hosts several
   plants?** `v_sensor_reading_with_plant` fans out — a reading with two active
   plants in its region produces two rows, and any naive `SUM`/`COUNT` over the
   view double-counts. Is plant→region 1:1 (one plant per pot-region, enforced
   by a partial unique index on `plant(region_id) WHERE removed_at IS NULL`),
   or N:1 with shared series and explicit "shared with" UI? Pick one: the view
   supports N:1 and the UI has no answer at all.

3. **Where do care thresholds live?** Per `plant_type` (all monsteras want the
   same light), per `plant` (this one is by a north window), or a separate
   `plant_type_threshold(plant_type_id, sensor_type_id, min, max)` table?
   `plant_type` has no columns for it today. And thresholds without alerts are
   decoration — decide in the same breath whether V1 renders bands only, or
   whether an evaluation path and an alert store are in scope.

4. **Is a pot a region, and is a plant bound to one?** The region rules in
   `05-regions.html` — max 12 children per parent, minimum depth 3 — imply
   Room → Shelf → Pot, so a pot is a `region` row. But `plant.region_id` has no
   depth constraint: nothing stops a plant being attached to "Room A". Does V1
   require plants to sit at leaf/pot depth (checkable via
   `v_region_path.depth`), or accept a plant anywhere in the tree and resolve
   sensors by walking `v_region_path.path_ids`?

5. **What is a repot?** Same plant, new pot-region — story 6 with different
   words? Or a lifecycle event worth recording in its own right (soil changed,
   root pruned), which needs a table that does not exist?

6. **Who owns a plant?** There is no user, owner, or tenant column in any of
   the 12 migrations. "My plants" is not expressible. Ownership must be
   invented before any of Epic B can be authorized, and `libs/go/grpcauth`
   gives subject + realm roles only — never ownership.

7. **Do regions get write endpoints too?** Regions are also SQL-only today. A
   grower who cannot create a pot cannot place a plant in one, so the plant
   write path is blocked on a region write path that no persona has yet
   claimed.

## What this persona must NOT be able to do

- Push or edit device configs (`PushDeviceConfig`), change sensor I2C
  addresses, mux paths, or `sensor_chip` assignments — that is the tinkerer.
- Register, rename, reboot, or decommission boards, or navigate primarily by
  `board.device_id`. Device identity is plumbing here.
- Move or rename *sensors* (`sensor_region_history`, `sensor_name_history`) — a
  grower moves plants; moving the hardware is a different job with different
  consequences.
- Hard-delete anything. Plant removal is `plant.removed_at`; no
  `DELETE FROM plant`, and no deleting a referenced `plant_type`.
- Edit or backfill `sensor_reading` rows, or alter a reading's stamped
  `region_id` / `config_version`.
- See other people's plants, once ownership exists (question 6).
- Restructure the region tree above pot level, or violate the max-12-children
  and min-depth-3 rules.

## API surface this persona needs

None of these exist. The service has three RPCs, all device-config or board.

| Endpoint | Purpose | Auth requirement |
|----------|---------|------------------|
| `ListPlants(region_id?, include_removed?, page)` | The collection — active by default (`idx_plant_active`) | Authenticated; owner-scoped once ownership exists |
| `GetPlant(plant_id)` | Plant detail + current placement via `v_region_path` | Authenticated, owner-scoped |
| `CreatePlant(region_id, plant_type_id, name)` | Acquire and place | Authenticated, owner-scoped write |
| `UpdatePlant(plant_id, name?, plant_type_id?)` | Correct a mistake — not a move | Authenticated, owner-scoped write |
| `MovePlant(plant_id, region_id, effective_at?)` | Move / repot — **semantics blocked on question 1** | Authenticated, owner-scoped write |
| `RemovePlant(plant_id, removed_at?)` | Soft-delete via `plant.removed_at` | Authenticated, owner-scoped write |
| `ListPlantTypes()` / `UpsertPlantType(...)` | The `plant_type` catalog | Read: authenticated. Write: elevated (shared catalog) |
| `GetPlantReadings(plant_id, sensor_type, from, to, agg?)` | Epic C — reads `v_sensor_reading_with_plant` | Authenticated, owner-scoped |
| `ComparePlantReadings([plant_id], sensor_type, from, to)` | Side-by-side over one window | Authenticated, owner-scoped |
| `ExportPlantReadings(plant_id, from, to)` | CSV | Authenticated, owner-scoped |
| `ListRegions(parent_id?)` / `CreateRegion(...)` | Needed to place a plant at all (question 7) | Read: authenticated. Write: elevated |

## Top blockers

1. **No plant API of any kind.** `LeafLabAPI` is `PushDeviceConfig`,
   `GetDeviceConfig`, `ListBoards`. Every story above needs an endpoint that
   has never been declared in the proto.
2. **No placement history for plants.** No `plant_region_history` table; a move
   is a destructive `UPDATE plant SET region_id`, which silently rewrites all
   past attribution in `v_sensor_reading_with_plant`. Epics B and C are unsound
   until this is fixed — the single highest-priority item on this list.
3. **No ownership model.** Zero user/owner/tenant columns across all 12
   migrations. "My plants" cannot be expressed, so no grower write can be
   authorized. `libs/go/grpcauth` supplies subject + roles, not ownership.
4. **No readings read-path.** Nothing serves `sensor_reading` or any `v_` view
   over the API; Grafana reads the database directly. Epic C has no transport.
5. **No region write path.** Regions are hand-written SQL only, so a grower
   cannot create the pot they need to place a plant into.
6. **No home for care thresholds.** `plant_type` is `common_name` + `species`
   and nothing else.
7. **No alerts.** No table, no evaluator, no delivery. `19-alerts.html` is a
   wireframe over nothing.
8. **No plant anywhere in the UI.** Not one of the 19 wireframe screens shows a
   plant; `05-regions.html` counts sensors per region and `07-readings.html`
   filters by device/type/region. The plant lens must be designed, not
   restyled.
9. **No pagination, no structured errors, no HTTP/JSON.** gRPC on `:50051`
   only — no grpc-gateway, no grpc-web — so a browser UI for this persona has
   no reachable transport today.

## Relationship to the Household Owner persona

Same person, different hat, most of the time — and the difference is *write
intent*, not technical skill.

- **Same:** Epic C's "how has this plant been doing" and Epic A's "what do I
  have" are shared verbatim. Both want plant-first navigation, both want a gap
  to look like a gap (story 14), both are indifferent to `board.device_id`.
- **Genuinely different:** the grower *changes the world* — Epic B's
  create/move/repot/remove writes are the grower's alone. The household owner
  is a read-only "is my fern ok?" glance and would be satisfied by a status
  badge; the grower needs the ranged series behind it, comparisons across
  plants (story 13), and the postmortem (story 19).
- **Also different:** the grower is the one who cares that a move must not
  rewrite history (question 1) and that a shared pot fans out (question 2). The
  household owner will never notice either — which is exactly why those
  decisions must be made on the grower's terms.
