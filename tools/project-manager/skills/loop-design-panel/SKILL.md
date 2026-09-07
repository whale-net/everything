---
name: loop-design-panel
description: Drives a feature (or milestone) through the full design pipeline unattended — dispatches /project-manager:design with --stakeholder-meeting, then a reviewer subagent that stands in for whatever would otherwise need a human: breaking a stakeholder disagreement once the meeting's own round cap is hit, and rendering the Approve / Request changes call /project-manager:review normally asks a human for — until the root plan issue is published, labeled plan:agent-approved instead of plan:approved so its provenance stays visible. Same agent architecture as /project-manager:loop-plan-implement-validate: every phase runs in its own fresh subagent so this session's context never absorbs Discussion/gh traffic. Use for "design this without me", "let the panel settle this", "auto-approve this plan", or when no human reviewer is available for this plan right now.
---

# loop-design-panel

Chains `/project-manager:design --stakeholder-meeting` with a `reviewer` subagent that stands in for the human at every point the pipeline would otherwise stop and ask one, until a root plan Issue is published. See `tools/project-manager/CONVENTIONS.md` § Agent-approved plans for the label mechanics this relies on, and `agents/reviewer.md` for the persona doing the standing-in.

This is not a shortcut that skips review — it replaces the *human* at the review gate with an agent that reasons about the same evidence (stakeholder feedback, architect's reconciliation, the draft itself) and records its call as a Discussion comment, same as `stakeholder`/`architect` already do. The only thing that changes downstream is the label: `plan:agent-approved` unlocks `/project-manager:plan` exactly like `plan:approved` does (CONVENTIONS.md § Agent-approved plans), it just says plainly that no human looked at this one.

## Usage

```
/project-manager:loop-design-panel "short feature description"
/project-manager:loop-design-panel <discussion-url>
/project-manager:loop-design-panel <product-issue> --milestone M2
/project-manager:loop-design-panel <discussion-url> --personas "Operator,Release engineer" --max-panel-rounds 2
```

- `--milestone M<n>` — forwarded to the initial `/project-manager:design` dispatch, same meaning as there (positional argument becomes a product issue number).
- `--personas "<a,b>"` — forwarded to the stakeholder meeting, both inside the initial `design` dispatch and every panel round this skill runs directly in step 4. Defaults to every persona named in the spec.
- `--stakeholder-rounds <n>` — forwarded to the initial `/project-manager:design` dispatch's own `--stakeholder-rounds` (default 2 there). This is *not* this skill's own cap — see `--max-panel-rounds`.
- `--max-panel-rounds <n>` — this skill's own cap, applied separately to step 4 (stakeholder disagreement rulings) and step 6 (agent-review changes-requested rounds). Defaults to 3. A well-scoped plan should clear in 1 round of each; hitting the cap on either points at a disagreement `reviewer` genuinely can't settle, and the loop stops and hands it to the user rather than manufacturing a ruling to avoid stopping (agents/reviewer.md § What you do not do).

## Steps

1. **Resolve the starting point.**
   - Given a root plan Issue number: `gh issue view <n> --json labels`. If already labeled `plan:approved` or `plan:agent-approved`, stop and point the user to `/project-manager:plan <n>` — there's nothing left for this skill to do.
   - Given a Discussion URL/number: `gh discussion view <target> --comments` — note whether `Architect sign-off: approved` is already present and how many `Stakeholder meeting round <N>: <url>` link comments exist, so step 3's report from the design dispatch can be sanity-checked against it, but don't duplicate design's own resolution logic beyond this — it re-derives the same state itself in step 2.
   - Given a bare description or nothing: proceed straight to step 2 — a fresh intake discussion doesn't exist yet for `gh` to read.

2. **Design phase (subagent).** Dispatch a fresh `general-purpose` subagent with a self-contained prompt: invoke `Skill` with `skill: "project-manager:design"`, `args` forwarding the target, `--milestone` (if given), `--stakeholder-meeting` (always included — this run is pointless without it), `--stakeholder-rounds` (if given), and `--personas` (if given). Add one explicit instruction beyond what `design` normally gets: **this is an unattended run dispatched by `/project-manager:loop-design-panel` — if intake (Mode 0) is still needed, apply `agents/producer.md` Mode 0's "thinner input, note the assumptions" allowance immediately instead of pausing for further live back-and-forth; the description given is all the input there is.** Let it run to completion, then report back *only*:
   - the discussion URL,
   - one of: **ready for hand-off** (architect signed off, stakeholder meeting cleared or none was needed), **blocked: stakeholder disagreement** (the meeting discussion URL, round number, and the standing consolidated blockers from that round's minutes), or **blocked: architect stalled** (architect never signed off after its own 5-round cap),
   - and, if `--milestone` was given, the product issue number (recovered from the discussion's `Product: #<p> — Milestone M<n>` line) — step 7 needs it for the Ledger comment.

   Nothing else — no Q&A transcript, no intermediate architect comments — should reach this session.

3. **Route on the design phase's report.**
   - **Ready for hand-off** → go to step 5.
   - **Blocked: architect stalled** → stop the loop and report to the user: the discussion URL and that architect couldn't reconcile the draft after 5 rounds. This is a real spec conflict, not a decision a `reviewer` ruling can settle — it needs a person to re-scope, same as it would if `/project-manager:design` had been run directly.
   - **Blocked: stakeholder disagreement** → go to step 4.

4. **Panel round (stakeholder disagreement past the meeting's own cap).** This is the exact point CONVENTIONS.md § Stakeholder meeting says goes to a human — `reviewer` takes it instead. Track a panel-round counter, starting at 1.
   a. Dispatch `project-manager:reviewer` (model `opus` — CONVENTIONS.md § Model tiers) with Mode: Ruling — the discussion URL, the meeting discussion URL, and the round number from step 2's (or the previous iteration's) report. It posts `Design panel ruling (round <k>): ...` on the discussion and returns the sustained/overruled counts.
   b. Dispatch `project-manager:producer` (Mode 2) with the discussion URL and a pointer to the ruling comment, instructing it to fold in every **sustained** item as a requirement change and record every **overruled** item under **Out of scope**, citing the ruling as the reason — same mechanics as answering a human's review feedback.
   c. Dispatch `project-manager:architect` for a fresh reconciliation round in the discussion. If it raises open questions rather than signing off, loop producer↔architect exactly as `/project-manager:design` steps 5-6 do until sign-off (cap 5 rounds, same as there) — this is ordinary reconciliation, not something `reviewer` needs to touch.
   d. Once architect re-signs off, invoke `/project-manager:stakeholder-meeting <discussion-url>` directly (passing `--personas` if it was given) for the next round.
   e. **Cleared** → go to step 5. **Blocked again** → increment the panel-round counter; if it has now reached `--max-panel-rounds`, stop the loop and report the standing disagreement (the blockers, and `reviewer`'s prior rulings on them) to the user — a disagreement that survives a reasoned ruling and a redraft is a real one. Otherwise repeat from 4a.

5. **Agent review (subagent) — stands in for `/project-manager:review`'s human gate.** Dispatch `project-manager:reviewer` (model `opus`) with Mode: Agent review — the discussion URL. It reads the draft, architect's reconciliation, the stakeholder meeting outcome, and any panel rulings from step 4, then posts either `Agent review: approved` or `Agent review: changes requested` with numbered items.

6. **Route on the review decision.** Track a separate review-round counter, starting at 1.
   - **Approved** → go to step 7.
   - **Changes requested** → dispatch `project-manager:producer` (Mode 2) to address the numbered items and `project-manager:architect` for a fresh reconciliation round, then repeat step 5. Increment the review-round counter each time changes come back; if it reaches `--max-panel-rounds`, stop the loop and report the standing items to the user instead of looping further.

7. **Publish (subagent).** Dispatch `project-manager:producer` with the discussion URL and a pointer to the `Agent review: approved` comment, instructing it to run the **agent-approved variant of Mode 3** (`agents/producer.md`): create the root plan Issue labeled `plan:agent-approved`, not `plan:approved`, with the same body/first-line/closing-comment mechanics as ordinary Mode 3 plus the `Approved by: design panel (agent review — <comment-url>)` line. If this is a milestone of a product brief, producer still posts `Ledger: M<n> → planned (#<issue>)` on the tracking issue exactly as usual. Capture the created issue number and URL.

8. **Report.** Tell the user: the root plan Issue number/URL and its `plan:agent-approved` label, the discussion URL, how many panel rulings (if any) were made and their outcome, and that `/project-manager:plan <n>` is the next step — `plan:agent-approved` satisfies that gate exactly like `plan:approved` (CONVENTIONS.md § Agent-approved plans), including for `/project-manager:loop-plan-implement-validate`. State plainly that no human reviewed this plan — that's what the label is for.
