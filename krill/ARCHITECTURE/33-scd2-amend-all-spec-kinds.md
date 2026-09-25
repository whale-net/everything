# SCD2 amend across every spec-axis kind (FR 8b2e87d1, FR f0f6bc18, FR 39373553, FR b2767a89)

`krill/store/amend.go`'s `AmendStore` is the close-and-open write for
**every** spec-axis entity kind -- Product, FeatureSet, Feature,
Requirement, Persona, NonGoal, LoadBearingDecision, and Milestone. Each
method is the pair `AGENTS.md` names:

```sql
UPDATE <table> SET valid_to = NOW() WHERE id = $1 AND valid_to IS NULL;
INSERT INTO <table> (id, ..., scope_id) VALUES ($1, ..., $scope);
```

All eight share one generic body, `supersede` (amend.go), which row-locks
the current row with `SELECT ... FOR UPDATE` before closing it. That lock
is what makes two concurrent amends of the same id serialize instead of
racing each other for the `(id) WHERE valid_to IS NULL` partial unique
index. Each method's only remaining job is the per-kind INSERT, which
carries the closed row's `id`, `scope_id`, parent id, `kind`, `position`,
`display_number`, and the milestone's own `created_at`/LB4 subject pair
forward unchanged, and takes the caller's replacement `name` plus that
kind's one content field.

**What an amend never does.** It never reparents and never re-kinds
(FR f0f6bc18). The store signatures simply have no parameter to express
either, so the invariant holds structurally rather than by a check that
could be forgotten. The refusal is still a *named* one at the surface, not
a generic unknown-field decode: every amend request body embeds
`store.AmendPlacementChange` (amend.go), a struct of the parent/kind
fields a caller might reach for, which no handler or tool ever applies.
`Refuse` turns any of them into `store.ErrPlacementChange` naming the
field and pointing at the operation that does move or re-kind an entity --
the create/move path, or the resolution path for a kind change.

**Name uniqueness is create's, unchanged** (FR b2767a89). Every spec-axis
table's scope-qualified name index is partial on `valid_to IS NULL`, so
closing the prior revision before the INSERT is what lets an amend keep
its own name, while a name a *live* sibling already holds still trips the
same index Create trips. The store maps that violation onto
`store.ErrNameConflict` (errors.go) so the caller gets the collision named
rather than a raw Postgres error; `writeStoreError` maps it to 409, the
same status a create's collision gets.

## Milestone: the one kind that needed a migration

`milestone_ref` was the last spec-axis table without `valid_from`/
`valid_to` -- migrations 004/010/011/014 all recorded that a milestone is
a bare fact ("this product's roadmap names an M3"), not a value that
changes over time. That call is what left Milestone without an amend path:
a supersession needs somewhere to put the prior revision's `valid_to`.
**Migration 020 (`020_milestone_scd2`)** gives it the same shape migration
002 gave the other seven: `revision_id` becomes the row key, `id` stops
being the primary key, and every unique index is re-scoped to current
rows.

The five child `milestone_id` columns (`entity_milestone`,
`milestone_deferral`, `milestone_status_event`, `delivery_shipment`,
`task`) plus `milestone_ref`'s own self-referencing
`parent_milestone_id` lose their referential actions. A FK cannot target a
column that is not table-wide unique, and an SCD2 table's immutable `id`
deliberately is not. Rather than add a second identity table to keep the
constraint alive, migration 020 applies the boundary migration 002
already drew for every other spec-axis parent link -- a plain UUID column
with parent existence checked by `krill/store` inside the writing
transaction (`currentRowExists`/`errParentNotFound`).

**The delivery axis is untouched by design** (FR 39373553). Status
transitions, shipments, the Delivers/Must-not-foreclose associations, and
deferrals each live in their own append-only table keyed on the immutable
`id`; `AmendMilestone` reuses that same `id`, so none of those tables is
read, written, or even reachable from a supersession. It carries
`outcome` forward from the closed row and replaces only `name` and
`outcome` -- `fr_budget` is unchanged, and the `SetOutcome`/`SetFRBudget`
revise paths remain in-place updates scoped to the current revision.

## Surfaces

**HTTP** (`krill/api/handlers/amend.go`, mounted in `routes.go` behind
`RequireSession`): `POST /products/{id}/amend`,
`/feature-sets/{id}/amend`, `/features/{id}/amend`,
`/requirements/{id}/amend`, `/personas/{id}/amend`,
`/non-goals/{id}/amend`, `/load-bearing-decisions/{id}/amend`, and
`/milestones/{id}/amend`. Each takes a per-kind body -- `name` plus that
kind's content field (`vision`, `description`, `body`, or `outcome`) --
and returns the unchanged surrogate id as `handlers.IDResponse`.

**MCP** (`krill/mcp/tools/amend.go`, on the `/mcp/design` mount via
`RegisterWrite`): `amend_product`, `amend_feature_set`, `amend_feature`,
`amend_requirement`, `amend_persona`, `amend_non_goal`,
`amend_load_bearing_decision`, and `amend_milestone`, each taking a
`krill_session_id` plus the same replacement content.

**History reads are unchanged.** `HistoryStore` still covers Requirement
and LoadBearingDecision only (FR 11/12) -- see
[`22-amend-as-of-history-reads.md`](22-amend-as-of-history-reads.md) for
that read side, and for why the as-of slice assembly's "Product,
FeatureSet, and Feature have no write path that supersedes a row yet"
note now needs the qualifier this file supplies.
