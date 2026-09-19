---
name: product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map in a GitHub Discussion, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones and publishes <domain>/PRODUCT.md plus a thin tracking Issue (product:approved). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /krill-design:design <product-issue> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned past ~20 FRs.
---

# product

Forked from `tools/project-manager/skills/product` — **mechanically
unchanged**. Product briefs (`<domain>/PRODUCT.md` + `<domain>/product/*.md`
+ a `product:approved` tracking issue) are committed markdown, not krill
entities — krill's `Product` entity today only carries a `name`/`vision`
string (`slice.Document.product`), not the full vision/personas/capability-
map/roadmap content this skill produces. Nothing about *how* this skill
works needed to change for this fork.

Read `tools/project-manager/skills/product/SKILL.md` for the full mechanics
(the artifact layout, the hard zero-FRs rule, load-bearing-decision format,
roadmap/ledger format, and all 9 steps) — this file only exists so
`krill-design` has its own dispatch targets and command names:

- Every `project-manager:producer`/`project-manager:architect` dispatch
  becomes `krill-design:producer`/`krill-design:architect`.
- Every `/project-manager:design` reference in the hand-off (step 9) and
  downstream diagram becomes `/krill-design:design`.
- Everything else — usage, parameters (`--milestones`, `--fr-budget`,
  `--resume-agents`), the artifact layout, steps 1-8 — is byte-for-byte the
  same procedure, just run by this plugin's own producer/architect personas.

## Downstream

```
/krill-design:design <product-issue> --milestone M1
  → /krill-design:review → /krill-work:plan → /krill-work:implement → /krill-work:validate
  → repeat for M2, M3, ...
```
