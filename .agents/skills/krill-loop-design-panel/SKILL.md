---
name: krill-loop-design-panel
description: Drives a feature (or milestone) through the full krill design pipeline unattended — runs /krill-design with --stakeholder-meeting, then a reviewer subagent that stands in for whatever would otherwise need a human, until a signoff revision event with signoff_status approved lands. Every phase runs in its own fresh subagent so this session's context never absorbs revision-event traffic. Use for "design this without me", "let the panel settle this", "auto-approve this design", or when no human reviewer is available right now.
---

# krill-loop-design-panel

Chains `/krill-design --stakeholder-meeting` with a `krill-reviewer` subagent
that stands in for the human at every point the pipeline would otherwise stop
and ask one, until a `signoff` revision event lands.

**No separate agent-approval marker is needed.** Every `RevisionEvent`'s
`acting`/`on_behalf_of` `Subject` records whether the caller was a human or an
agent.

## Usage

```
/krill-loop-design-panel "short feature description"
/krill-loop-design-panel <design-session-id>
/krill-loop-design-panel <product-id> --milestone M2
/krill-loop-design-panel <design-session-id> --personas "Operator,Release engineer" --max-panel-rounds 2
```

- `--milestone M<n>` — forwarded to the initial `/krill-design` dispatch.
- `--personas "<a,b>"` — forwarded to the stakeholder meeting.
- `--stakeholder-rounds <n>` — forwarded to the initial `design` dispatch's own cap (default 2).
- `--max-panel-rounds <n>` — this skill's own cap, applied separately to stakeholder-disagreement rulings and agent-review changes-requested rounds. Defaults to 3.

## Subagent setup

Call `get_skill_resources` for `["references/CONVENTIONS.md", "references/agents/reviewer.md", "references/agents/producer.md", "references/agents/architect.md"]`.
Define once per conversation with `define_subagent`:
- `krill-design-runner`: `enable_subagent_tools: true, enable_mcp_tools: true, enable_write_tools: true`, instructed to call `lookup_skill` on `krill-design` and run it unattended.
- `krill-reviewer`: `system_prompt` from `references/agents/reviewer.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.
- `krill-producer`: `system_prompt` from `references/agents/producer.md`, `enable_mcp_tools: true`, `enable_write_tools: true`.
- `krill-architect`: `system_prompt` from `references/agents/architect.md`, `enable_mcp_tools: true`, `enable_write_tools: false`.

## Steps

1. **Resolve the starting point.**
   - Given a design-session id: `get_design_session {id}`. If the last event is
     already `signoff` with `approved`, stop and point the user to
     `/krill-plan <feature-set-id> --milestone-id <id>`.
   - Given a bare description or nothing: proceed straight to step 2.

2. **Design phase (subagent).** Dispatch `krill-design-runner` via
   `invoke_subagent` (`Model: "inherit"`): instruct it to call `lookup_skill`
   on `krill-design` and execute it with `--stakeholder-meeting` (plus
   `--milestone`, `--stakeholder-rounds`, `--personas` if given), applying
   Mode 0's "thinner input, note the assumptions" allowance immediately instead
   of pausing for live back-and-forth. Have it report back *only*:
   - the design-session id,
   - one of: **ready for hand-off**, **blocked: stakeholder disagreement**
     (round number + standing `SB-` ids), or **blocked: architect stalled**,
   - and, if `--milestone` was given, the milestone id.

3. **Route on the design phase's report.**
   - **Ready for hand-off** → step 5.
   - **Blocked: architect stalled** → stop, report to the user.
   - **Blocked: stakeholder disagreement** → step 4.

4. **Panel round.** Track a panel-round counter, starting at 1.
   a. Dispatch `krill-reviewer` with Mode: Ruling — the design-session id and
      the round number.
   b. Dispatch `krill-producer` (Mode 2) with the design-session id and round
      number to fold in every **sustained** item via an `answer` event (and
      `propose_entities` if needed) and resolve the overruled `SR-` questions.
   c. Dispatch `krill-architect` for a fresh `reconciliation`. Loop
      producer↔architect until sign-off (cap 5 rounds).
   d. Once clear, run `/krill-stakeholder-meeting <design-session-id>` for the
      next round (or via a fresh subagent).
   e. **Cleared** → step 5. **Blocked again** → increment the panel-round
      counter; at `--max-panel-rounds`, stop and report the standing
      disagreement to the user. Otherwise repeat from 4a.

5. **Agent review (subagent).** Dispatch `krill-reviewer` with Mode: Agent
   review — the design-session id. It appends a `signoff` revision event
   (`approved` or `changes_requested`).

6. **Route on the review decision.** Track a separate review-round counter.
   - **Approved** → step 7.
   - **Changes requested** → dispatch `krill-producer` (Mode 2) and
     `krill-architect` for another round, then repeat step 5. Increment the
     counter each time; stop at `--max-panel-rounds`.

7. **Report.** If this is a milestone-scoped design, call `set_milestone_status
   {milestone_id, status: "designed"}` then `{status: "planned", note: "design
   session <design-session-id> signed off"}`.

8. **Report to the user.** The design-session id, that its final event is a
   `signoff` with `acting.kind: service` (not a human), how many panel rulings
   (if any) were made and their outcome, and that `/krill-plan <feature-set-id>
   --milestone-id <id>` is next. State plainly that no human reviewed this
   design.
