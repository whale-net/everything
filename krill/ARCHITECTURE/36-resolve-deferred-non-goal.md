# 36 — Resolve: promote vs retire a `deferred` Non-Goal

Settles a Non-Goal held back as "explicitly *not* a non-goal" (FR
`d0021a0f`, FR `19123858`). The companion to
[`33-scd2-amend-all-spec-kinds.md`](33-scd2-amend-all-spec-kinds.md) and
[`35-void-tombstone.md`](35-void-tombstone.md): where those two are the
two ends of the LB3 boundary call, resolve is the third shape.

## The three shapes

| verb | closes the row | opens a successor | changes `kind` | register |
|---|---|---|---|---|
| amend | yes | yes | never (FR `f0f6bc18`) | none — the closed row is the history |
| **promote** | yes | **yes** | **yes** (`deferred` → `permanent`) | `non_goal_promotion` (migration 022) |
| void / **retire** | yes | no | no | `void_event` (migrations 021, 022) |

`promote` is the amend write path with exactly one difference, and that
difference is the entire reason it is a separate verb. Amend's contract is
that it never re-kinds; that guarantee is what lets a reader trust an
amended row's `kind` to be the row's *original* kind. Widening amend to
carry a new kind would buy a convenience by retiring that guarantee, so
`store/resolve.go` re-implements the close-and-open rather than calling
`AmendNonGoal`, which would carry `kind` forward and accomplish nothing.

Everything else on a promote is carried forward untouched — `scope_id`,
`product_id`, `name`, `body`, `position` — because a promote changes what
the Non-Goal *asserts*, not which Non-Goal it is. The name and body
survive so a citation already rendered for the deferred row still resolves.

## The one check resolve adds

`lockDeferredNonGoal` requires the target's current `kind` to be
`deferred`, and runs before either outcome writes anything, so a
`permanent` Non-Goal is refused with no `UPDATE` and no `INSERT` — there is
no rollback for a misordered guard to hide behind. A `permanent` Non-Goal
is the terminal state of *both* outcomes, so there is nothing left to
settle; the refusal names the kind actually found, which is what lets a
caller tell "already resolved" from "never deferred".

The `SELECT ... FOR UPDATE` is also what makes two concurrent resolves of
one id serialize. Without it both would read `deferred` and both would try
to promote, and one would lose on a unique-index violation reported as a
500 rather than as a resolution.

## RETIRE reuses void rather than reimplementing it

A retire opens no successor, so it *is* a tombstone — the same shape a void
leaves — and `retireNonGoal` calls `voidEntityInTx` rather than a second
copy of the close. That is what keeps the two from drifting on the
refusals, on the name-freeing, and on the register row they share.

The transaction is shared rather than composed: the `deferred` check and
the tombstone run in one transaction, because two transactions would leave
a window in which the row changed between the check and the close.

Two consequences fall out of the reuse, and both are deliberate:

- **Void's delivery refusal applies to a retire unchanged.** Retiring a
  Non-Goal a milestone has already delivered would leave that Delivers
  pointing at a row no current read can see — the exact orphaning FR
  `2a3a8eef` exists to prevent. The correct path there is *promote*,
  which keeps the id and so keeps the reference valid. That is also why
  promote needs no delivery refusal of its own: nothing it does can orphan
  a reference, because the id survives.
- **The register is shared, and `outcome` tells them apart.** Both write
  `void_event`; the `outcome` column (migration 022) records which. Without
  it an auditor reading a tombstone would have to guess whether the
  Non-Goal was retracted or had been settled.

## Why PROMOTE gets its own table

A promote leaves a *current* row, so a `void_event` entry would be a false
tombstone — and worse, that table's `(scope_id, entity_kind, entity_id)`
unique index would permanently block a later genuine void of the same
Non-Goal. `non_goal_promotion` also records something `void_event` could
not: the SCD2 row says only that `kind` changed, not that it changed *by
resolution* rather than by a create that happened to reuse the id.

## Reads

A `deferred` Non-Goal is visible in `ListCurrentByProduct` and therefore
in the rendered brief until it is resolved; both outcomes drop it out of
the current-`deferred` set, promote by re-kinding and retire by closing.

- `list_non_goal_promotions` (MCP) — the promote side.
- `list_void_events` (MCP), narrowed to `entity_kind = non_goal` — the
  retire side, distinguished by `outcome = 'retire'`.

There is no HTTP audit read for either, matching void's: both registers
are scope-keyed while every ungated read in `krill/api` is keyed off a
globally-unique entity id, so exposing them would need either a new store
method or a caller-supplied `scope_id` that reads any scope's register.
The MCP tools take `scope_id` explicitly, exactly as `list_products` does.

## Open work

- A `deferred` Non-Goal is a per-row decision, not a batch. A product that
  accumulated many of them has no "resolve all" verb; that is a product
  call, not a missing primitive.
