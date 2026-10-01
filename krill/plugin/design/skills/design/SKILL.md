---
name: design
description: Design one feature's or one milestone's specification — interviews you for requirements/user stories, opens a krill DesignSession and drafts Requirements as revision events, then loops producer/architect until architect signs off, ready for /krill-design:review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before any krill Tasks exist — for task breakdown after signoff, use /krill-work:plan instead. Takes --milestone M<n> against a krill-hosted Product to spec exactly one milestone; if the request is a whole product rather than one feature, run /krill-design:product first instead of designing it all at once.
---

# design

Orchestrates the `krill-design` pipeline inside a krill DesignSession from a
feature idea up to architect sign-off, optionally including a stakeholder
meeting round. Comes before `/krill-design:review` (human approval) and
`/krill-work:plan` (task breakdown) — this skill produces the spec, not the
tasks. See `krill/plugin/shared/CONVENTIONS.md` for the
design-session mechanics.

## Usage

```
/krill-design:design "short feature description"
/krill-design:design <design-session-id>            # resume an existing session
/krill-design:design <product-id> --milestone M2     # spec milestone M2 of a krill-hosted product
/krill-design:design                                 # no args — ask what the feature is
```

### Parameters

| Parameter | Default | Effect |
|---|---|---|
| `--milestone M<n>` | none | Scope this design to one milestone of a krill-hosted product (the positional argument is then the krill Product id). Producer specs only that milestone's outcome; architect also checks the draft against the milestone's `Must not foreclose` decisions. |
| `--stakeholder-meeting` | off | After architect sign-off, run `/krill-design:stakeholder-meeting`: every persona in the spec gives a round of feedback, and any blocker goes back through the producer/architect loop before hand-off to review. |
| `--stakeholder-rounds <n>` | `2` | Max stakeholder meeting rounds before stopping and summarizing standing blockers for the user. Implies `--stakeholder-meeting`. |
| `--personas "<a,b>"` | spec personas | Meet with only these personas. Implies `--stakeholder-meeting`. |
| `--resume-agents` | off | Continue the same producer/architect subagents via `SendMessage` (targeting `producer-<id>`/`architect-<id>`) for follow-up rounds within this invocation, instead of spawning fresh ones; falls back to a fresh dispatch if the name isn't a live agent. Never reaches across separate skill invocations. |

## Steps

0. **Check the scope of the request.** Stop and recommend
   `/krill-design:product` for a whole product/app/subsystem; flag a
   milepebble draft heading well past ~12 Requirements (the per-milepebble
   target, not a hard cap) by suggesting a split into milepebbles. Milestones
   themselves have no FR cap.

1. **Survey related spec.** For a krill-hosted product, call
   `get_product_slice {id}` and `get_backlog {product_id}` and note any
   existing Feature/Requirement or backlog scope that overlaps the request,
   before intake.

2. **Resolve the design session.**
   - **Check `--milestone` first** — the positional argument is a krill
     Product id (`get_scope {}` then `list_products {scope_id}` finds it).
     Resolve `M<n>` to a Milestone id with `list_product_delivery
     {product_id}`, then call `get_milestone {id}` for its exact authoring
     fields/`Delivers`/`Must not foreclose`/deferrals (an exact per-milestone
     read, not `get_product_slice`'s whole-product superset). **If the
     product isn't hosted in krill or has no such milestone, stop** and
     report the "Milestone required" hard stop in CONVENTIONS.md — run
     `/krill-design:product` first. `get_milestone_status {id}` already
     `in design` or later means resume: the latest `in design` transition's
     note in `get_milestone_status_history {id}` names the design-session id
     (step 3) — resume that session instead of opening a new one.
   - If given a design-session id, call `get_design_session` and
     `list_open_questions {blocking: true}`. Zero blocking questions after a
     `reconciliation` event means architect has already signed off — skip to
     step 7 if a stakeholder meeting was requested and none has been held,
     otherwise stop and point the user to `/krill-design:review <id>`.
   - If given a description (or nothing — ask for one), proceed to intake.

3. **Intake (new designs only).** Call `open_design_session {krill_session_id,
   product_id, opening_submission}` — `opening_submission` is the request as
   given, or (with `--milestone`) the milestone's live `get_milestone`
   result quoted verbatim. There is no separate "intake discussion"
   artifact to create — the DesignSession's own event log is the durable
   record. Conduct the
   interview conversationally directly in this session (do not delegate —
   it needs live back-and-forth), following `agents/producer.md` Mode 0 —
   including recording each interview round as a `draft` revision event, so
   the interview lives on the session rather than only in this context.
   If step 1 turned up real overlap, open with those entities. If this is a
   milestone, call `set_milestone_status {milestone_id, status: "in design",
   note: "design session <design-session-id>"}` before interviewing
   (CONVENTIONS.md) — the note is how a later run finds the session.

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
   design-session id and round number (the blockers are open `SB-` questions
   it reads itself) to answer them via an `answer` event, dispatch
   `krill-design:architect` for a fresh `reconciliation`, hold the next
   round. Cap at `--stakeholder-rounds`; if blockers still stand, stop and
   summarize — unless running inside `loop-design-panel`, which takes over
   with its `reviewer` subagent instead.

8. **Hand off.** Once signed off (and the stakeholder meeting cleared, if
   held), tell the user `/krill-design:review <design-session-id>` is next.
   If no human reviewer is available, `/krill-design:loop-design-panel
   <design-session-id>` runs the same pipeline unattended.

   **With `--milestone`:** also report the Requirement count against the
   FR budget, which is per milepebble (CONVENTIONS.md "FR budget"): report
   each milepebble's count against its `fr_budget` from `list_milepebbles`
   (default 12); a milestone over 12 with no milepebbles cut yet needs a
   proposed milepebble split, not a scope cut. `review` appends the
   `signoff` event that makes the proposed entities the approved plan and
   moves the milestone through `designed` to `planned` (CONVENTIONS.md edge
   table).

   `/krill-work:plan` needs a Milestone: if this design wasn't scoped to one
   (no `--milestone`), cut one first with `create_milestone` and
   `add_delivers` (CONVENTIONS.md "Milestone required").
