# LeafLab — Product Brief

Product discussion: [#1487](https://github.com/whale-net/everything/discussions/1487)

This file is the canonical entry point for LeafLab's product scope. Start here, then follow the jump table below.

| Section | File | When to read it |
|---|---|---|
| Current state | [product/01-current-state.md](product/01-current-state.md) | Before specifying any milestone — what actually exists, what is scaffolded, and what is broken in the tree today |
| Capability map | [product/02-capability-map.md](product/02-capability-map.md) | To find the `Cn` a requirement traces to, or to see what is deliberately deferred |
| Roadmap | [product/03-roadmap.md](product/03-roadmap.md) | Before designing a milestone — its outcome sentence, `Delivers`, `Must not foreclose`, and `FR budget` |

Live milestone status is **not** in this file. It is tracked as `Ledger: M<n> → <status>` comments on the `Product: leaflab` tracking issue — see `tools/project-manager/CONVENTIONS.md` § Roadmap ledger.

---

## Vision

LeafLab lets a person plug in a board full of sensors and immediately watch their space report on itself. You hand someone a board, they connect it, and readings start showing up — no flashing, no SQL, no Grafana. From a single web UI they name their boards and sensors in whatever words they actually use, describe the places those boards live, and record the plants sitting in those places. When something moves — a sensor from the living room to the kitchen, a plant from one shelf to another — nothing is lost: LeafLab remembers where everything was at every point in time, so a reading taken six months ago is still attributable to the location, sensor, and plant it actually belonged to then.

## Personas

- **Operator/builder** — runs the LeafLab deployment: registers boards, pushes configs, keeps the pipeline healthy, and is the only one who touches infrastructure.
- **Board owner ("gawker")** — was handed a physical board; wants it to work unattended and to open a page and see their metrics feeding in.
- **Tinkerer** — a board owner who adds or rewires sensors after initial setup and expects the UI to catch up without a reflash.

*These three are a deliberate simplification of the finer-grained persona set on the abandoned #1166 branch, not an oversight. At ~10 users, the distinctions that mattered there (household owner vs. grower vs. super admin) collapse into "runs it," "watches it," and "changes the hardware."*

---

## Load-bearing decisions

### LB1 — Owner identity, and the local user record it points at

**At risk:** C15 (write-enforced ownership, `Next`/M2), C22 (read-side scoping, `Later`), C23 (role grants from a UI, `Later`) — and the `leaflab_user_role` grant table itself, which has nothing to hang off without this.

**Decide now:** leaflab owns a `leaflab_user` table keyed on the OIDC `sub`, and every ownable dimension row — `board`, `region`, `plant` — carries a FK to `leaflab_user`, populated from M1 onward even though nothing enforces it and no screen shows it. Ownership is recorded as SCD2 (`board_owner_history`, `valid_from`/`valid_to`) rather than a mutable column, for the same reason everything else in this schema is: boards get handed to people, and "who owned this board when this reading was taken" is the same question as "where was this sensor when this reading was taken." Two consequences the shape has to allow for from the start: `board` rows are minted by the processor with no principal, so a board exists *before* it has an owner and the history table must model unowned as "no open row," not as an owner row starting at `registered_at`; and every FK — ownership and role grants alike — points at the local `leaflab_user` row, never at a raw claim string. Uniqueness of ownership is a property of the *open* row and nothing else: `board_owner_history` carries a partial unique index on `(board_id) WHERE valid_to IS NULL`, never a table-wide `UNIQUE(board_id)`, and "is this board already claimed?" is answered by the presence of an open row, not by the presence of any row. Board ownership is the SCD2 one because boards change hands; the `region` and `plant` owner FKs are plain current-value columns, since those rows are created by their owner in place.

**Stays cheap:** all enforcement (row filters in handlers and views), the admin bypass, sharing and multi-owner, and the entire role model on top of this — the `leaflab_user_role` grant table in M2, C23's grant/revoke UI later, and whatever role names get chosen — because a grant has no history worth backfilling and is meaningful the moment it is written. Swapping identity providers is cheap too: a `sub` change is one `UPDATE` when the FK is local. Retrofitting the ownership column is what is not cheap: by then the boards, regions, plants and readings that exist have no recoverable owner, and there is no source of truth to backfill from. Releasing and re-claiming a board (C25), and transferring a region or plant to someone else — the first is a close-and-open on a history table that already models it, and the second is the same additive move as sensor name history, where the current value becomes the open row and there is no prior transfer to reconstruct.

### LB2 — Server-side dimension state is written by the API, not by the device-config ack path

**At risk:** C9, C10, C11, C13, C14 (`Next`) and C15's enforcement (`Later`).

**Decide now:** the only writer of `sensor.region_id` and `sensor_region_history` today is `ApplyConfigRegions`, fired from an accepted `DeviceConfigAck` in the processor. A UI edit therefore lands only after a full MQTT round trip to a physically-present board, and silently never lands if the board is offline. Commit in the CRUD-UI milestone to the API writing dimension state (names, placements, regions, plants) directly against Postgres as the authoritative path, with `SensorConfig.region_id` — already commented `// server-side only; device ignores this field` in `firmware/proto/config.proto:39` — retired as the transport for placement.

**Stays cheap:** which RPCs exist, their signatures, the UI's shape, gRPC vs. HTTP. Expensive later: every placement recorded via the ack path stamps `valid_from` at ack time rather than edit time, so a later switch leaves a permanently mixed-provenance history that cannot be reconstructed; and LB1's ownership check would have to be retrofitted into an MQTT consumer that has no request principal instead of a request handler that does.

### LB3 — A config push is whole-device replacement, and the API owns composing it

**At risk:** C9, C14 (`Next`), C2's stated semantics (`Now`).

**Decide now:** `ConfigApplier::ApplyFactory` (`firmware/config/config_applier.cc:96`) resets every sensor pool and rebuilds the board's entire complement from the pushed config. `PushDeviceConfig` publishes exactly the caller's `sensors` list with no merge against the last accepted config. So a "rename one sensor" push that carries one entry **destroys every other sensor on that board.** Both `api.proto`'s comment ("Sensors with no matching entry are unchanged") and C2's wording ("push a config to name, enable, disable...") describe override semantics that the firmware does not implement. Decide that the API — not the caller and not the UI — materializes the full desired sensor list by read-modify-write over the last accepted config plus the DB inventory, and that the wire `DeviceConfig` stays whole-state.

**Stays cheap:** which edit intents the UI exposes, and how they are presented. Expensive later: if the first UI ships partial pushes, it silently wipes boards in production; and once any client depends on partial-push semantics, moving to whole-state (or teaching the firmware to merge) is a breaking change to a fleet with no OTA.

### LB4 — Plant placement is SCD2 before anything writes a plant

**At risk:** C12, C13.

**Decide now:** `plant` today is a soft-delete table with a mutable `region_id`, and `v_sensor_reading_with_plant` joins on it. Before the first row is written by anything, move placement into a `plant_region_history` SCD2 table (`valid_from`/`valid_to`, open-interval partial index, value-at-`recorded_at` predicate per `AGENTS.md`), and repoint the view to resolve placement at `recorded_at`. `plant.removed_at` should follow the same convention.

**Stays cheap:** plant types, plant CRUD screens, the plant lens in the UI, whether plants are owned. Expensive later: the first plant move under the current shape silently rewrites that plant's entire reading attribution, and because the pre-move placement was never recorded, no migration can restore it. This is the one entry where the *existing* schema is the trap — `ARCHITECTURE.md` reads as though the plant model is finished, and it is a table with no writer and a wrong shape.

### LB5 — The reading's attribution unit is the sensor, not the board

**At risk:** C11 as worded ("move a **board** to a different location"), and every roll-up query behind C6/C7/C8.

**Decide now:** placement and the `sensor_reading.region_id` snapshot both hang off the sensor. If board-level placement is introduced for the UI's sake — and it probably should be, because "move the board to the kitchen" is how a person thinks — it must be a *convenience that writes sensor placements*, not a second attribution source. Readings resolve their region through the sensor, permanently; a sensor on a 2m cable can legitimately be in a different region from its board.

**Stays cheap:** the UI affordance, bulk-move ergonomics, whether `board` gets a display-only default region. Expensive later: two attribution paths means every reading has two possible regions and every view has to choose one forever, and the choice cannot be revisited without re-deriving history.

### LB6 — Region names are labels; the region parent pointer is an attribution

**At risk:** C10 (nest locations), C11 (pre-move readings attributed correctly).

**Decide now:** `region` is not historised at all — `v_region_path` recomputes the root→leaf path from the *current* `parent_region_id`, and `region.name` is current-value. Renaming a region is a label change and rewriting the label retroactively is acceptable (adding name history later is additive: the current name is a valid open row). **Re-parenting is not a label change** — it changes which readings roll up under "Living room," retroactively and irrecoverably. Either `region.parent_region_id` is SCD2 from the CRUD-UI milestone, or the brief states as an explicit non-goal that hierarchy roll-ups are as-of-now and re-parenting is understood to rewrite them.

**Stays cheap:** region names/descriptions/history, the tree widget, depth limits. Expensive: the same backfill-with-no-source-of-truth problem as LB4.

### LB7 — The device wire contract is one-way expensive because there is no OTA

**At risk:** C14 (`Next`), C16/C17 (`Later`), and any future durability claim under C1.

**Decide now:** flashing is USB-only, so `firmware/proto/*.proto` changes are a physical-access migration across the whole fleet, and a board that is out in someone's house may never get one. Treat the device-facing contract as append-only-and-optional for the CRUD-UI milestone: anything the UI needs about a reading or a sensor that is not on the wire today must be derivable server-side. Two known gaps to record rather than fix in this milestone: `SensorDescriptor`'s single-hop mux (which already forecloses C14 for cascaded-mux sensors), and the absence of a device-side reading timestamp and idempotency key (which forecloses any later store-and-forward).

**Stays cheap:** everything server-side — schema, views, API, UI. Expensive: any required (non-optional) field, any semantic change to an existing field, and anything that makes an un-reflashed board incorrect rather than merely limited.

---

## Non-goals

- **Not a home-automation platform.** LeafLab observes; it does not actuate. It may one day feed a home-automation system, but it will not become one.
- **Not multi-tenant SaaS.** At most ~10 users total. No orgs, no tenancy model, no billing, no self-serve signup.
- **Not a general analytics tool.** Grafana is being retired in favor of the custom UI; ad-hoc and exploratory analysis is expected to stay in SQL against the `v_` views rather than becoming UI surface area. *(Nothing consumes those views on `main` today — this is an intent, not a description of current practice.)*
- **No bespoke identity system, but leaflab owns its own roles.** LeafLab reuses the shared `libs/go/htmxauth` library for sign-in and session handling, and uses `app-registry`'s role implementation as a *reference pattern* (how roles are structured, how DB-backed sessions are wired) — not as a shared dependency and not by borrowing its realm roles. Authentication is delegated to the IdP; **authorization is leaflab's**: role grants live in leaflab's own database, keyed to the `leaflab_user` row from LB1.

---

## Two notes for whoever reads this next

- Plan #1166 (closed; its merged scaffolding reverted in `71d828b0`) and the abandoned worktree at `.pm-worktrees/1472/` are **not** live requirements — see [Current state](product/01-current-state.md); this brief supersedes them.
- The 19 screens under `leaflab/ui/design/wireframes/` are exploratory, not a specification: 02/14 and 03/10 are conflicting duplicates of the same view, 19 is not in the shell nav, and `preview.html` is stale. A milestone may cite them for flavor; it may not cite them as a requirement.
