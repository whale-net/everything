# Roadmap

Forward-looking only — AR-M through AR-8-and-beyond above are shipped, not
milestoned here. Ordered so each milestone is independently useful and
builds toward the vision's named "next horizon" (post-deploy validation →
auto-rollback), with the approval gate deferred furthest out pending LB1.

### M1 — A release operator can see, at a glance, whether an entire environment is healthy right now

Delivers: C9 (closes its one remaining gap — environment-level
aggregation, on top of what's already shipped)
Must not foreclose: LB2 (the aggregate view must be built as a query/view
over `promotion_sync_event`, not by folding health status into
`promotion_event`'s enum)
Deliberately deferred: automatic rollback action (C10 → M2); approval gate
(C11 → M3)
FR budget: 12
Design note (non-load-bearing, flagged by architect): build the
aggregation as a query/view over `promotion_sync_event`, not by
subscribing to the RabbitMQ SSE bus for "live" data — the same trap LB3
calls out for C10 applies here even though a stale aggregate view isn't a
broken trigger.

### M2 — A release operator no longer has to manually roll back a bad promotion

App Registry rolls back to the prior artifact automatically when the
environment health view from M1 says the new one isn't healthy.

Delivers: C10
Must not foreclose: LB2 (decision policy reads `promotion_sync_event`,
doesn't widen `promotion_event`'s enum); LB3 (the trigger must come from
the durable Temporal/writeback path, never from the RabbitMQ SSE bus)
Deliberately deferred: approval gate (C11 → M3)
FR budget: 12

### M3 — A promotion attempt generates a trackable approval-request scoped to (environment, build), and only executes once that request is resolved

Instead of a boolean gate checked inline in the promote call, promoting a
build to an environment creates a first-class approval-request artifact;
the promotion itself is downstream of, and an artifact of, resolving that
request (approve or reject). Scoped to the promotion-layer seam only — see
LB1's updated wording.

Delivers: C11
Must not foreclose: LB1 (now resolved for the promotion-layer seam only;
the release-trigger-layer stub's fate is explicitly not decided by this
milestone and stays an open question beyond this brief); LB4 (approver
identity is a Keycloak role check through the existing `server/auth`/
`grpcauth` model, not a bespoke table)
Deliberately deferred: ephemeral/per-PR environments (C12 → Later, not yet
roadmapped); the release-trigger-layer approval stub's disposition
(`worker/release/approval.go`'s `CheckApproval`) — has no capability
number of its own and is explicitly out of scope for M3, not folded into
C11 (see LB1)
FR budget: 12 (contingent on design intake resolving: whether one
approval-request covers a given (environment, build) pair exactly once or
every promotion attempt; whether a rejected request can be re-requested;
whether the request is visible/actionable from the same UI surface as
promotions; and — architect flag — a pending request must be represented
without reusing the `promotion` table's SCD2 row, since
`promotion_current_idx`'s uniqueness is per (environment, target/app),
coarser than per-build, and an unapproved build's row would otherwise show
up as "currently deployed" via `v_current_promotion`, which M1's health
view and M2's rollback decision both already consume)
