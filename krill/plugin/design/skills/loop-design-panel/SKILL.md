---
name: loop-design-panel
description: Drives a feature (or milestone) through the full krill-design pipeline unattended — dispatches /krill-design:design with --stakeholder-meeting, then a reviewer subagent that stands in for whatever would otherwise need a human, until a signoff revision event with signoff_status approved lands. Same agent architecture as /krill-work:loop-plan-implement-validate: every phase runs in its own fresh subagent so this session's context never absorbs revision-event/gh traffic. Use for "design this without me", "let the panel settle this", "auto-approve this design", or when no human reviewer is available right now.
---

# loop-design-panel

Chains `/krill-design:design --stakeholder-meeting` with a `reviewer`
subagent that stands in for the human at every point the pipeline would
otherwise stop and ask one, until a `signoff` revision event lands.

**No `plan:agent-approved` label exists here, and none is needed.** Every
`RevisionEvent`'s `acting`/`on_behalf_of` `Subject` records whether the
caller was a human (`kind: human`, via mcpauth) or an agent (`kind:
service`, via whagent-net) — provenance is structural. A `signoff` event
appended by this skill's `reviewer` dispatch is indistinguishable in
*mechanics* from a human's, but its `acting.kind` on the stored event tells
you it wasn't one — `status` and `get_design_session` both surface this.

## Usage

```
/krill-design:loop-design-panel "short feature description"
/krill-design:loop-design-panel <design-session-id>
/krill-design:loop-design-panel <product-issue> --milestone M2
/krill-design:loop-design-panel <design-session-id> --personas "Operator,Release engineer" --max-panel-rounds 2
```

Parameters (`--milestone`, `--personas`, `--stakeholder-rounds`,
`--max-panel-rounds`, default 3) — see `tools/project-manager`'s
`loop-design-panel` skill for the exact forwarding/capping semantics.

## Steps

1. **Resolve the starting point.**
   - Given a design-session id: `get_design_session {id}`. If the last event
     is already `signoff` with `approved`, stop and point the user to
     `/krill-work:plan <feature-set-id>` — there's nothing left to do.
   - Given a bare description or nothing: proceed straight to step 2.

2. **Design phase (subagent).** Dispatch a fresh `general-purpose` subagent:
   invoke `Skill` with `skill: "krill-design:design"`, forwarding the target,
   `--milestone`, `--stakeholder-meeting` (always included), and
   `--stakeholder-rounds`/`--personas` if given. Add the same unattended-run
   instruction project-manager's version does: apply `agents/producer.md`
   Mode 0's "thinner input, note the assumptions" allowance immediately
   instead of pausing for live back-and-forth. Let it run to completion,
   then report back *only*:
   - the design-session id,
   - one of: **ready for hand-off**, **blocked: stakeholder disagreement**
     (meeting discussion URL + round number + standing blockers), or
     **blocked: architect stalled**,
   - and, if `--milestone` was given, the product issue number.

3. **Route on the design phase's report.**
   - **Ready for hand-off** → step 5.
   - **Blocked: architect stalled** → stop, report to the user — a real spec
     conflict, not something a `reviewer` ruling can settle.
   - **Blocked: stakeholder disagreement** → step 4.

4. **Panel round.** Track a panel-round counter, starting at 1.
   a. Dispatch `krill-design:reviewer` (model `opus`) with Mode: Ruling — the
      design-session id, the meeting discussion URL, the round number. It
      appends a `ruling` revision event and returns the sustained/overruled
      counts and per-blocker reasoning.
   b. Dispatch `krill-design:producer` (Mode 2) with the design-session id
      and the ruling's reasoning, instructing it to fold in every
      **sustained** item via an `answer` event (and a follow-up
      `propose_entities` call if a Requirement needs to change) and record
      every **overruled** item's rationale in the same event's notes.
   c. Dispatch `krill-design:architect` for a fresh `reconciliation`. Loop
      producer↔architect exactly as `design` steps 5-6 do until sign-off
      (cap 5 rounds).
   d. Once clear, invoke `/krill-design:stakeholder-meeting
      <design-session-id>` directly for the next round.
   e. **Cleared** → step 5. **Blocked again** → increment the panel-round
      counter; at `--max-panel-rounds`, stop and report the standing
      disagreement (including `reviewer`'s prior rulings) to the user.
      Otherwise repeat from 4a.

5. **Agent review (subagent).** Dispatch `krill-design:reviewer` (model
   `opus`) with Mode: Agent review — the design-session id. It appends a
   `signoff` revision event (`approved` or `changes_requested`) with its
   reasoning in the dispatch response.

6. **Route on the review decision.** Track a separate review-round counter.
   - **Approved** → step 7.
   - **Changes requested** → dispatch `krill-design:producer` (Mode 2) and
     `krill-design:architect` for another round, then repeat step 5.
     Increment the counter each time; stop at `--max-panel-rounds`.

7. **Report.** If this is a milestone of a product brief not hosted in
   krill, post `Ledger: M<n> → planned (<design-session-id>)` on the
   tracking issue. If it's a krill-hosted milestone, call
   `set_milestone_status {milestone_id, status: "planned"}` instead
   (CONVENTIONS.md). There is no separate "publish" dispatch — the
   `signoff` event from step 5 already made the proposed entities the
   approved plan.

8. **Report to the user.** The design-session id, that its final event is a
   `signoff` with `acting.kind: service` (i.e. not a human), how many panel
   rulings (if any) were made and their outcome, and that
   `/krill-work:plan <feature-set-id>` is next. State plainly that no human
   reviewed this design.
