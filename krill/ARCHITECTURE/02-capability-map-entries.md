# Capability map entries, personas, and non-goals (issue #2488)

The importer (FR16) and renderer (FR13) both need product-brief content
that is not in the `Product -> FeatureSet -> Feature -> {FR, NFR}` chain:
capability-map entries (`Cn`), personas, and non-goals. This task decides
how all three map onto the entity model, so the two downstream tasks agree.

**Personas and non-goals are new product-level tables.** `persona` and
`non_goal` (migration 002) are ordinary spec entities — same SCD2 /
`scope_id` / surrogate-id rules as everything else, single-parent FK to
`product`. `non_goal.kind` (`permanent` | `deferred`) carries
`PRODUCT.md`'s Non-goals split ("Permanent" vs "Explicitly *not*
non-goals — deferred, not foreclosed") the same way `requirement.kind`
carries FR vs NFR: one table, one discriminator column, not two tables
that would otherwise be identical.

**A capability-map entry (`Cn`) is not a fourth parallel table — it maps
onto `Feature`.** `product/02-capability-map.md`'s own opening note
already settles the direction: *"Krill's own entity model retires
`Capability` — `Feature` takes its slot in the
`Product -> FeatureSet -> Feature -> {FR, NFR}` chain."* This task's job
is to confirm that decision requires no schema of its own and to record
why:

- An FR's citation of a capability (`FR4 (C3) — ...`) is structurally
  identical to an FR's citation of its parent — both are "this
  Requirement's `feature_id`, resolved against a real row." Making `Cn`
  its own table would mean every citing FR needs *two* parent-shaped
  references (its `Feature` and its `Capability`) for what is, after
  today's brief is imported, the same entity. `feature` already *is* that
  entity once import treats each capability-map entry as one `Feature`
  row. A test asserting this (a later phase of this task): creating a
  `Feature` (standing in for a capability `Cn`) and a `Requirement` under
  it whose `feature_id` equals that `Feature`'s `id` is the whole proof
  that "an FR → C citation resolves to a real entity id rather than a
  string" — no separate association table or citation-string column is
  needed for that to be true.
- `FeatureSet` is *not* where `Cn` maps: the capability map's own
  "Now/Next/Later" grouping is a delivery-axis concept (which milestone
  ships a capability), not a spec-axis grouping — that is LB6's
  association table (migration 004, a later M1 task), never a column or a
  parent choice on `feature`/`feature_set`. `FeatureSet` remains a
  free-form mid-tier grouping of related Features, assigned by whoever
  authors or imports a product's spec (the importer task's job, not
  this one).
- No column anywhere stores `Cn` itself (`capability_number`, `c_number`,
  etc.) — that would be exactly the display-number trap LB2 exists to
  prevent, applied to a different vocabulary. A `Cn` label is rendered
  from a `Feature`'s `position` among its siblings, precisely like an
  `FRn`/`LBn` label is rendered from a `Requirement`/`LoadBearingDecision`'s
  `position`.

