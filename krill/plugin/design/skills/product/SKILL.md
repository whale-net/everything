---
name: product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones as krill entities (Product, FeatureSet, LoadBearingDecision, Milestone). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /krill-design:design <product-id> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned past ~20 FRs. Blocked today until #2926 closes — see "Known blocker" below.
---

# product

Forked from `tools/project-manager/skills/product`. The artifact is krill
entities (`Product`, `FeatureSet`, `LoadBearingDecision`, `Milestone` via a
`DesignSession`), not a GitHub Discussion + committed `PRODUCT.md` +
tracking issue — see `krill/plugin/shared/CONVENTIONS.md` for the
design-session mechanics.

## Known blocker

`create_product`/`create_feature_set`/`create_load_bearing_decision`/
`create_milestone` require `PersonaAgent`, which this plugin's ordinary
producer/architect subagent dispatch cannot obtain today — see
whale-net/everything#2926 for why and the two fix options. Steps 4 and 7
below will fail with `forbidden: persona "swarm_operator" may not call
create_product` (or equivalent) until that closes. **Do not fall back to
GitHub to route around it** — report the failure and stop.

## The artifact

One `Product` (name, vision); one `FeatureSet` per capability-map area (a
`LoadBearingDecision` attaches to the `FeatureSet` it constrains, never the
bare `Product`); one `Milestone` per roadmap entry. No `PRODUCT.md`, no
tracking issue, no ledger comments — a krill-hosted milestone's status lives
on the `Milestone` entity (`set_milestone_status`/
`get_milestone_status_history`), and the entities are the durable record the
moment they're written. Same hard rule as the original skill: capability
lines (`C7 — ...`), never a numbered FR, at this level.

## Steps

Follow `tools/project-manager/skills/product/SKILL.md`'s steps 1-9 (intake
questions, load-bearing-decision format, roadmap-entry format, human gate,
5-round reconciliation cap) with these substitutions:

- **No GitHub Discussion** — intake happens directly in this session, same
  as `krill-design:design` step 3. Once intake settles a name/vision,
  `create_product`, then `open_design_session {product_id,
  opening_submission}` for the rest (current-state, load-bearing, roadmap).
- **Producer/architect post `draft`/`reconciliation`/`signoff`
  revision events**, not Discussion comments — same convention as `design`.
- **Publish (step 7) writes the entities** — `create_feature_set` per
  capability area, `create_load_bearing_decision` per LB,
  `create_milestone` per roadmap entry. No PR, no tracking issue;
  `/krill-design:design <product-id> --milestone M1` reads them directly.
- **Amendment (step 8)** is further `append_revision_event` calls (or a new
  session) plus new/updated entities, never a hand edit.

## Downstream

```
/krill-design:design <product-id> --milestone M1
  → /krill-design:review → /krill-work:plan → /krill-work:implement → /krill-work:validate
  → repeat for M2, M3, ...
```

`<product-id>` is the krill `Product` surrogate id, not a GitHub issue
number.
