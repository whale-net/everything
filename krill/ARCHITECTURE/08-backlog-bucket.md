# The backlog bucket vs. the `Later` capability bucket (FR5, FR6, issue #2687)

Migration `014_backlog_bucket` adds a third `milestone_ref.kind` value,
`'backlog'`, following migration 011's own precedent exactly: the bucket is
a `milestone_ref` row (`parent_milestone_id` NULL, one per `(scope_id,
product_id)` via a partial unique index), never a parallel table and never
a new association mechanism — an item lands in it the same
`entity_milestone` (relation='delivers') way it lands in any milestone or
milepebble.

**This is not the product's `Later` capability bucket**
(`krill/product/02-capability-map.md`'s `Later` heading). The two answer
different questions and live on different axes entirely:

- **`Later`** is spec-axis: a capability-map entry (`Cn`) the product may
  someday build, never committed to a milestone at all.
- **The backlog bucket** is delivery-axis: a home for scope that WAS
  committed (it had a real Delivers association to some milestone or
  milepebble) and was then un-committed — by a re-cut (FR5, this issue) or
  an abandon (FR6, a separate issue on this board consuming the same
  bucket and the `RecutStore.MoveScope` primitive this issue adds).

Nothing ever moves between the two: leaving a milestone lands an item in
backlog, never in `Later`, and promoting a `Later` idea into delivery
scope creates a new Feature/Requirement and a fresh Delivers association,
it never resurrects a backlog row.

`krill/store/recut.go`'s `RecutStore` covers `GetOrCreateBacklog` (the
idempotent resolve-or-create, mirroring `MilestoneStore.GetOrCreateRef`),
`ListBacklog` (the bucket's raw entity-id contents, the same shape
`DeliveryShipmentStore.DeliveryBreakdown` returns — `krill/slice.Querier.
GetBacklog` turns it into typed entities, mirroring `GetDeliveryBreakdown`'s
own composition), and `MoveScope` (the not-yet-shipped move primitive).
`MoveScope` checks `delivery_shipment` directly inside its own
transaction rather than calling `DeliveryShipmentStore.DeliveryBreakdown`
(migration 013/issue #2686) through the pool, so the shipped-check and the
move it gates share one snapshot — a move can never touch anything
already shipped (NFR3), and validates its whole `entity_ids` batch before
writing anything (all-or-nothing). It also preserves the FR3 subset
invariant (#2684): a move into a milepebble whose parent does not yet
deliver the entity is only auto-added when the source is a sibling
milepebble of that same parent, and moving an entity out of a milestone
also drops its Delivers association with every milepebble cut from that
milestone. `POST /delivery/move` / `move_delivery_scope` and `GET
/products/{id}/backlog` / `get_backlog` are its HTTP/MCP surfaces
(`krill/api/handlers/recut.go`, `krill/mcp/tools/recut.go`).
`krill/render`'s existing `kind = 'milestone'` filter (migration 010's own
addition, "The milestone authoring schema" above) already excludes any
later kind from `product/03-roadmap.md`, so a `'backlog'` row was excluded
from rendered output before this migration ever created one.

