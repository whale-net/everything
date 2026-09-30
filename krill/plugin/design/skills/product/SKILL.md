---
name: product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones as krill entities (Product, FeatureSet, LoadBearingDecision, Milestone). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /krill-design:design <product-id> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned past ~20 FRs.
---

# product

Produces the **product brief** — the level above a design, which exists to
make each design small. It answers *what is this product, for whom, what
must be true across all of it, and in what order do we build it*, and
deliberately not *what does the system do in detail* — that is
`/krill-design:design`, once per milestone. A single design pass over a
whole product produces 60-80 FRs, which is context-hostile and unsafe to
implement in one shot; a durable higher-level brief lets each milestone's
spec be small without being short-sighted.

The artifact is krill entities (`Product`, `FeatureSet`,
`LoadBearingDecision`, `Milestone`, written through a `DesignSession`), not
a document — see `krill/plugin/shared/CONVENTIONS.md` for the
design-session mechanics.

## Usage

```
/krill-design:product "short product description"
/krill-design:product <design-session-id>   # resume an in-flight product session
/krill-design:product <product-id>          # amend an existing product
/krill-design:product                       # no args — ask what the product is
```

| Parameter | Default | Effect |
|---|---|---|
| `--milestones <n>` | `3` | Target number of milestones. A guide, not a cap — over ~6 usually means the capability map is really two products. |
| `--fr-budget <n>` | `12` | Per-milepebble Requirement backstop recorded on each milestone (`create_milestone`'s `fr_budget`); architect enforces it during `/krill-design:design --milestone`. |
| `--resume-agents` | off | Continue the same producer/architect subagents via `SendMessage` for follow-up rounds (steps 5-7) instead of spawning fresh ones. Doesn't reach amendment (step 8), which is a separate invocation. |

## When *not* to use it

A single feature added to an existing system goes straight to
`/krill-design:design`. Use this skill when the request is a product or
subsystem that doesn't exist yet, when "what's in v1" is genuinely
unsettled, or when a design has ballooned past ~20 FRs (feed the ballooned
draft in as the description). If a single *milestone's* design re-balloons,
prefer splitting the roadmap with an extra milestone unless it genuinely
spans a new domain-sized subsystem.

## The artifact

One `Product` (name, vision); one `FeatureSet` per capability-map area (a
`LoadBearingDecision` attaches to the `FeatureSet` it constrains, never the
bare `Product`); one `Milestone` per roadmap entry. A krill-hosted
milestone's status lives on the `Milestone` entity
(`set_milestone_status`/`get_milestone_status_history`), and the entities
are the durable record the moment they're written.

**Hard rule: capability lines (`C7 — Operators can see per-device sensor
health at a glance`), never a numbered FR or NFR, at this level.** A
testable behavior statement belongs in a milestone's design; a brief that
acquires FRs just moves the 80-FR problem up a layer. Capabilities are the
unit of traceability: milestones deliver them, and each milestone's FRs
cite the capability they serve. Keep the map to one screen — if it doesn't
fit, the product is two products.

### Load-bearing decisions

Each is a structural commitment an early milestone must get approximately
right because a later capability depends on it. Three clauses, always:

```
LB3 — Multi-tenancy boundary
  At risk: C12 (org-scoped dashboards), C15 (per-org API keys) — both `Later`.
  Decide now: every row in the reading/device tables carries `org_id` from M1,
  even though M1 only ever has one org and no UI exposes it.
  Stays cheap: the org selector, org CRUD, and authorization logic — adding
  those later touches handlers and templates, not a backfill of every table.
```

`Stays cheap` keeps the list honest: if nothing about a decision is
expensive to reverse, it isn't load-bearing. Aim for 3-8. Architect owns
this section.

### Roadmap

Each milestone is one user-visible outcome — a sentence naming who can now
do what ("a logged-in grower can see one plant's live readings"; "the data
layer" is not a milestone). Each records:

```
M2 — A logged-in grower can see one plant's live readings
Delivers: C3, C4, C6
Must not foreclose: LB1, LB3
Deliberately deferred: multi-plant list (C7 → M3), alerting (C11 → Later)
FR budget: 12
```

`Must not foreclose` is what architect checks each milestone's draft
against; the FR budget is a backstop, since the real constraint is that
every FR traces to a capability the milestone delivers.

## Steps

1. **Resolve the target.** A design-session id: read its latest events and
   resume at the unfinished step. A product id: go to step 8. A description
   (or nothing — ask): proceed to intake.
2. **Open the session.** Intake happens directly in this session, not in a
   thread. Once it settles a name/vision, `create_product`, then
   `open_design_session {product_id, opening_submission}` (the request as
   given) for the rest — producer and architect post `draft`/
   `reconciliation`/`signoff` revision events, as in `/krill-design:design`.
3. **Product intake.** Conduct it conversationally here — it needs live
   back-and-forth, so don't delegate it. Ask about the users and the job
   each is hiring the product for, the end state a year out (capabilities,
   not features or screens), what already exists (anything built on,
   replaced, or not to be broken), what's explicitly never in scope, and
   critically the smallest genuinely useful version. Push back on scope
   here: "what would you cut to have this working next week?" is usually M1.
4. **Draft the brief.** Dispatch `krill-design:producer` (Mode P1) with the
   session id and an explicit `name: "producer-<session-id>"` (and
   `name: "architect-<session-id>"` for step 5) so later rounds can resume
   under `--resume-agents`. It drafts vision, personas, capability map
   (`C1..Cn`, bucketed `Now`/`Next`/`Later`), and non-goals as a `draft`
   event. No current state, load-bearing decisions, or roadmap yet.
5. **Architect current-state pass.** Dispatch `krill-design:architect` in
   Product mode: survey what exists in the affected domains, record
   **Current state**, then derive **Load-bearing decisions** from the
   `Next`/`Later` capabilities, plus open questions and nitpicks. Don't
   skip this even when the answer is "nothing exists yet" — in this repo
   "nothing" often means half-built or recently reverted.
6. **Roadmap and loop.** Dispatch producer (Mode P2) to answer architect's
   questions, fold in current state and load-bearing sections verbatim, and
   add the roadmap; then re-dispatch architect to reconcile. Repeat until
   architect signs off with no blocking questions, capping at 5 rounds and
   summarizing for the user if stuck. With `--resume-agents`, target the
   same named agents via `SendMessage`. Architect checks: does M1 deliver
   something a person can use? Does any outcome sentence name a component
   rather than a user? Does every `Later` capability have an `LB` entry or
   an explicit note that it's cheap to add?
7. **Human gate and publish.** Present the signed-off brief: vision,
   bucketed capability map, load-bearing decisions, and the milestone list
   with what M1 does and doesn't include. Ask for approval, changes, or a
   re-cut. **Changes** → another producer/architect round, then back here.
   **Approved** → dispatch producer (Mode P3) to write the entities:
   `create_feature_set` per capability area, `create_load_bearing_decision`
   per LB, `create_milestone` per roadmap entry (with `add_delivers`,
   `add_must_not_foreclose`, `add_deferral`). This skill runs its own gate
   rather than `/krill-design:review`, which gates a milestone design.
8. **Amendment (existing product).** Reality changes roadmaps, but the
   brief is never edited silently: producer (Mode P2) drafts the change as
   further `append_revision_event` calls (or a new session), architect
   reconciles it when it touches load-bearing decisions or milestone
   ordering (recording that it reconciled even when it finds no impact),
   the user approves the diff, then producer applies it with the relevant
   authoring/amend tools (`amend_load_bearing_decision`, `add_deferral`,
   `move_delivery_scope`, `create_milestone`, ...). Never rewrite a shipped
   milestone's history — ship what shipped, change what's ahead.
9. **Hand off.** Tell the user the brief is written and that
   `/krill-design:design <product-id> --milestone M1` is next, and name
   what M1 contains.

## Downstream

```
/krill-design:design <product-id> --milestone M1
  → /krill-design:review → /krill-work:plan → /krill-work:implement → /krill-work:validate
  → repeat for M2, M3, ...
```

`<product-id>` is the krill `Product` surrogate id. The entities are read
fresh at the start of every milestone's design, which keeps milestone N+1
aware of decisions made in milestone N without re-reading N's spec.
