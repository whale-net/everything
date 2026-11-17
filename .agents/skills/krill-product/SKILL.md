---
name: krill-product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones as krill entities (Product, FeatureSet, LoadBearingDecision, Milestone). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /krill-design <product-id> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned far past ~12 FRs per milepebble.
---

# krill-product

Produces the **product brief**, the level above a design, so each design stays
small. It answers *what is this product, for whom, what must be true across
all of it, and in what order do we build it*; the detail is `/krill-design`, once
per milestone.

The artifact is krill entities (`Product`, `FeatureSet`, `LoadBearingDecision`,
`Milestone`, written through a `DesignSession`), not a document. Load
`references/CONVENTIONS.md`, `references/agents/producer.md`, and
`references/agents/architect.md` via `get_skill_resources` before dispatching
personas.

## Usage

```
/krill-product "short product description"
/krill-product <design-session-id>   # resume an in-flight product session
/krill-product <product-id>          # amend an existing product
/krill-product                       # no args — ask what the product is
```

| Parameter | Default | Effect |
|---|---|---|
| `--milestones <n>` | `3` | Target milestone count; a guide, not a cap. Over ~6 usually means two products. |
| `--fr-budget <n>` | `12` | Per-milepebble Requirement backstop recorded on each milestone (`create_milestone`'s `fr_budget`); architect enforces it in `/krill-design --milestone`. |
| `--resume-agents` | off | Continue the same producer/architect subagents via `send_message` for steps 5-7 instead of spawning fresh ones. Doesn't reach amendment (step 8). |

## When *not* to use it

A single feature on an existing system goes straight to `/krill-design`.
Use this skill for a product or subsystem that doesn't exist yet, when "what's
in v1" is unsettled, or when a design has ballooned far past ~12 FRs per
milepebble (feed the ballooned draft in as the description). If one
milestone's design re-balloons, prefer an extra milestone unless it spans a
new domain-sized subsystem.

## The artifact

One `Product`; one `FeatureSet` per capability area, a thematic group of
capabilities that belong together (a page, surface or subsystem), never a
timeframe like `Now`/`Next`/`Later` (a `LoadBearingDecision` attaches to the
`FeatureSet` it constrains, never the bare `Product`); one `Milestone` per
roadmap entry, whose status lives on the entity
(`set_milestone_status`/`get_milestone_status_history`).

**Hard rule: capability lines (`C7 — Operators can see per-device sensor health
at a glance`), never numbered FRs or NFRs, at this level.** Testable behavior
belongs in a milestone's design.

**Load-bearing decisions** are structural commitments an early milestone must
get approximately right because a later capability depends on them: `At risk`,
`Decide now`, `Stays cheap` (always all three), 3-8 total, architect-owned.

**Roadmap:** each milestone is one user-visible outcome sentence naming who can
now do what, with `Delivers`, `Must not foreclose`, `Deliberately deferred` and
`FR budget`.

## Subagent setup

Before dispatching `krill-producer` or `krill-architect`, call `get_skill_resources`
for `["references/CONVENTIONS.md", "references/agents/producer.md", "references/agents/architect.md"]`
and register them once per conversation with `define_subagent`:
- `krill-producer`: `system_prompt` from `references/agents/producer.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-architect`: `system_prompt` from `references/agents/architect.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.

Invoke them with `invoke_subagent` (`Model: "inherit"`, `Workspace: "inherit"`).

## Steps

1. **Resolve the target.** Design-session id: read its latest events and resume
   at the unfinished step. Product id: step 8. Description (or nothing — ask):
   intake.
2. **Open the session.** Intake happens in this session, not a thread. Once a
   name/vision settles, `create_product`, then `open_design_session {product_id,
   opening_submission}` (the request as given). Producer and architect post
   `draft`/`reconciliation`/`signoff` events as in `/krill-design`.
3. **Intake.** Conversational and live, so don't delegate it. Ask about users
   and the job each hires the product for, the end state a year out
   (capabilities, not features or screens), what already exists, what's never in
   scope, and the smallest genuinely useful version. Push back on scope: "what
   would you cut to have this working next week?" is usually M1.
4. **Draft the brief.** Dispatch `krill-producer` (Mode P1) with the
   `design_session_id` and `krill_session_id` (and save its `conversationId`
   for `--resume-agents`). It posts vision, personas, capability map and
   non-goals as a `draft` event; no current state, LBs or roadmap yet.
5. **Architect current-state pass.** Dispatch `krill-architect` in Product
   mode to record **Current state** and derive **Load-bearing decisions**, plus
   questions and nitpicks. Don't skip it when "nothing exists": here that often
   means half-built or recently reverted.
6. **Roadmap and loop.** Dispatch `krill-producer` (Mode P2) to answer architect,
   fold in the architect sections verbatim, and add the roadmap; re-dispatch
   `krill-architect` to reconcile. Repeat until architect signs off with no
   blocking questions, capped at 5 rounds (then summarize for the user). With
   `--resume-agents`, continue the existing subagent `conversationId`s via
   `send_message`.
7. **Human gate and publish.** Present vision, capability map by area, LBs and
   the milestone list, including what M1 does and doesn't contain. Ask for
   approval, changes or a re-cut (using `ask_question` if helpful). **Changes**
   → another producer/architect round. **Approved** → dispatch `krill-producer`
   (Mode P3) to write the entities. This skill runs its own gate; `/krill-review`
   gates a milestone design.
8. **Amendment (existing product).** Also the route when `/krill-plan` stops
   for want of a Milestone: cut it here. The brief is never edited silently:
   `krill-producer` (Mode P2) drafts the change as further `append_revision_event`
   calls (or a new session), `krill-architect` reconciles it when it touches LBs
   or milestone ordering (recording that it reconciled even with no impact), the
   user approves the diff, then `krill-producer` applies it with the entity's
   `amend_*` tool (plus `add_deferral`, `move_delivery_scope`, `create_milestone`,
   …). Never rewrite a shipped milestone's history.
9. **Hand off.** Say the brief is written, that `/krill-design <product-id> --milestone M1`
   is next, and what M1 contains.

## Downstream

```
/krill-design <product-id> --milestone M1
  → /krill-review → /krill-plan → /krill-implement → /krill-validate
  → repeat for M2, M3, ...
```

`<product-id>` is the `Product` surrogate id. Entities are read fresh at the
start of each milestone's design, so milestone N+1 sees N's decisions without
re-reading its spec.
