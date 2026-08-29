# Roadmap

Part of the [LeafLab product brief](../PRODUCT.md). Milestone definitions only.

Every milestone below is `not started` except M0, which is a description of what already shipped. Live status is tracked as `Ledger:` comments on the product tracking issue, never by editing this file.

The CRUD UI is cut into five milestones rather than one. The reason is the ownership decision: the first milestone that puts a write button in a browser is also the first milestone that needs write-enforced ownership, and bundling naming, placement, plants, and hardware changes behind that one gate produces a milestone nobody can spec against a 12-FR budget. Each cut below is independently useful on the day it ships, and each one carries the defect fixes that would otherwise make its own headline capability a lie.

`Ships alongside` names non-capability work — defect fixes and schema shapes — that the milestone must land for its outcome sentence to be true. It is not a capability list and nothing in it appears in the capability map.

## M0 — Baseline: readings already flow from board to database

Not a milestone of work; it is the state every later milestone is specced against.

```
Delivers: C1, C2, C3, C4 (already shipped)
FR budget: n/a
```

Anything a later milestone assumes about this baseline should be checked against [`Current state`](01-current-state.md), not against `README.md`, `ARCHITECTURE.md`, or `DATA.md` — architect found all three overstating what exists (plant/region tables with no writer, `board.last_seen_at` as a liveness signal, an ER diagram missing two tables).

## M1 — A signed-in board owner can see their boards and readings in a browser

```
Delivers: C5, C6, C7, C8
Ships alongside: LB1's ownership record — `leaflab_user` plus the ownership FK/history on every
  ownable dimension row, populated but enforced by nothing and shown on no screen;
  a leaflab `ui_sessions` migration, since DB-backed sessions are what let the UI refresh an
  access token for gRPC calls on the user's behalf (`libs/go/htmxauth/README.md` § cookie-mode
  limitation)
Must not foreclose: LB1, LB2, LB5, LB7
Deliberately deferred: all writes — renaming and claiming (C9, C24 → M2), locations and placement
  (C10, C11, C21 → M3), plants (C12, C13 → M4), sensor add/rewire (C14, C19 → M5);
  ownership *enforcement* and role grants (C15 → M2, where the first write lands);
  role grant/revoke UI (C23 → Later); alerting (C20 → Later)
FR budget: 12
```

Read-only, and useful on its own: it is the first time anyone other than an operator with a `psql` prompt can see what their board is doing, and it retires the Grafana question by demonstrating the alternative. Ownership is recorded here and nowhere later, per LB1 — retrofitting it after boards, regions, and readings exist leaves nothing to backfill from. Nothing consults a role in M1: every signed-in user reads everything (C15/C22), so the `leaflab_user_role` grant table lands in M2 with the first write.

## M2 — A signed-in user can claim a board, name their own boards and sensors, and nobody else can

```
Delivers: C9, C15, C24
Ships alongside: config-push composition per LB3 — the API materializes the board's full desired
  sensor list, so renaming one sensor no longer wipes every other sensor on that board;
  authentication and authorization on `PushDeviceConfig`, which today accepts any caller
  writing any region onto any sensor; the `leaflab_user_role` grant table (SCD2, FK to
  `leaflab_user`), with the first admin seeded in a migration rather than granted from a screen
Must not foreclose: LB1, LB2, LB3, LB7
Deliberately deferred: releasing/transferring a board (C25 → Later), locations and placement
  (C10, C11, C21 → M3), plants (C12, C13 → M4), sensor add/rewire (C14, C19 → M5),
  read-side scoping (C22 → Later), role grant/revoke UI (C23 → Later)
FR budget: 12
```

Small on the surface and load-bearing underneath: this is where the write path, the ownership gate, the claim, and the config-push fix all land together. Shipping any edit affordance before this milestone means ten people with browser-reachable writes over each other's hardware, and per LB3 a careless push wipes a board. Note that C9's board half has nothing to rename today — `board` has only `device_id`. The FR budget of 12 is the backstop, not headroom: every FR still has to cite C9, C15, or C24, and anything that cannot goes to a later milestone or a new capability.

**The unowned-board rule**, settled at product level so M2's design round starts from it rather than deriving it:

