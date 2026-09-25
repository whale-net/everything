# One delivery parent, batched (FR: a single plan op across FeatureSets; FR: Now/Next/Later never creates a second delivery parent)

**The collision.** LB6 keeps delivery as an *association* rather than a
second parent precisely so that "when will this ship?" has exactly one
answer. krill's own product names its FeatureSets `Now` / `Next` /
`Later` — a purely cosmetic `Name` string, no FK, no constraint — and
that naming reproduces the same conceptual shape one layer up: a second
thing that sounds like a timeline, with no rule for which one a planner
should believe when the two disagree. It bit for real when M6's `Delivers`
set turned out to span the `Later` and `Next` FeatureSets at once
(C19 under one, C29/C30/C31 under the other), so a single unit of
delivered scope needed two separate plan invocations and a human
reconciling the two axes by hand — the exact tax LB6 was written to avoid
on the schema side.

No migration accompanies this. Both halves are properties of the write
path over the `entity_milestone` rows migrations 004 and 010 already
established, so `//krill/migrate/schema` is untouched and the expected
version stays where it is.

**The batch.** `MilestoneAuthoringStore.AddDeliversMany` is the
deliver operation, and it takes the whole list at once. There is no
FeatureSet parameter and none is needed: an `entity_milestone` row hangs
off the *entity's* surrogate id, so a milestone delivers the `Now` and the
`Next` entities in the same call with nothing reconciling them.
`AddDelivers` is the one-element case and now delegates to it, so the two
paths cannot drift. `add_delivers` and `POST /milestones/{id}/delivers`
expose `entity_ids` next to the existing single `entity_id` — one
invocation, all-or-nothing, validated in full before any row is written.

**The single owner.** Associating a Delivers row onto a
`kind='milestone'` container is refused when the entity already has one
under a *different* milestone of the same product
(`ErrEntityDeliveredByCompetingMilestone`, HTTP 409, naming the competing
milestone). Re-adding to the same milestone stays an idempotent no-op, so
re-planning an unchanged milestone is still safe.

The check is deliberately scoped to `kind='milestone'`, and that scope is
the design rather than a gap:

- A **milepebble** only narrows its parent milestone's own claim — FR3's
  subset invariant, which `AddMilepebbleDelivers` already enforces and
  which needs no second owner to be meaningful. Blocking it would break
  the invariant the design already relies on.
- The **backlog bucket** holds scope no milestone delivers at all; it is
  the un-delivered state, not a competing claim.

So "lane" on this axis means a milestone of the product, and a milepebble
is a slice of its parent's lane rather than a rival for it. That is the
one reading under which the Now/Next/Later buckets stay cosmetic: they
name a bucket, the association names the owner, and the two can never
both be answering the same question.

**The re-cut is the way out, and is deliberately ungated.**
`RecutStore.MoveScope` (`move_delivery_scope`) is what
`ErrEntityDeliveredByCompetingMilestone`'s message tells the caller to
reach for, so it is not itself held to the same rule: a move is what
*establishes* the single parent, not a second claim on it. It already
satisfies the same end state on its own terms — it deletes the from-association
(and, when the source is a milestone, every milepebble association under
it) in the same transaction as the insert, so a re-cut never leaves the
entity with two owners either.

**The importer is not on this path.** `MilestoneStore.AddAssociation`
(FR16) is the pre-existing, session-less write the markdown importer uses
to reconstruct a brief. It is a different method from
`AddDeliversMany` and is deliberately left unguarded, so re-importing a
committed brief keeps reproducing exactly what it recorded — including
any historical overlap a brief genuinely describes. The rule binds the
planning path, which is where the collision actually occurred.
