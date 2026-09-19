-- 013_delivery_shipment: per-delivered-item shipment record (issue #2686,
-- FR10, C28). Makes "partially complete" (migration 012's status set)
-- concrete: for a milestone or milepebble, which of its `delivers`
-- associations (migration 004/010's `entity_milestone`, relation=
-- 'delivers') have actually shipped, and which have not. This is the
-- "not-yet-shipped scope" definition the re-cut (FR5) and abandon (FR6)
-- work on this board both consume.
--
-- ============================================================================
-- LB3 / NFR2 / NFR3 -- delivery_shipment is append-only, NOT SCD2
-- ============================================================================
-- Shipping an item is a fact that accrues, never a flag flipped in place:
-- following the same boundary call migration 012 made for
-- milestone_status_event, this table has no `valid_from`/`valid_to` pair,
-- no "current row" concept, no UPDATE path, and no DELETE path anywhere in
-- krill/store. A row once written is immutable forever (NFR2 -- "recorded
-- as an addition to history, never an overwrite"). Marking the same item
-- shipped twice appends a second row rather than being rejected as a
-- duplicate or upserted -- idempotency for a *reader* ("is this shipped")
-- is a query concern (IsShipped/DeliveryBreakdown), never enforced by
-- collapsing the write. This is precisely what makes NFR3 ("abandoning
-- never discards or alters the record of what it already shipped")
-- structurally true rather than a rule someone has to remember to
-- respect: there is no code path in this package that could alter or
-- remove a delivery_shipment row even if it tried.
--
-- Shipped-ness hangs off the association, not the spec entity: a Feature
-- shipped *in a given milestone* is a fact about that (entity, container)
-- pair, not about the Feature itself, since the same Feature can be
-- associated to a different container (e.g. a milepebble and its parent
-- milestone, or two independent milestones) as a separate fact each time.
-- So `entity_id` and `milestone_id` are both plain columns here, never a
-- `shipped_at` column added to `feature`/`requirement`.
--
-- `milestone_id` covers milepebbles too (FR9's kind widening on
-- `milestone_ref` already established): a milepebble's delivery breakdown
-- is scoped to its own (narrower) `delivers` subset via the same
-- mechanism AddMilepebbleDelivers already enforces, not a second table or
-- a kind-specific column.
--
-- This table's own `shipped`-per-item fact is deliberately not derived
-- from, and does not derive, a container's own coarser status (migration
-- 012): a milestone can be marked `shipped` before every delivered item
-- has a delivery_shipment row, and can be `partially complete` with any
-- mix of shipped/unshipped items. Reconciling the two is out of this
-- migration's scope.
CREATE TABLE delivery_shipment (
    id                           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    scope_id                     UUID        NOT NULL REFERENCES scope(id),
    entity_id                    UUID        NOT NULL,
    milestone_id                 UUID        NOT NULL REFERENCES milestone_ref(id),
    note                         TEXT        NULL,
    created_by_acting_iss        TEXT        NOT NULL,
    created_by_acting_sub        TEXT        NOT NULL,
    created_by_acting_kind       TEXT        NOT NULL,
    created_by_on_behalf_of_iss  TEXT        NOT NULL,
    created_by_on_behalf_of_sub  TEXT        NOT NULL,
    created_by_on_behalf_of_kind TEXT        NOT NULL,
    created_at                   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- ShippedEntityIDs/DeliveryBreakdown scan by milestone_id alone (every
-- delivered item of one container, in one query); the composite index
-- serves IsShipped-shaped lookups of one specific (entity, container)
-- pair without a second table scan.
CREATE INDEX delivery_shipment_milestone_idx ON delivery_shipment(milestone_id);
CREATE INDEX delivery_shipment_entity_milestone_idx ON delivery_shipment(entity_id, milestone_id);
