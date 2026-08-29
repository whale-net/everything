---
name: product
description: Scope a product before any feature spec exists — interviews you for vision, personas, and a capability map in a GitHub Discussion, has the architect record current state and the load-bearing decisions that later capabilities depend on, then breaks the product into milestones and publishes <domain>/PRODUCT.md plus a thin tracking Issue (product:approved). Run this first when a request is a whole product/app rather than one feature; each milestone is then specced with /project-manager:design <product-issue> --milestone M<n>. Also the right target for "scope this out", "what should v1 be", "break this into milestones", or when a design has ballooned past ~20 FRs.
---

# product

Produces the **product brief** — the document that sits one level above a plan and exists to make each plan small. It answers *what is this product, for whom, what must be true across all of it, and in what order do we build it*, and deliberately does **not** answer *what does the system do in detail* — that is `/project-manager:design`, once per milestone.

This skill exists because a single `design` pass over a whole product produces 60-80 FRs, which is both context-hostile and unsafe to implement in one shot. The fix is not to write fewer requirements — it is to have a durable higher-level document that lets each milestone's spec be small *without* being short-sighted.

Comes before `/project-manager:design`. See `tools/project-manager/CONVENTIONS.md` § Product brief & milestones.

## Usage

```
/project-manager:product "short product description"
/project-manager:product <discussion-url>       # resume an in-flight product discussion
/project-manager:product <issue-number>         # amend an existing product:approved brief
/project-manager:product                        # no args — ask what the product is
```

### Parameters

| Parameter | Default | Effect |
|---|---|---|
| `--milestones <n>` | `3` | Target number of milestones in the roadmap. A guide for the producer, not a hard cap — but a roadmap over ~6 milestones usually means the capability map is really two products. |
| `--fr-budget <n>` | `12` | Default per-milestone FR backstop written into each milestone. Enforced later by architect during `/project-manager:design --milestone`. |
| `--agent-sync` | off | Run steps 5-6's architect pass and roadmap loop as one long-lived producer+architect session pair over `agentsync-mcp` instead of a fresh subagent dispatch every round. See CONVENTIONS.md § Agent-sync mode. |

## When *not* to use it

A single feature added to an existing system goes straight to `/project-manager:design`. Use this skill when the request is a product or subsystem that does not exist yet, when the answer to "what's in v1" is genuinely unsettled, or when a `design` pass has already ballooned past roughly 20 FRs — in that last case, the ballooned draft is good raw material: feed it in as the description. The same can happen to a single *milestone's* design (`design --milestone` step 0 flags it); since a product maps 1:1 to a domain, running this skill against a re-ballooned milestone only cleanly applies when it genuinely spans a new domain-sized subsystem — otherwise prefer splitting the existing roadmap into an extra milestone instead (CONVENTIONS.md § When a milestone re-balloons).

## The artifact

Two artifacts, deliberately split:

- **The spec** — `<domain>/PRODUCT.md` plus `<domain>/product/*.md`, committed to `main` via a small doc-only PR. A product maps 1:1 to a top-level domain (`AGENTS.md` § Domains); publishing creates the domain directory if it's new. This is the document a future agent reads to understand why the system is shaped the way it is — same reason `ARCHITECTURE.md` lives in the domain instead of in an issue.
- **The tracking issue** — titled `Product: <name>`, labeled `product:approved`. Body is just a pointer: `Product brief: <domain>/PRODUCT.md (#<pr-number>)` and `Product discussion: <discussion-url>`. Its comments are the live roadmap ledger (see § Roadmap below) — the body itself is never rewritten after creation except by an amendment PR reference.

`PRODUCT.md` is an **index**, not a monolith — this is the large-document split guidance in `AGENTS.md` § Size Limits & Splitting applied from day one rather than after the fact, because the two sections most likely to grow unbounded (the capability map, which only ever adds to `Later`; the roadmap, which keeps every milestone forever, shipped ones included) are exactly the sections this whole skill exists to keep small for everyone else:

```
<domain>/PRODUCT.md                    — index: Vision, Personas, Load-bearing
                                          decisions, Non-goals inline + a jump table
<domain>/product/01-current-state.md   — "# Current state"
<domain>/product/02-capability-map.md  — "# Capability map"
<domain>/product/03-roadmap.md         — "# Roadmap" (milestone definitions only —
                                          no ledger, that lives on the tracking issue)
```

The four sections that stay inline in the index are short and either stable (Vision, Personas, Non-goals) or cross-referenced by number from everywhere (`LB1..LBn`, cited by every milestone's `Must not foreclose` and by architect's Load-bearing check) — exactly the carve-out `AGENTS.md`'s splitting rule allows instead of forcing a one-line file. The three that split out have no natural ceiling. See `tools/project-manager/CONVENTIONS.md` § Layout for the full rationale, including when `product/03-roadmap.md` itself eventually needs the current/history split `AGENTS.md` prescribes for a `PLAN.md`.

