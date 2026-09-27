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
`Refuse` turns a submitted value that *differs* from the entity's own
current placement into `store.ErrPlacementChange` naming the field and
the operation that does move or re-kind an entity. The advice is per
(kind, field), because only some pairs have a verb: a Feature's
`feature_set_id` names `reparent_feature`, a Non-Goal's `kind` names
`resolve_non_goal`, a kind that is genuinely parented but has no move
verb is told to create-then-void, and a field that is not that kind's
parent column at all is told so -- a Milestone carrying
`feature_set_id` is not pointed at a Feature's verb, because a Milestone
has no FeatureSet parent and there would be nothing to move. A value
equal to the placement already there is not a change, so a client that
echoes back the parent and kind it read amends normally (FR b62ed47a); a
field the kind has no column for has no current value, so anything sent
for it differs. That is why the guard is not decidable from the request
body alone: each surface reads the entity's current row with
`CurrentPlacement` in the same request, before any supersede runs.

**Name uniqueness is create's, unchanged** (FR b2767a89). Every spec-axis
table's scope-qualified name index is partial on `valid_to IS NULL`, so
closing the prior revision before the INSERT is what lets an amend keep
its own name, while a name a *live* sibling already holds still trips the
same index Create trips. The store maps that violation onto
`store.ErrNameConflict` (errors.go) so the caller gets the collision named
rather than a raw Postgres error; `writeStoreError` maps it to 409, the
same status a create's collision gets.

## The move verb, where one kind has one

`krill/store/reparent.go`'s `ReparentStore` is `supersede` again, used to
change WHERE a `Feature` sits rather than what it says: it closes the
current row and opens a successor under a different `feature_set_id`,
carrying the id, `scope_id`, name, description, `position`, and
`display_number` forward. It is a separate store, not another
`AmendStore` method, precisely so the contract above stays true of
`AmendStore`.

**Why `display_number` must survive.** A Feature's `Cn` is the token a
rendered brief, a rendered roadmap, and a human all cite; it is frozen at
creation (`nextDisplayNumber`, position.go) and frozen numbers are the
whole point of the column. A move that renumbered would silently repoint
every citation of the Feature, which is the same class of damage
`nextDisplayNumber`'s counting of voided rows exists to prevent.

**Why the target is same-scope AND same-Product.** A scope holds many
Products, so a same-scope check alone would permit a move that crosses a
Product boundary -- and two things break there. `Cn` is numbered
product-wide, so the number lands in a second numbering space; and
`renderMilestones` (krill/render) resolves a milestone's `Delivers: Cn`
line by looking the entity up in the product slice it is rendering, so a
Feature that left the product would vanish from the `Delivers` line of the
milestone that delivers it, with no error anywhere. The move is refused
by name (`ErrReparentAcrossProduct`) rather than allowed, because
crossing Products is a different operation from moving a Feature between
two FeatureSets of the same Product. A move to the parent the Feature
already has is refused too (`ErrReparentNoOp`): it would spend a revision
on a change that changed nothing.

**Position is carried forward, not renumbered.** Every read orders by
`(position, name)`, so a position that collides with a sibling in the
target set falls through to `name` deterministically -- no error, no lost
row. The cost is that a moved Feature can land at an arbitrary point in
the target set's rendered order rather than at the end of it; appending
instead would discard the ordering a caller chose when they created the
Feature.

**What follows the Feature and what does not.** Requirements key on
`feature_id`, the Feature's immutable id, so they follow for free -- the
slice reads join through it (`ListRequirementsByFeatureSet`). A
LoadBearingDecision is FeatureSet-scoped (`feature_set_id`), so it belongs
to the FeatureSet and stays with the FeatureSet the Feature moved out of.

**The delivery axis is untouched, structurally.** `entity_milestone` keys
on `(entity_id, milestone_id, relation)` and a move reuses that same
`entity_id` while changing no `milestone_id`, so the single-delivery-parent
rule in [`34-single-delivery-parent.md`](34-single-delivery-parent.md) --
which is enforced entirely in terms of `entity_id` and `product_id`, and
never mentions a FeatureSet -- cannot be reached by a move at all. A
Feature a milestone delivers keeps its `Delivers` and must-not-foreclose
rows, and a competing milestone is refused exactly as before. Both halves
are pinned by `TestReparentFeature_DeliveredFeature_KeepsItsSingleDeliveryOwner`
and `TestReparentFeature_CompetingDeliveryStillRefusedAfterMove` in
`krill/store/reparent_integration_test.go`, rather than left to be
discovered in production.

Feature is the only kind with this verb. Milestone and Requirement
reparenting is a separate open question, not a gap this file forecloses;
`supersede` takes its table, columns, and scan as arguments and hard-codes
nothing about any one kind, so the others can reuse it as they are.

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

**`ReparentStore` is reached by one MCP tool, no HTTP endpoint.**
`reparent_feature` (krill/mcp/tools/amend.go, registered by the same
`RegisterAmendAll` fan-out as the eight amends) takes `feature_id` and
the new `feature_set_id` and returns the unchanged id, the same
`amendPersonas` list and `krillSessionInput` gate as the amends -- it is
registered there because the refusal above names it, and a refusal that
points at a verb the mount lacks is a dead end. There is still no HTTP
twin, so an HTTP amend refusal names a verb that route does not expose;
the two surfaces' message is the same one, and closing that gap is
separate work.
