# Relationship to AR-5 (allocation cutover)

**AR-7 does not conflict with what remains of AR-5, and belongs strictly
before it.** AR-5a shipped `AllocateVersion` fully implemented and *inert* —
no domain is at stage `allocate`, `plan.go`'s `autoIncrementVersion` is
untouched, and `version_allocation` is empty in every environment. That is the
cheapest possible moment to re-home its storage into `artifact` (AR-7b):
after a cutover, migration 007 would be folding rows a live release path is
writing.

What AR-7 does to each of AR-5's remaining items:

| AR-5 leftover | Effect of AR-7 |
|---|---|
| Replace `autoIncrementVersion` in `plan.go` | Unchanged in shape — but the plan step is already writing an intent row per target by then (`version_source = 'tag'`), so the cutover flips *who authors the version*, not who writes the row. |
| Seed each domain's starting version from its tags at cutover | Largely obviated: by cutover the registry already holds tag-derived versions as real `artifact` rows, so "latest" is answerable from the table that `AllocateVersion` reads. |
| Parity check ("allocated versions match tag-scanning") | Becomes a query rather than a manual comparison against soak logs — `version_source` records which path authored each version, and a shadow allocation can be compared against the recorded tag version while a domain is still at `observe`. |
| Per-domain cutover gate, rollback by moving the stage back | Unchanged, and now carries two more meanings (recording mandatory at `promote`, compose-time chart enforcement at `allocate`). |
| Remove the release workflow's version-allocation concurrency group | Unchanged AR-5 concern. AR-7's `UNIQUE (owner_id, kind, version)` across all states is the constraint that makes dropping it safe. |

The one ordering rule: **do not move any domain to `allocate` before AR-7b
lands.** Everything else in AR-7 can be built, merged, and run while every
domain sits at `observe`. **AR-7b landed first** (migration `007`,
`AllocateVersion` writing `artifact` rows directly, `BeginPublish`/
`FailPublish`, the reaper) — satisfying this rule before the cutover could
happen.

**The AR-5 cutover is now complete.** `domain_adoption` — the per-domain
`observe`/`promote`/`allocate` stage this whole document is written against
— is dropped. Every domain now allocates versions unconditionally:
`AllocateVersion` serves any domain, `CheckChartHermeticity` is
unconditionally enforced, and `RecordArtifact`'s pre-AR-7 direct-create
fallback is gone entirely (see "Artifact lifecycle" above). AR-7b's fold-in
of `version_allocation` into `artifact` (the row above, "Per-domain cutover
gate, rollback by moving the stage back") was already load-bearing for this
— the cutover only had to flip *who authors the version* for every domain at
once, exactly as this table anticipated, because `artifact`'s
`UNIQUE (owner_id, kind, version)` and state machine already spanned every
domain regardless of stage. See PLAN.md's "AR-5 — cutover status" for the
as-built account of how the cutover landed.