### Hard rule: the brief contains zero numbered FRs or NFRs

A capability is `C7 — Operators can see per-device sensor health at a glance`. It is **not** `FR7 — The health endpoint returns 200 with a JSON body containing lastSeenAt`. If you are writing a testable behavior statement, you are in the wrong document — that belongs in a milestone's spec, written by `/project-manager:design`. A brief that acquires FRs has just moved the 80-FR problem up one layer instead of solving it.

Capability lines are the unit of traceability: milestones deliver capabilities, and each milestone's FRs each cite the capability they serve.

### Load-bearing decisions

The section that earns the document. Each entry is a structural commitment an early milestone must get approximately right because a later capability depends on it. Three clauses, always:

```
LB3 — Multi-tenancy boundary
  At risk: C12 (org-scoped dashboards), C15 (per-org API keys) — both `Later`.
  Decide now: every row in the reading/device tables carries `org_id` from M1, even
  though M1 only ever has one org and no UI exposes it.
  Stays cheap: the org *selector*, org CRUD, and all authorization logic — adding
  those later touches handlers and templates, not a backfill of every table.
```

The third clause is what keeps the list honest and short. If nothing about a decision is expensive to reverse, it is not load-bearing and does not belong here — put it in the milestone's spec instead. Aim for 3-8 entries; a list of twenty is a design document wearing a disguise.

Architect owns this section. It is the product-level output that later prevents the trap the user actually fears: rewriting everything to fit in something that was always coming.

### Roadmap

Each milestone is defined by **one user-visible outcome** — a sentence naming who can now do what. "Auth and a plant detail page" is a milestone; "the data layer" is not, because nobody can do anything with it.

```
### M2 — A logged-in grower can see one plant's live readings

Delivers: C3, C4, C6
Must not foreclose: LB1, LB3
Deliberately deferred: multi-plant list (C7 → M3), alerting (C11 → Later)
FR budget: 12
```

`Must not foreclose` is the list architect checks the milestone's draft spec against. `FR budget` is a backstop, not the control — the real constraint is that every FR must trace to a capability this milestone delivers.

**The ledger lives on the tracking issue, not the spec file.** Status changes on every milestone transition, and a file edit would mean a PR for every status flip — worse, two milestones can be in flight at once (`implement` already parallelizes task work), so a shared body table would race. Instead every transition is a standalone comment on the tracking issue:

```
Ledger: M<n> → <status> (<link-or-none>)
```

