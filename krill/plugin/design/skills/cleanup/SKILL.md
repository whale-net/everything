---
name: cleanup
description: Mutating hygiene pass over one krill Product's delivery axis — walks every open milestone and milepebble, ages them from their status history, diagnoses drift (status lagging the task lanes, stale or empty containers, orphaned claims, unshipped scope on finished work), proposes fixes, and on approval applies them (set_milestone_status, mark_delivered_item_shipped, move_delivery_scope, abandon_milestone, abandon_task). Use for "clean up krill", "close out old milestones", "this product's roadmap is stale". For a read-only look, use status instead.
---

# cleanup

The mutating counterpart to `status`. `status` reports; `cleanup` diagnoses
and fixes. Lives in `krill-design` because milestone status, shipment,
re-cut, and abandon writes only mount on `/mcp/design` — `krill-work` cannot
make them (CONVENTIONS.md "Known limitations").

## Usage

```
/krill-design:cleanup <product-id>
/krill-design:cleanup                     # no args — list products and ask
```

| Parameter | Default | Effect |
|---|---|---|
| `--stale-days <n>` | `14` | A non-terminal container with no status transition and no task movement for this long is "stale". |
| `--dry-run` | off | Diagnose and print the proposed actions; write nothing. |
| `--yes` | off | Apply every **reversible** action without asking. Irreversible ones (`abandon_milestone`, `abandon_task`) still need explicit approval. |

## Steps

### 1. Resolve the product

`init_session {}` → keep `krill_session_id` and `scope_id`. With no
`<product-id>`, call `list_products {scope_id}` and offer them through
`AskUserQuestion` (name + id; if more than 4, list the rest in text and let
the user answer via "Other"). Never guess.

### 2. Gather (reads only)

1. `list_product_delivery {product_id, view: "summary"}` — every milestone and
   milepebble with its status. Drop `shipped`/`abandoned`; those are closed.
2. For each remaining container (milestone **and** milepebble):
   - `get_milestone_status_history {milestone_id}` — last transition time =
     its age in the current status; first transition time = its total age.
     No history means `not started` since creation.
   - `get_delivery_breakdown {milestone_id}` — delivered items, shipped vs
     not.
   - `list_tasks {milestone_id}` — lanes, attempt counts, live claims.
     `get_task {id}` only for tasks that need it (a live claim, a non-`Done`
     task in a container you're about to call finished, an `escalated`
     state) — `current_claim.lease_expires_at`, `claim_id`, `state`,
     `notes`.
3. `get_backlog {product_id}` — just to report its size at the end.

With more than ~8 open containers, dispatch one `general-purpose` subagent to
do step 2 and return only the per-container fact table below, so this
session doesn't absorb every raw payload.

Per-container fact row: `id | name | parent | status | days in status |
tasks by lane | live claims (expired?) | escalated | delivered shipped/total`.

### 3. Diagnose

Apply these rules, in order; one container can hit several.

| # | Finding | Signal | Proposed fix | Reversible? |
|---|---|---|---|---|
| D1 | **Work finished, status lags** | ≥1 task, every task `Done`, status `planned`/`in progress`/`partially complete` | Mark each not-yet-shipped delivered item shipped (`mark_delivered_item_shipped`), then `set_milestone_status` → `shipped` (via `in progress` first if `planned`) | No (shipment is append-only) — ask |
| D2 | **Work started, status lags** | any task out of its first lane or ever claimed, status `designed`/`planned` | `set_milestone_status` → `in progress` (via `planned` if `designed`) | Yes |
| D3 | **Planned but no tasks** | status `planned`/`in progress`, zero tasks, stale | Report; suggest `/krill-work:plan`. If the user says it's dead, treat as D5 | — |
| D4 | **Partially shipped, stalled** | `in progress`, some items shipped, stale, no live claims | `set_milestone_status` → `partially complete`; offer `move_delivery_scope` of the unshipped items to the backlog (`get_backlog`'s id) or a sibling milepebble | Yes / recut is reversible |
| D5 | **Stale, nothing moving** | non-terminal, stale, no live claims, no shipped items | Offer `abandon_milestone {note}` (sweeps unshipped scope to backlog) **or** `move_delivery_scope` the scope elsewhere and leave it open | Abandon: **No** |
| D6 | **Empty shell** | no delivered scope, no tasks, `not started`/`in design`, stale | `abandon_milestone {note: "empty — no scope or tasks"}` | **No** |
| D7 | **Orphaned claim** | `current_claim.lease_expires_at` in the past (or claimed > `--stale-days` with no heartbeat movement) | `abandon_task {task_id, claim_id, reason}` — frees it, counts one attempt, lane unchanged | **No** (attempt counted) |
| D8 | **Escalated / at attempt cap** | `state: escalated`, or attempts at cap | Report only — `release_task`/`requeue_task`/`cancel_task` are Swarm-Operator `/mcp/ops` tools; point at the operator UI | — |
| D9 | **Parent behind milepebbles** | every milepebble `shipped`, parent milestone not | Treat the parent like D1 | No — ask |
| D10 | **Open notes** | tasks carrying open `scope-note`s in a container you're closing | Report; suggest `/krill-work:plan` triage **before** closing it (planner owns `transition_note_lifecycle`) | — |

A status move must follow the edge table — `not started → in design →
designed → planned → in progress → {partially complete ↔ in progress} →
shipped`, with `abandoned` reachable from any non-terminal status. Chain the
intermediate transitions; never skip. Re-setting the current status writes
nothing.

**Don't** close a container that has a live, unexpired claim — someone is
working it. **Don't** propose D1/D9 when any task is non-`Done`.

### 4. Propose

Print one table, grouped by container, oldest first:

```
Container            Status (days)       Finding  Action
M3 Delivery axis     in progress (41d)   D1       ship 4 items, → shipped
 └ M3a Recut         planned (41d)       D2       → in progress
M5 Console           not started (60d)   D6       abandon (irreversible)
```

Then, unless `--dry-run`:

- Reversible actions: one `AskUserQuestion` (multiSelect) to pick which to
  apply, or apply all with `--yes`.
- Irreversible actions: one `AskUserQuestion` per container naming exactly
  what will be written (items marked shipped, scope swept to backlog,
  attempt counted). Offer the reversible alternative (recut instead of
  abandon) as an option where one exists.

### 5. Apply

Use the `krill_session_id` from step 1 for every write. Pass a `note`/`reason`
on every write: `cleanup: <finding> — <one-line why>`, so the status history
explains itself. Apply children (milepebbles, tasks) before their parent.
On a write error (an illegal edge, `ErrClaimNotCurrent`, a shipped-item
refusal), record it and move on — don't retry blindly.

### 6. Verify and report

Re-read `list_product_delivery {product_id, view: "summary"}` and print
before → after per touched container, every action that failed and why,
every D3/D8/D10 item left for a human, and the backlog's item count. Point
at `/krill-work:status <milestone-id>` to re-inspect any one container.
