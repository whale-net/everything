---
name: design
description: Design one feature's or one milestone's specification — interviews you for requirements/user stories, opens a krill DesignSession and drafts Requirements as revision events, then loops producer/architect until architect signs off, ready for /krill-design:review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before a GitHub Project board or task issues exist — for task breakdown after signoff, use /krill-work:plan instead. Takes --milestone M<n> against a product:approved brief to spec exactly one milestone; if the request is a whole product rather than one feature, run /krill-design:product first instead of designing it all at once.
---

# design

Orchestrates the `krill-design` pipeline inside a krill DesignSession from a
feature idea up to architect sign-off, optionally including a stakeholder
meeting round. Comes before `/krill-design:review` (human approval) and
`/krill-work:plan` (task breakdown) — this skill produces the spec, not the
Project board. See `krill/plugin/shared/CONVENTIONS.md` for the
design-session mechanics.

## Usage

```
/krill-design:design "short feature description"
/krill-design:design <design-session-id>            # resume an existing session
/krill-design:design 42 --milestone M2               # spec milestone M2 of product brief issue #42
/krill-design:design                                 # no args — ask what the feature is
```

### Parameters

`--milestone M<n>`, `--stakeholder-meeting`, `--stakeholder-rounds <n>`
(default 2), `--personas "<a,b>"`, `--resume-agents` — see
`tools/project-manager`'s `design` skill for the full effect of each.

## Steps

0. **Check the scope of the request.** Stop and recommend
   `/krill-design:product` for a whole product/app/subsystem; flag a
   milestone draft heading past ~20 Requirements the same way.

1. **Survey related issues.** `gh issue list` for overlapping
   `idea`/`source:scope-note` issues before intake. This stays on GitHub;
   krill has no issue-tracking entity of its own yet.

2. **Resolve the design session.**
   - **Check `--milestone` first** — the positional argument is a product
     issue number. `gh issue view <n>` and confirm `product:approved`; read
     `<domain>/PRODUCT.md` → `product/03-roadmap.md` for the milestone entry
     — **except for a product hosted in krill** (krill's own domain, or one
     imported via `krill/importer`), where a real krill Milestone entity
     exists: call `get_milestone {id}` for its exact authoring
     fields/`Delivers`/`Must not foreclose`/deferrals (an exact per-milestone
     read, not `get_product_slice`'s whole-product superset). Take the last
     `Ledger: M<n> → <status> (<design-session-id>)` tracking-issue comment
     for a non-krill-hosted product, or `get_milestone_status {id}` for a
     krill-hosted one — already `in design` or later means resume that
     session id instead of opening a new one.
   - If given a design-session id, call `get_design_session` and
     `list_open_questions {blocking: true}`. Zero blocking questions after a
     `reconciliation` event means architect has already signed off — skip to
     step 7 if a stakeholder meeting was requested and none has been held,
     otherwise stop and point the user to `/krill-design:review <id>`.
   - If given a description (or nothing — ask for one), proceed to intake.

3. **Intake (new designs only).** Call `open_design_session {krill_session_id,
   product_id, opening_submission}` — `opening_submission` is the request as
   given, or (with `--milestone`) the milestone's roadmap entry / live
   `get_product_slice` result quoted verbatim (same krill-domain carve-out as
   step 2). There is no separate "intake discussion" artifact to create —
   the DesignSession's own event log is the durable record. Conduct the
   interview conversationally directly in this session (do not delegate —
   it needs live back-and-forth), following `agents/producer.md` Mode 0 —
   including recording each interview round as a `draft` revision event, so
   the interview lives on the session rather than only in this context.
   If step 1 turned up real overlap, open with those issue numbers. If this
   is a milestone, post `gh issue comment <product-issue> --body "Ledger:
   M<n> → in design (<design-session-id>)"` before interviewing — or, for a
   krill-hosted milestone, call `set_milestone_status {milestone_id,
   status: "in design"}` instead (CONVENTIONS.md).

4. **Draft the specification.** Dispatch with an explicit `name: "producer-
   <design-session-id>"` (and `name: "architect-<design-session-id>"` for
   architect) so a later round has a stable target under `--resume-agents`.
   Dispatch `krill-design:producer` with the design-session id (not the
   interview transcript — producer reads the recorded `draft` rounds via
   `get_design_session`; CONVENTIONS.md "Subagent dispatch: ids, not
   bodies"), instructing it to run Mode 1: append a `draft` revision event
   and `propose_entities` for the Requirements gathered.

5. **Reconcile.** Dispatch `krill-design:architect` with the design-session
   id, instructing it to run its Process: reconcile against repo
   conventions, appending one `reconciliation` event (with open blocking
   questions, or none if clean).

   **With `--milestone`:** architect also runs its **Load-bearing check**.

6. **Loop until architect sign-off.**
   - If architect's `reconciliation` event opened blocking questions:
     dispatch `krill-design:producer` to run Mode 2 (append an `answer`
     event resolving them), then dispatch `krill-design:architect` again.
   - Repeat until architect's `reconciliation` event has zero open blocking
     questions (check via `list_open_questions {blocking: true}`), or cap at
     5 rounds and summarize for the user if stuck.
   - **With `--resume-agents`:** target the same `producer-<id>`/
     `architect-<id>` names via `SendMessage`.

7. **Stakeholder meeting (only with `--stakeholder-meeting`).** Once
   architect has signed off, invoke `/krill-design:stakeholder-meeting
   <design-session-id>` — passing `--personas` through. Cleared → step 8.
   Blocked → dispatch `krill-design:producer` (Mode 2) with the
   design-session id and meeting discussion URL (not the blocker text) to
   answer the consolidated blockers via an `answer` event, dispatch
   `krill-design:architect` for a fresh `reconciliation`, hold the next
   round. Cap at `--stakeholder-rounds`; if blockers still stand, stop and
   summarize — unless running inside `loop-design-panel`, which takes over
   with its `reviewer` subagent instead.

8. **Hand off.** Once signed off (and the stakeholder meeting cleared, if
   held), tell the user `/krill-design:review <design-session-id>` is next.
   If no human reviewer is available, `/krill-design:loop-design-panel
   <design-session-id>` runs the same pipeline unattended.

   **With `--milestone`:** also report the Requirement count against the
   FR budget. For a krill-hosted milestone the budget is per milepebble
   (CONVENTIONS.md "FR budget"): report each milepebble's count against
   its `fr_budget` from `list_milepebbles` (default 12); a milestone over
   12 with no milepebbles cut yet needs a proposed milepebble split, not a
   scope cut. Otherwise use the roadmap file's `FR budget` line. `review`
   appends the `signoff` event that makes the proposed entities the
   approved plan as usual; producer posts `Ledger: M<n> → planned
   (<design-session-id>)` on the tracking issue, or calls
   `set_milestone_status {milestone_id, status: "planned"}` for a
   krill-hosted milestone.
