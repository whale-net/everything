---
name: krill-design
description: Design one feature's or one milestone's specification — interviews you for requirements/user stories, opens a krill DesignSession and drafts Requirements as revision events, then loops producer/architect until architect signs off, ready for /krill-review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before any krill Tasks exist — for task breakdown after signoff, use /krill-plan instead. Takes --milestone M<n> against a krill-hosted Product to spec exactly one milestone; if the request is a whole product rather than one feature, run /krill-product first instead of designing it all at once.
---

# krill-design

Orchestrates the krill design pipeline inside a `DesignSession` from a feature
idea to architect sign-off, optionally with a stakeholder meeting. It comes
before `/krill-review` (human approval) and `/krill-plan` (tasks); it produces
the spec, not the tasks. Load `references/CONVENTIONS.md`,
`references/agents/producer.md`, and `references/agents/architect.md` via
`get_skill_resources`.

## Usage

```
/krill-design "short feature description"
/krill-design <design-session-id>            # resume an existing session
/krill-design <product-id> --milestone M2     # spec milestone M2 of a krill-hosted product
/krill-design                                 # no args — ask what the feature is
```

| Parameter | Default | Effect |
|---|---|---|
| `--milestone M<n>` | none | Scope to one milestone of a krill-hosted product (the positional argument is then the Product id). Producer specs only that outcome; architect also checks the draft against the milestone's `Must not foreclose` decisions. |
| `--stakeholder-meeting` | off | After architect sign-off, run `/krill-stakeholder-meeting`; blockers go back through the producer/architect loop before review. |
| `--stakeholder-rounds <n>` | `2` | Max meeting rounds before summarizing standing blockers. Implies `--stakeholder-meeting`. |
| `--personas "<a,b>"` | spec personas | Meet only these personas. Implies `--stakeholder-meeting`. |
| `--resume-agents` | off | Continue the same `krill-producer`/`krill-architect` subagent `conversationId`s via `send_message` within this invocation; falls back to a fresh dispatch if not live. |
| `--with-strong-models` | off | Noted for stakeholder meeting step if run. |

## Subagent setup

Before dispatching `krill-producer` or `krill-architect`, call `get_skill_resources`
for `["references/CONVENTIONS.md", "references/agents/producer.md", "references/agents/architect.md"]`
and register them once per conversation with `define_subagent`:
- `krill-producer`: `system_prompt` from `references/agents/producer.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-architect`: `system_prompt` from `references/agents/architect.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.

Invoke them with `invoke_subagent` (`Model: "inherit"`, `Workspace: "inherit"`).

## Steps

0. **Check scope.** For a whole product/app/subsystem, stop and recommend
   `/krill-product`. A milepebble draft well past ~12 Requirements (a
   per-milepebble target, not a hard cap) should be split into milepebbles;
   milestones themselves have no FR cap.

1. **Survey related spec.** For a krill-hosted product, call
   `get_product_slice {id}` and `get_backlog {product_id}` and note existing
   Features/Requirements or backlog scope overlapping the request, before intake.

2. **Resolve the session.**
   - **`--milestone` first.** The positional argument is a Product id (`get_scope
     {}` then `list_products {scope_id}` finds it). Resolve `M<n>` via
     `list_product_delivery {product_id}`, then `get_milestone {id}` for its
     authoring fields, `Delivers`, `Must not foreclose` and deferrals. If the
     product isn't krill-hosted or has no such milestone, stop with the
     "Milestone required" hard stop in `CONVENTIONS.md` and run `/krill-product`
     first. Then run `CONVENTIONS.md` "Milestone given: check its milepebbles
     first": spec only scope no milepebble already delivers, resume an existing
     milepebble's design session rather than opening a new one, and stop if
     every milepebble is covered. If `get_milestone_status {id}` is already
     `in design` or later, resume: the latest `in design` transition's note in
     `get_milestone_status_history {id}` names the design-session id (step 3).
   - Given a design-session id: `get_design_session` and `list_open_questions
     {blocking: true}`. Zero blocking questions after a `reconciliation` event
     means architect already signed off: go to step 7 if a stakeholder meeting
     was requested and none held, else point to `/krill-review <id>`.
   - Given a description (or nothing — ask): intake.

3. **Intake (new designs only).** `open_design_session {krill_session_id,
   product_id, opening_submission}`, where `opening_submission` is the request
   as given or (with `--milestone`) the milestone's `get_milestone` result
   verbatim. Interview conversationally in this session (don't delegate; it needs
   live back-and-forth) per `references/agents/producer.md` Mode 0, recording
   each round as a `draft` event. Open with overlapping entities if step 1 found
   any. For a milestone, first `set_milestone_status {milestone_id, status:
   "in design", note: "design session <design-session-id>"}`.

4. **Draft.** Dispatch `krill-producer` with `<krill-session-id>` and
   `<design-session-id>` (save its `conversationId` for `--resume-agents`).
   Remind it that Requirement bodies stay tight (`CONVENTIONS.md` "Requirement
   text: keep it tight"). Pass the design-session id, not the interview
   transcript, and tell it to run Mode 1: a `draft` event plus `propose_entities`.

5. **Reconcile.** Dispatch `krill-architect` with `<krill-session-id>` and
   `<design-session-id>` to run its Process, appending one `reconciliation`
   event (blocking questions, or none if clean). With `--milestone` it also runs
   its **Load-bearing check**.

6. **Loop to sign-off.** If architect opened blocking questions, dispatch
   `krill-producer` (Mode 2, an `answer` event resolving them), then
   `krill-architect` again. Repeat until `list_open_questions {blocking: true}`
   is empty, capped at 5 rounds (then summarize for the user). With
   `--resume-agents`, use `send_message` to the same `conversationId`s.

7. **Stakeholder meeting (only with `--stakeholder-meeting`).** After architect
   sign-off, follow `/krill-stakeholder-meeting` (call `lookup_skill` on
   `krill-stakeholder-meeting`), passing `<design-session-id>` and `--personas`.
   Cleared → step 8. Blocked → dispatch `krill-producer` (Mode 2) with the
   session id and round number, then `krill-architect` for a fresh
   `reconciliation`, then hold the next round. Cap at `--stakeholder-rounds`;
   if blockers still stand, stop and summarize, unless inside
   `krill-loop-design-panel`, whose `krill-reviewer` takes over.

8. **Hand off.** Once signed off (and any meeting cleared), tell the user
   `/krill-review <design-session-id>` is next, or
   `/krill-loop-design-panel <design-session-id>` if no human reviewer is
   available.

   **With `--milestone`:** also report each milepebble's Requirement count
   against its `fr_budget` from `list_milepebbles` (default 12). A milestone
   over 12 with no milepebbles cut needs a proposed split, not a scope cut.

   `/krill-plan` needs a Milestone: without `--milestone`, cut one first with
   `create_milestone` and `add_delivers` (`CONVENTIONS.md` "Milestone required").
