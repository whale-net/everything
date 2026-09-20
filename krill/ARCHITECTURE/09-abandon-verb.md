# The abandon verb (FR6, issue #2688)

`krill/store/abandon.go`'s `AbandonStore.Abandon` composes three pieces
#2685-#2687 each shipped independently, in one transaction: the
append-only `abandoned` status value (`MilestoneStatusEventStore.
RecordTransition`, migration 012), the not-yet-shipped definition
(`DeliveryShipmentStore.DeliveryBreakdown`, migration 013), and the
backlog bucket plus move primitive (`RecutStore.GetOrCreateBacklog`/
`MoveScope`, migration 014, the section above). No new migration or table
-- this task's own scope is the atomic composition and the invariants that
only exist once the three are combined. Each of #2685-#2687's own
transaction-scoped write cores (`recordTransitionTx`, `deliveryBreakdown`,
`getOrCreateBacklogTx`, `moveScopeTx`) is reused directly inside Abandon's
one transaction, never copied a second time -- see abandon.go's own doc
comment for exactly which function each step calls.

**Atomicity.** Reject-target-container, sweep-into-backlog, and
append-transition all happen inside one `pool.Begin()`/`Commit()`: a
caller (or a crash) can never observe the status set without the sweep,
or the sweep without the status. `krill/store/abandon_integration_test.go`
proves this by cascading into an already-abandoned milepebble (a
guaranteed, deterministic failure at the cascade step) and asserting the
parent's own already-applied move and status writes both roll back too.

**Cascade: abandoning a milestone abandons every one of its milepebbles
too.** A milestone's milepebbles are sub-commitments of the same outcome
(migration 011, issue #2684, FR3) -- once the parent is no longer being
pursued, none of its milepebbles are either. Abandon cascades: abandoning
a milestone with live milepebbles abandons each one in the same
transaction, each getting its own `abandoned` `MilestoneStatusEvent` row
(FR9/FR12's per-container history stays accurate) and its own
not-yet-shipped sweep into the same backlog bucket (a milepebble always
shares its parent's product). If any milepebble under the milestone is
already abandoned, the *whole* call is rejected (`ErrAlreadyAbandoned`)
rather than silently skipped -- consistent with the double-abandon check
applied to the top-level target; a milestone with an already-abandoned
milepebble must be dealt with explicitly rather than have the cascade
quietly decide what to leave alone. Abandoning a milepebble directly
cascades no further (a milepebble has no children).

**Not reversible: truncate, never rewind.** There is no un-abandon verb
anywhere in this milestone -- not in the store, not over HTTP (`POST
/milestones/{id}/abandon`), not over MCP (`abandon_milestone`). A revived
commitment is a new milestone or milepebble; the backlog's scope re-cuts
into it via `RecutStore.MoveScope` -- the documented recovery path. Adding
an "un-abandon" later would have to decide which of the swept items to
pull back into which container, a decision this package deliberately
leaves to a fresh `MoveScope` call rather than an inverse of Abandon.

**NFR3 stays structurally true.** Abandon never calls anything that writes
`delivery_shipment` -- shipped associations stay on the abandoned
container exactly as recorded, provable from that table's own rows being
byte-identical before and after (same technique `recut_integration_test.go`
already established for `MoveScope`).

