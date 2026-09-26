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

**A move out of a milepebble also releases its parent.** Deleting only
the milepebble-level row left the parent milestone's own row in place, so
moving an entity to a competing milestone left it delivered by two — the
state the refusal above exists to prevent, reached through the very verb
the refusal names. The parent's row is now released for exactly one
destination shape: a milestone other than the cut's own parent. Every
other destination — the backlog bucket, a sibling cut, the parent itself —
is a narrowing or a relocation within the same parent, and keeps the
parent's claim; that boundary is FR9's shipped contract for abandoning a
cut, which shares this transaction body, and is pinned by
`TestAbandonStore_Abandon_FR9_MilepebbleDirect` and the conformance
suite. Where a *sibling* cut of the same parent still delivers the
entity, the parent must keep its row (FR3's subset invariant), so a
competing milestone cannot be the destination at all and the move is
refused outright with `ErrEntityDeliveredBySiblingCut`, naming the cut to
move the entity out of first.

**The importer is on this path too.** `MilestoneStore.AddAssociation`
(FR16) is the pre-existing, session-less write the markdown importer uses
to reconstruct a brief, and an import is a `Delivers` write like any
other: it enforces the same single-delivery-parent refusal, with the same
`ErrEntityDeliveredByCompetingMilestone` and the same message, via the
shared `refuseCompetingMilestoneDelivers`. Re-asserting a milestone's own
association stays idempotent, so an unchanged brief re-imports cleanly.

**`Must not foreclose` is not a delivery claim, and had been recorded as
one.** The importer's second pass used to call the same `AddAssociation`
for a brief's `Must not foreclose: LB1, LB4` list, and `AddAssociation`
always wrote a `delivers` row. Every decision a brief listed as
must-not-foreclose therefore ended up *looking* delivered by every
milestone that listed it — LB1 by six, in krill's own brief. That is both
wrong on its own terms (the renderer reads the two relations apart) and
exactly the multi-milestone-owner state the refusal forbids, which is how
enforcing the rule here first surfaced. The pass now writes through
`AddMustNotForecloseAssociation`, which shares the transaction and the
milestone-existence guard but records `relation = must_not_foreclose` and
carries no delivery-parent rule — a constraint many milestones must respect
is not a delivery claim by each of them.
