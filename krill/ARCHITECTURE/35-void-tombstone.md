# Void: the SCD2 close-WITHOUT-successor (FR d38d726e, FR 2a3a8eef)

`krill/store/void.go`'s `VoidStore` is the other half of the boundary
call [`ARCHITECTURE/33-scd2-amend-all-spec-kinds.md`](33-scd2-amend-all-spec-kinds.md)
draws. Amend is close-and-open; void is close and **nothing**:

```sql
UPDATE <table> SET valid_to = NOW() WHERE id = $1 AND scope_id = $2 AND valid_to IS NULL;
-- and no INSERT. That absence is the tombstone.
```

Seven kinds are void-able — Product, FeatureSet, Feature, Requirement,
Persona, NonGoal, LoadBearingDecision. Milestone is not: a milestone is a
delivery-axis fact with append-only delivery records, and FR 39373553
gives it amend for its authoring fields instead.

**The distinction is observable.** After an amend the id has a current
revision again; after a void it never does. That is also why a void
refuses rather than cascading (see below) — the alternative to
tombstoning something the delivery axis still points at is supersession.

## What a void does and does not release

| | Freed by a void? | Why |
|---|---|---|
| the unique **name** | **yes** | every name index is partial on `valid_to IS NULL`, so a closed row stops occupying its name and a later create may reuse it |
| the **display number** | **no — retired forever** | see below |
| the **id** | no | it is the audit handle; `ListVoidEvents` is how you reach it |

The number is the sharp edge. `nextDisplayNumber` (`store/position.go`)
computes `MAX(display_number)` over **every row the product has ever
had, closed ones included** — not just its current rows. If it counted
only current rows, voiding the highest-numbered Feature in a product would
drop the MAX by one, and the very next create (reusing the freed *name*)
would be handed the voided entity's number. A `C7` citation already
rendered for the voided Feature would then resolve to a different one:
exactly the silent renumbering LB2 exists to prevent.

Two halves hold that up:

- **the store** — `nextDisplayNumber` counts closed rows, and its
  `feature_set` join is unfiltered on the parent's currentness so that
  voiding a Feature's FeatureSet later cannot hand its numbers back out.
  No DB constraint can express "unique per product" here, because neither
  table carries a `product_id` — the same store-owns-the-invariant
  position migration 002's LB2 parentage note takes.
- **the schema** — `void_event`'s partial unique index on
  `(scope_id, entity_kind, product_id, retired_display_number)` makes "a
  number is retired for exactly one entity in one product, forever"
  DB-enforced (migration 021).

## The two refusals

Both run **before** the tombstoning `UPDATE`, so a refused void issues no
write at all — there is no rollback to hide a misordered check behind.

- `ErrEntityDelivered` — `deliveryRefusal` checks `entity_milestone`
  **and** `delivery_shipment`, and does *not* narrow to
  `relation='delivers'`. A Must-not-foreclose association and a shipment
  are both references a reader will follow, and a shipment is only ever
  recorded against a Delivers association, so checking one table alone
  would rest the shipped case on an invariant enforced elsewhere.
- `ErrHasLiveChildren` — `childRefusal` walks the full per-kind child set:
  a Product keeps alive FeatureSet, Persona, NonGoal **and
  `milestone_ref`** (a milestone hangs off a product, so voiding the
  product would orphan the whole delivery axis); a FeatureSet keeps alive
  Feature and LoadBearingDecision; a Feature keeps alive Requirement; the
  four leaves keep nothing. "Live" is `valid_to IS NULL`, so an
  already-voided child does *not* keep its parent alive — which is what
  makes voiding bottom-up work.

Every check is scope-qualified (LB1) on both sides: the target lookup
reports another scope's real id as `ErrNotFound` rather than voiding it,
and the child query ignores another scope's children rather than blocking
on them.

## Audit

`ListVoidEvents(ctx, scopeID, kind)` is the only way to reach a tombstone.
An empty `kind` means *every* kind, not *none*. It carries the original
`entity_id` and `retired_display_number` — the identity a `C7`-style
citation resolved to before the void — plus the LB4 subject pair.

## The exposed verb

FR d38d726e opens "a Requirement Contributor can void a mistaken create",
and a Requirement Contributor reaches krill through MCP — so the verb has
to exist above the store or the FR is unreachable. It mirrors the amend
surface ([`33-scd2-amend-all-spec-kinds.md`](33-scd2-amend-all-spec-kinds.md)):

| | HTTP | MCP |
|---|---|---|
| void | seven `POST /{kind}/{id}/void` routes, gated | `void_entity {entity_kind, entity_id, reason}`, gated |
| audit | — (see below) | `list_void_events {scope_id, entity_kind?}`, ungated |

**MCP gets one verb, HTTP gets seven routes.** The seven kinds differ in
nothing a caller can act on — same arguments, same refusals, same returned
id, and the store already dispatches on kind internally. Seven tools would
put seven near-identical descriptions in front of a model choosing between
them on nothing. The discriminator form is not invented: `record_note`
already takes an `entity_kind` over the same spec-axis kinds. HTTP keeps
one route per kind because there the path genuinely *is* the
discriminator, and REST already reads that way.

**No `POST /void-events`.** The audit read is scope-keyed, and every
ungated read in `krill/api` is keyed off a globally-unique entity id
instead; exposing it would have meant either a new store method or a
caller-supplied `scope_id` query parameter that reads any scope's register.
The MCP tool takes `scope_id` explicitly, exactly as `list_products`
already does. Wiring an HTTP twin is open work.

### Reuse by the Non-Goal resolve verb

FR d0021a0f's RETIRE outcome is specified as "the same shape as void".
`VoidStore.VoidNonGoal` is that shape and is deliberately left directly
callable: RETIRE adds one check of its own — that the NonGoal is currently
`deferred` — and its "outcome, actor and timestamp" record is exactly the
`void_event` row `VoidNonGoal` already writes. Nothing here must be
duplicated to add it. Void deliberately does *not* enforce the `deferred`
check itself, because a `permanent` NonGoal may also be a mistaken create,
which is void's own case.

## Testing note

`//krill/store:void_integration_test` uses a pgx query tracer for the
refusal tests rather than asserting only that no row survived. A guard
that ran *after* the `UPDATE` and then rolled back would satisfy a
"writes nothing" assertion while still being misordered, so those tests
assert that no write statement is ever **issued**. Every `scope_id`
argument also has a dedicated cross-scope test, and each was verified to
turn the suite red when that argument is dropped.
