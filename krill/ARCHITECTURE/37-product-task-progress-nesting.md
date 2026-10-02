# Per-container task progress (FR 59f664ff): one read, overlapping rows

`SummarizeProductTaskProgress` (`krill/store/task_progress.go`), surfaced as
`GET /products/{id}/task-progress` and the `get_product_task_progress` MCP
tool, answers "how far along is this product" for every delivery container at
once. It exists for the "N of M tasks done" figures the Overview, the
Milestones list, a milestone's detail page and each Board swimlane header
need — four displays that would otherwise each cost one count query per
container. No UI renders them yet (those pages are the operator-UI facelift,
M13, status *planned*); today the read's only callers are the API and MCP
surfaces below, so this section is the contract the first UI consumer inherits.

This section covers why it is one statement, why its rows overlap rather than
partition, and what a consumer reads instead when it wants a product-wide
number.

## One statement, not one query per container

`milestone_ref` is left joined to `task` on the container the task was scoped
to, grouped by `(container, lane)`. One pass therefore produces every
container's whole breakdown, including the zero-task containers the `LEFT
JOIN` keeps — a milestone with nothing on it is still a row carrying a zero
total, which a caller renders as "No tasks yet" rather than dropping from the
listing or rendering as "0 of 0".

The container *set* is `ProductTaskScope`, carried over from the product task
read (`task_product_list.go`) rather than re-derived: the same
`IsIncompleteContainerStatus` predicate, the same `incompleteContainerFilterSQL`
rendering of it, and the same walk of `milestone_ref` for `task.milestone_id`.
Both reads also derive each row's two statuses from the latest
`milestone_status_event` via `currentContainerStatusSQL`, never from a stored
column. That is why a progress total and the task read's row count agree per
container, and why neither read can name a container the other does not.

Two refusals keep a wrong-container answer from ever rendering as a
plausible one: `ErrMilestoneOutsideProduct` for a container the product does
not own, and `ErrNotFound` for a product with no current row — never an empty
aggregate. An empty `Containers` slice would otherwise be ambiguous between
"every container is shipped" and "no such product"; only the second is an
error, and it is resolved by a probe the common case never pays for. An
unrecognised scope kind is rejected outright rather than answered as the
whole product.

## Rows overlap: a milestone's `PerLane` spans the whole cut

The one sharp edge. A milestone's join reaches its milepebbles' tasks as well
as its own, while a milepebble's reaches only its own — so

- a **milestone** row's `PerLane` partitions the whole cut: its own direct
  tasks plus every one of its milepebbles';
- a **milepebble** row's `PerLane` partitions that milepebble alone.

Those two are not a partition of the product. Every task scoped to a cut
milepebble appears in **two** rows: its own milepebble's and its parent
milestone's. Summing `containers[].total` therefore counts each such task
twice, and a consumer that derives a product-wide total that way reports a
number larger than the product's real task count — silently, and by exactly
the size of the cut.

The overlap is deliberate, not an artifact: FR 59f664ff requires a
milestone's counts to include its milepebbles' tasks, because the milestone
is the unit a person reads progress at. A milestone row that reported only
its own direct slice would show "0 of 0" on a milestone whose entire work
lives in its milepebbles.

The corollary is the rule every consumer of this read obeys: **render one
progress bar per row, and never a product-wide total derived from the
rows.**

## What a consumer reads instead

`CountProductTasks` (`task_product_list.go`, FR c4ab6c68) with
`ProductTaskScopeIncomplete`, over the same product, is the honest
product-wide figure. It runs `COUNT(*)` over `productTasksQuery` — the exact
`FROM`/`JOIN`/`WHERE` clause `ListProductTasks` pages over, and the same
predicate the progress read reuses for its container set — and joins `task`
to exactly one `milestone_ref` row on `task.milestone_id`. Each task is
counted once no matter how the containers nest, which is precisely what the
progress read's overlapping rows cannot do.

It is one extra read, not a second source of truth: the two share the
predicate, so the total and the rows can never come to describe different
filters. Already shipped as `GET /products/{id}/tasks/count` and the
`count_product_tasks` MCP tool, and it fails rather than reporting `0` when
the store cannot compute it — a count that could not be computed must never
render as an empty queue.

## The arithmetic a per-lane breakdown can rely on

- The five per-lane counts — Scaffold, Implementation, Testing, Validation,
  Done — always partition `Total`, whichever lane a task was last left in.
  `Total` is their sum by definition rather than a separately counted number
  that could drift from them.
- `Done` **is** the Done lane's own count, not a second count of the same
  thing. A per-lane breakdown therefore cannot disagree with the progress bar
  rendered beside it.
- Both are read in one statement, so `Total`, `Done`, `Cancelled` and the
  per-lane counts can never be drawn from different snapshots of a board
  that is moving underneath the read.

## `CancelledTaskCounting`

`store.CancelledTaskCounting` is this read's single, package-wide definition
of how a cancelled task counts, rendered identically by every progress
display:

- It counts in the container's `Total`, in the lane it was left in, and in
  `Cancelled`. It is work the container was given and will never be done;
  hiding it would make a container look emptier than it was.
- Cancelling never moves a task — `task_cancel.go` leaves `current_lane`
  exactly where it was, since a cancelled task is not "in" any lane. So a
  cancelled task is `Done` only if it had already completed when it was
  cancelled; dead-lettering finished work does not un-complete it.
- The corollary: the five per-lane counts still sum to `Total` exactly,
  whether or not any of those tasks is cancelled.

## The two non-additivity hazards

Both reads that partition nothing across a nesting are labelled as consumer
instructions in `README.md`, repeated in their MCP tool description (the
persona with no UI to show it a number is the one most likely to sum), and
pinned by a test that proves the non-additivity rather than restating it:

| Hazard | Read | Cause | Consumer instruction | Proof |
|---|---|---|---|---|
| Per-container task progress (FR 59f664ff) | `GET /products/{id}/task-progress`, `get_product_task_progress` | a milestone row already covers its milepebbles' tasks | never sum `containers[].total` for a product-wide total; read `GET /products/{id}/tasks/count` | `store/task_integration_test.go`'s `..._ContainerRowsAreNotAdditive` |
| Per-product open-notes counts (FR c4ab6c68) | `GET /console/notes/count`, `count_open_notes` | a note on a since-voided spec entity belongs to no product | show the current product's own figure; never derive a scope-wide total from per-product ones | `store/task_note_console_integration_test.go`'s `..._PerProductFiguresDoNotSum` |