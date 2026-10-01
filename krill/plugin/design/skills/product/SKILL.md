---
name: product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones as krill entities (Product, FeatureSet, LoadBearingDecision, Milestone). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /krill-design:design <product-id> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned far past ~12 FRs per milepebble.
---

# product

Produces the **product brief**, the level above a design, so each design stays
small. It answers *what is this product, for whom, what must be true across
all of it, and in what order do we build it*; the detail is
`/krill-design:design`, once per milestone. One design pass over a whole
product yields 60-80 FRs, which is unsafe to implement in one shot.

The artifact is krill entities (`Product`, `FeatureSet`, `LoadBearingDecision`,
`Milestone`, written through a `DesignSession`), not a document. Mechanics are in
`krill/plugin/shared/CONVENTIONS.md`; the brief's shape (capability map, LB
format, roadmap entries) is in `agents/producer.md` and `agents/architect.md`.

## Usage

```
/krill-design:product "short product description"
/krill-design:product <design-session-id>   # resume an in-flight product session
/krill-design:product <product-id>          # amend an existing product
/krill-design:product                       # no args — ask what the product is
```

| Parameter | Default | Effect |
|---|---|---|
| `--milestones <n>` | `3` | Target milestone count; a guide, not a cap. Over ~6 usually means two products. |
| `--fr-budget <n>` | `12` | Per-milepebble Requirement backstop recorded on each milestone (`create_milestone`'s `fr_budget`); architect enforces it in `/krill-design:design --milestone`. |
| `--resume-agents` | off | Continue the same producer/architect subagents via `SendMessage` for steps 5-7 instead of spawning fresh ones. Doesn't reach amendment (step 8). |

## When *not* to use it

A single feature on an existing system goes straight to `/krill-design:design`.
Use this skill for a product or subsystem that doesn't exist yet, when "what's
in v1" is unsettled, or when a design has ballooned far past ~12 FRs per
milepebble (feed the ballooned draft in as the description). If one
milestone's design re-balloons, prefer an extra milestone unless it spans a
new domain-sized subsystem.

## The artifact

One `Product`; one `FeatureSet` per capability-map area (a `LoadBearingDecision`
attaches to the `FeatureSet` it constrains, never the bare `Product`); one
`Milestone` per roadmap entry, whose status lives on the entity
(`set_milestone_status`/`get_milestone_status_history`). Entities are the
durable record the moment they're written.

**Hard rule: capability lines (`C7 — Operators can see per-device sensor health
at a glance`), never numbered FRs or NFRs, at this level.** Testable behavior
belongs in a milestone's design. Capabilities are the unit of traceability:
milestones deliver them and each milestone's FRs cite the one they serve.

**Load-bearing decisions** are structural commitments an early milestone must
get approximately right because a later capability depends on them: `At risk`,
`Decide now`, `Stays cheap` (always all three), 3-8 total, architect-owned.

**Roadmap:** each milestone is one user-visible outcome sentence naming who can
now do what, with `Delivers`, `Must not foreclose`, `Deliberately deferred` and
`FR budget`. `Must not foreclose` is what architect checks each milestone's
draft against; the budget is a backstop, since the real constraint is that
every FR traces to a delivered capability.

## Steps

1. **Resolve the target.** Design-session id: read its latest events and resume
   at the unfinished step. Product id: step 8. Description (or nothing — ask):
   intake.
2. **Open the session.** Intake happens in this session, not a thread. Once a
   name/vision settles, `create_product`, then `open_design_session {product_id,
   opening_submission}` (the request as given). Producer and architect post
   `draft`/`reconciliation`/`signoff` events as in `/krill-design:design`.
3. **Intake.** Conversational and live, so don't delegate it. Ask about users
   and the job each hires the product for, the end state a year out
   (capabilities, not features or screens), what already exists, what's never in
   scope, and the smallest genuinely useful version. Push back on scope: "what
   would you cut to have this working next week?" is usually M1.
4. **Draft the brief.** Dispatch `krill-design:producer` (Mode P1) with the
   session id and `name: "producer-<session-id>"` (and `"architect-<session-id>"`
   in step 5) so later rounds can resume under `--resume-agents`. It posts
   vision, personas, capability map and non-goals as a `draft` event; no current
   state, LBs or roadmap yet.
5. **Architect current-state pass.** Dispatch `krill-design:architect` in
   Product mode to record **Current state** and derive **Load-bearing
   decisions**, plus questions and nitpicks. Don't skip it when "nothing
   exists": here that often means half-built or recently reverted.
6. **Roadmap and loop.** Dispatch producer (Mode P2) to answer architect, fold in
   the architect sections verbatim, and add the roadmap; re-dispatch architect to
   reconcile. Repeat until architect signs off with no blocking questions, capped
   at 5 rounds (then summarize for the user). With `--resume-agents`, target the
   same named agents via `SendMessage`.
7. **Human gate and publish.** Present vision, bucketed capability map, LBs and
   the milestone list, including what M1 does and doesn't contain. Ask for
   approval, changes or a re-cut. **Changes** → another producer/architect round.
   **Approved** → dispatch producer (Mode P3) to write the entities. This skill
   runs its own gate; `/krill-design:review` gates a milestone design.
8. **Amendment (existing product).** Also the route when `/krill-work:plan` stops
   for want of a Milestone: cut it here. The brief is never edited silently:
   producer (Mode P2) drafts the change as further `append_revision_event` calls
   (or a new session), architect reconciles it when it touches LBs or milestone
   ordering (recording that it reconciled even with no impact), the user approves
   the diff, then producer applies it with the entity's `amend_*` tool (plus
   `add_deferral`, `move_delivery_scope`, `create_milestone`, …). Never rewrite a
   shipped milestone's history.
9. **Hand off.** Say the brief is written, that `/krill-design:design
   <product-id> --milestone M1` is next, and what M1 contains.

## Downstream

```
/krill-design:design <product-id> --milestone M1
  → /krill-design:review → /krill-work:plan → /krill-work:implement → /krill-work:validate
  → repeat for M2, M3, ...
```

`<product-id>` is the `Product` surrogate id. Entities are read fresh at the
start of each milestone's design, so milestone N+1 sees N's decisions without
re-reading its spec.