1. **Unowned is a transient, expected state**, not an error: a board is unowned from the moment it first publishes until someone claims it.
2. **Every signed-in user can see it and its readings** — universal read holds for unowned boards exactly as it does for owned ones (C15, C22).
3. **Nobody can edit or push config to it through the API while unowned.** Device-originated writes — the manifest, readings and config acks the processor consumes — are unaffected by ownership at every stage, owned or not; that is what makes a board visible enough to be claimed in the first place. Claiming is the only write available on an unowned board, and it is available to any signed-in user.
4. **Claiming is first-come.** A second claim on an already-owned board is refused, not queued and not silently reassigned; the admin bypass is the escape hatch for a mistaken claim, and it is a seeded/operational action in M2, not a screen (same treatment as bootstrapping the first admin).

## M3 — A signed-in owner can describe their space and place sensors in it, with history that stays true

```
Delivers: C10, C11, C21
Ships alongside: `SensorCache` invalidation on placement change, so a moved sensor's readings stop
  being stamped with its old region until the board reboots; `region.parent_region_id` as SCD2,
  so re-parenting a location does not retroactively rewrite what rolled up under it
Must not foreclose: LB2, LB5, LB6, LB7
Deliberately deferred: plants (C12, C13 → M4), sensor add/rewire (C14, C19 → M5),
  visual board layout (C18 → Later)
FR budget: 12
```

The milestone that makes a reading mean something to a person rather than to a schema. C11 and C21 are deliberately different capabilities: a reading resolves its location through its sensor, permanently and only (LB5), and a board's recorded location is bookkeeping that no query attributes through. Placement is written by the API against Postgres, not by the device-config ack path (LB2) — otherwise a UI edit silently never lands when the board is offline, and `valid_from` records ack time rather than edit time.

## M4 — A signed-in owner can track plants and move them without losing where they were

```
Delivers: C12, C13
Ships alongside: `plant_region_history` as SCD2 with `v_sensor_reading_with_plant` repointed to
  resolve placement at `recorded_at`, and `plant.removed_at` on the same convention, before
  anything writes the first plant row; reconciling `DATA.md`'s ER diagram (omits `plant`
  and `plant_type`) with `ARCHITECTURE.md` (lists them as part of the working model)
Must not foreclose: LB2, LB4, LB5
Deliberately deferred: sensor add/rewire (C14, C19 → M5), alerting (C20 → Later)
FR budget: 8
```

Greenfield despite the DDL already existing: `plant` and `plant_type` have no writer anywhere in the repo. That is the opportunity — the shape can be fixed for free right now, and the first plant move under the current shape silently rewrites that plant's entire reading attribution with no recoverable pre-move truth (LB4).

## M5 — A tinkerer can change a board's hardware and the UI keeps up

```
Delivers: C14, C19
Ships alongside: `UpsertSensor` no longer minting a second `sensor_id` when a rename and a rewire
  arrive in the same manifest; `i2c_address` recordable in `sensor_hw_history`
Must not foreclose: LB3, LB7
Deliberately deferred: provisioning switch and LED (C16 → Later), board-served Wi-Fi config page
  (C17 → Later), visual board layout (C18 → Later), read-side scoping (C22 → Later);
  cascaded-mux sensors stay unsupported — the manifest carries single-hop mux only, and
  changing that is a physical-access migration across the whole fleet (LB7)
FR budget: 8
```

The last milestone that completes the `Now` bucket's original overclaim honestly: C4 covered rename, and rewire lands here with the two defects that make it false today.

## Later — not yet cut into milestones

- **Board provisioning (C16, C17)** — a firmware workstream, not a UI one: a physical mode switch, LED indication, and an HTTP config page the board serves itself. Needs its own intake round before it can be cut into milestones, and LB7 makes every part of it a physical-access change to the fleet.
- **Visual board layout (C18)** — depends on how sensors are actually described after M5.
- **Alerting (C20)** — a wireframe exists (`19-alerts.html`) and nothing else.
- **Read-side scoping (C22)** — only if the deployment stops being ten people who know each other.
- **Role grant/revoke UI (C23)** — until then the `leaflab_user_role` grants seeded in M2 are edited operationally.
- **Releasing a board (C25)** — the second hand-off. LB1's open-row model already makes this a close-and-open on a history table, so deferring it costs nothing.
