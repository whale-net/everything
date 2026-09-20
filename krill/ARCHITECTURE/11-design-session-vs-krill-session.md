# `design_session` vs `krill_session` (FR1, FR8, #2542)

M2's `design_session` (migration `008`) is the single most confusable
thing in this milestone relative to M1's `krill_session` (migration `003`)
-- write this down explicitly, mirroring "`krill_session` and the two
session ids" below:

- **`krill_session`** is the write-gate row FR3's `init` mints per
  mutating call: one fixed acting/on-behalf-of pair, gating exactly one
  request. It is minted fresh every time a caller calls `init`, and it
  never accumulates state of its own beyond that one pair.
- **`design_session`** is the longer-lived container FR2's
  `revision_event` rounds accumulate under. A single `design_session`
  spans many separate `krill_session`-gated calls, from potentially
  different actors, over its lifetime -- a producer-role Agent's `draft`,
  an architect's `reconciliation`, and a Requirement Contributor's
  `answer` are three different calls, each gated by its own, distinct
  `krill_session`, all landing `revision_event` rows against the same
  `design_session`.

`design_session.opened_by_krill_session_id` records which `krill_session`
gated the `open` call that created the row -- **provenance only**. It is
never the source of a later `revision_event`'s own attribution:
FR2 requires every `revision_event` to carry its own `acting_*`/
`on_behalf_of_*` pair directly (sourced from whichever `krill_session`
gated *that* event's call), so resolving a round's attribution by
following `opened_by_krill_session_id` back through `design_session` would
be wrong the moment a session's second round is written by a different
actor than its first.

`design_session` is append-only, not SCD2 (LB3) -- migration
`008_design_session.up.sql`'s comment: M2 ships no update path over this
row, and its mutable state (how many rounds it has seen, what those rounds
said) is entirely derived from its `revision_event` log, never written
back onto the `design_session` row itself.

**FR8's opening submission** (`design_session.opening_submission`) is a
column on `design_session`, not a sixth `revision_event.event_type` value
and not a `draft` event with empty deltas. FR2's `event_type` enum is
closed at exactly five values (`draft`, `reconciliation`, `answer`,
`signoff`, `ruling`), none of which names a Requirement Contributor's raw,
pre-entity idea -- widening that enum for this would contradict the plan
that fixes it, and reusing `draft` would misattribute a contributor's
plain language as a producer-role Agent's proposal (FR9/FR10 make `draft`
specifically the Agent's act).