Readers (`design --milestone`'s idempotency check, `/project-manager:status`, architect) reconstruct current status per milestone from the **last** such comment; no comment means `not started`. Status moves `not started → in design → planned → in progress → shipped` — see CONVENTIONS.md § Roadmap ledger for exactly who posts each transition.

## Steps

1. **Resolve the target.**
   - Given a discussion URL: read its latest comments and resume at whichever step is unfinished.
   - Given an issue number: confirm it is labeled `product:approved` and jump to step 8 (amendment).
   - Given a description (or nothing — ask): proceed to intake.

2. **Open the product discussion.**
   ```sh
   gh discussion create --title "Product: <name>" --category "Ideas" --body-file <tmpfile>
   ```
   Opening body is the request as given, plus the first round of questions.

3. **Product intake.** Conduct this conversationally in this session — it needs live back-and-forth, so do not delegate it. Follow `tools/project-manager/agents/producer.md` Mode P0. Ask about the product's users and the job each is hiring it for, what the end state looks like a year out, what already exists, what is explicitly never in scope, and — critically — **what the smallest version is that is genuinely useful to somebody**. Post each round as a discussion comment.

   Push back on scope here, not later. When the requester describes the end state, the useful follow-up is "what would you cut to have this working next week?" — the answer is usually M1.

4. **Draft the brief.** Dispatch `project-manager:producer` (Mode P1) with the intake transcript and discussion URL: post the draft brief — vision, personas, capability map, non-goals — as a discussion comment. No current state, no load-bearing decisions, no roadmap yet; those need architect first.

5. **Architect current-state pass and load-bearing decisions.** *(Default only — see the `--agent-sync` variant below, which folds this and step 6 into one dispatch.)* Dispatch `project-manager:architect` with the discussion URL, instructing it to run its **Product mode**: survey what already exists in the affected domains, post the **Current state** section, then derive **Load-bearing decisions** from the capability map's `Next`/`Later` buckets, plus open questions and nitpicks on the draft.

   Do not skip this even when the answer is "nothing exists yet" — in this repo "nothing" frequently means "a half-built or recently reverted thing", and a milestone specced from an imaginary zero is a milestone that gets re-specced.

6. **Roadmap and loop.**
   - **Default:** Dispatch `project-manager:producer` (Mode P2) to answer architect's questions, fold in the current-state and load-bearing sections, and add the **Roadmap** — milestones ordered so each one is independently useful, each with its outcome, capabilities, `Must not foreclose` refs, deferrals, and FR budget. Then re-dispatch `project-manager:architect` to reconcile. Repeat until architect posts `Architect sign-off: approved`, capping at 5 rounds and summarizing for the user if stuck.
   - **With `--agent-sync`:** call `mcp__agentsync-mcp__start_session("product-<discussion-number>")`, then dispatch `project-manager:producer` **and** `project-manager:architect` together in one message (two parallel `Agent` calls) — architect with the discussion URL and told to start with its Product mode current-state/load-bearing pass, producer with the discussion URL and told it will receive that pass via `sync()` before running Mode P2 — both told the session id and to run in agent-sync mode (producer.md / architect.md § Agent-sync mode; CONVENTIONS.md § Agent-sync mode) through sign-off. Skip straight to step 7 once both return.

   Architect specifically checks: does M1 deliver something a person can use? Does any milestone's outcome sentence name a component rather than a user? Does every `Later` capability have either an `LB` entry protecting it or an explicit note that it is cheap to add?

7. **Human gate and publish.** Present the signed-off brief to the user: the vision, the capability map bucketed, the load-bearing decisions, and the milestone list with what M1 does and does not include. Ask for approval, changes, or a re-cut of the milestone boundaries.
   - **Changes** — dispatch producer (Mode P2) and architect for another round in the discussion, then return here.
   - **Approved** — dispatch `project-manager:producer` (Mode P3) to publish. It writes the index (`<domain>/PRODUCT.md`) and its three split files (`<domain>/product/01-current-state.md`, `02-capability-map.md`, `03-roadmap.md` — § The artifact above), commits them together to a branch, and opens a doc-only PR (`gh pr create`), then creates the tracking issue whose body just points at the index and the discussion:
     ```sh
     gh issue create --title "Product: <name>" --label "product:approved" --body-file <tmpfile>
     ```
     Producer then comments `Approved product brief: <issue-url>` on the discussion. Tell the user the docs PR needs to merge before any milestone can be designed — `design --milestone` reads the spec from `main` and stops with a pointer to the PR if it isn't there yet.

     This skill runs its own gate rather than routing through `/project-manager:review`, which is specifically the gate for a milestone's root plan issue.

8. **Amendment (existing brief).** Reality changes roadmaps — a shipped milestone teaches you something, or a milestone's design surfaces a capability nobody had thought of. The spec is a living document, but never edited silently:
   - Dispatch producer (Mode P2) to draft the amendment as a comment on the **tracking issue** — not the original product discussion, which is closed once the brief is published — and architect to reconcile it there if it touches load-bearing decisions or milestone ordering, baselining against the committed `<domain>/product/01-current-state.md` on `main`.
   - Present the diff to the user for approval.
   - On approval, producer repeats the publish mechanics but touches only the file(s) the amendment actually changes (`PRODUCT.md` for a load-bearing/non-goal change, `product/02-capability-map.md` for a new capability, `product/03-roadmap.md` for a re-cut milestone) — a fresh branch, an edit, a PR — never `gh issue edit` on the tracking issue's body. Once the PR merges, it comments `Amended: <summary> (#<pr-number>)` on the tracking issue.
   - An amendment never rewrites the history of a shipped milestone. Ship what shipped; change what is ahead — the files' git history is the audit trail. If `product/03-roadmap.md` alone has grown past a one-pass read, this is also the moment to split it into `product/03-roadmap-HISTORY.md` (shipped milestones) and a live file holding only what's still moving (CONVENTIONS.md § Layout).

9. **Hand off.** Tell the user the brief is published (and its docs PR merged) and that `/project-manager:design <product-issue> --milestone M1` is the next step, and name what M1 contains.

## Downstream

Each milestone runs the existing pipeline, with only two conditional additions (`plan` and `validate` each post one ledger comment when the root plan names a product brief — CONVENTIONS.md § Roadmap ledger):

```
/project-manager:design <product-issue> --milestone M1
  → /project-manager:review → /project-manager:plan → /project-manager:implement → /project-manager:validate
  → repeat for M2, M3, ...
```

`<domain>/PRODUCT.md` and its `product/*.md` files are read fresh from `main` at the start of every milestone's design, which is what keeps milestone N+1 aware of decisions made in milestone N without re-reading milestone N's spec.
