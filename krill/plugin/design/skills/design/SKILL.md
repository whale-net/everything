---
name: design
description: Design one feature's or one milestone's specification — interviews you for requirements/user stories, opens a krill DesignSession and drafts Requirements as revision events, then loops producer/architect until architect signs off, ready for /krill-design:review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before any krill Tasks exist — for task breakdown after signoff, use /krill-work:plan instead. Takes --milestone M<n> against a krill-hosted Product to spec exactly one milestone; if the request is a whole product rather than one feature, run /krill-design:product first instead of designing it all at once.
---

# design

Orchestrates the `krill-design` pipeline inside a DesignSession from a feature
idea to architect sign-off, optionally with a stakeholder meeting. It comes
before `/krill-design:review` (human approval) and `/krill-work:plan` (tasks);
it produces the spec, not the tasks. Mechanics: `krill/plugin/shared/CONVENTIONS.md`.

## Usage

```
/krill-design:design "short feature description"
/krill-design:design <design-session-id>            # resume an existing session
/krill-design:design <product-id> --milestone M2     # spec milestone M2 of a krill-hosted product
/krill-design:design                                 # no args — ask what the feature is
```

| Parameter | Default | Effect |
|---|---|---|
| `--milestone M<n>` | none | Scope to one milestone of a krill-hosted product (the positional argument is then the Product id). Producer specs only that outcome; architect also checks the draft against the milestone's `Must not foreclose` decisions. |
| `--stakeholder-meeting` | off | After architect sign-off, run `/krill-design:stakeholder-meeting`; blockers go back through the producer/architect loop before review. |
| `--stakeholder-rounds <n>` | `2` | Max meeting rounds before summarizing standing blockers. Implies `--stakeholder-meeting`. |
| `--personas "<a,b>"` | spec personas | Meet only these personas. Implies `--stakeholder-meeting`. |
| `--resume-agents` | off | Continue the same `producer-<id>`/`architect-<id>` subagents via `SendMessage` within this invocation; falls back to a fresh dispatch if the name isn't live. Never spans invocations. |

## Steps

0. **Check scope.** For a whole product/app/subsystem, stop and recommend
   `/krill-design:product`. A milepebble draft well past ~12 Requirements (a
   per-milepebble target, not a hard cap) should be split into milepebbles;
   milestones themselves have no FR cap.

1. **Survey related spec.** For a krill-hosted product, call
   `get_product_slice {id}` and `get_backlog {product_id}` and note existing
   Features/Requirements or backlog scope overlapping the request, before intake.

2. **Resolve the session.**
   - **`--milestone` first.** The positional argument is a Product id (`get_scope
     {}` then `list_products {scope_id}` finds it). Resolve `M<n>` via
     `list_product_delivery {product_id}`, then `get_milestone {id}` for its
     authoring fields, `Delivers`, `Must not foreclose` and deferrals (exact, not
     `get_product_slice`'s whole-product superset). If the product isn't
     krill-hosted or has no such milestone, stop with the "Milestone required"
     hard stop in CONVENTIONS.md and run `/krill-design:product` first. If
     `get_milestone_status {id}` is already `in design` or later, resume: the
     latest `in design` transition's note in `get_milestone_status_history {id}`
     names the design-session id (step 3).
   - Given a design-session id: `get_design_session` and `list_open_questions
     {blocking: true}`. Zero blocking questions after a `reconciliation` event
     means architect already signed off: go to step 7 if a stakeholder meeting
     was requested and none held, else point to `/krill-design:review <id>`.
   - Given a description (or nothing — ask): intake.

3. **Intake (new designs only).** `open_design_session {krill_session_id,
   product_id, opening_submission}`, where `opening_submission` is the request
   as given or (with `--milestone`) the milestone's `get_milestone` result
   verbatim. The session's event log is the durable record; no separate intake
   artifact. Interview conversationally in this session (don't delegate; it needs
   live back-and-forth) per `agents/producer.md` Mode 0, recording each round as
   a `draft` event. Open with overlapping entities if step 1 found any. For a
   milestone, first `set_milestone_status {milestone_id, status: "in design",
   note: "design session <design-session-id>"}`; the note is how a later run
   finds the session.

4. **Draft.** Dispatch `krill-design:producer` with `name:
   "producer-<design-session-id>"` (and `"architect-<design-session-id>"` for
   architect) so later rounds have a stable `--resume-agents` target. Pass the
   design-session id, not the interview transcript (producer reads the `draft`
   rounds itself; CONVENTIONS.md "Subagent dispatch: ids, not bodies"), and tell
   it to run Mode 1: a `draft` event plus `propose_entities`.

5. **Reconcile.** Dispatch `krill-design:architect` with the design-session id to
   run its Process, appending one `reconciliation` event (blocking questions, or
   none if clean). With `--milestone` it also runs its **Load-bearing check**.

6. **Loop to sign-off.** If architect opened blocking questions, dispatch producer
   (Mode 2, an `answer` event resolving them), then architect again. Repeat until
   `list_open_questions {blocking: true}` is empty, capped at 5 rounds (then
   summarize for the user). With `--resume-agents`, use `SendMessage` to the same
   names.

7. **Stakeholder meeting (only with `--stakeholder-meeting`).** After architect
   sign-off, invoke `/krill-design:stakeholder-meeting <design-session-id>`,
   passing `--personas` through. Cleared → step 8. Blocked → dispatch producer
   (Mode 2) with the session id and round number (it reads the open `SB-`
   questions itself), then architect for a fresh `reconciliation`, then hold the
   next round. Cap at `--stakeholder-rounds`; if blockers still stand, stop and
   summarize, unless inside `loop-design-panel`, whose `reviewer` takes over.

8. **Hand off.** Once signed off (and any meeting cleared), tell the user
   `/krill-design:review <design-session-id>` is next, or
   `/krill-design:loop-design-panel <design-session-id>` if no human reviewer is
   available. `review`'s `signoff` event makes the proposed entities the approved
   plan and moves a milestone through `designed` to `planned` (CONVENTIONS.md edge
   table).

   **With `--milestone`:** also report each milepebble's Requirement count
   against its `fr_budget` from `list_milepebbles` (default 12; CONVENTIONS.md "FR
   budget"). A milestone over 12 with no milepebbles cut needs a proposed split,
   not a scope cut.

   `/krill-work:plan` needs a Milestone: without `--milestone`, cut one first with
   `create_milestone` and `add_delivers` (CONVENTIONS.md "Milestone required").
