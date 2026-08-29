---
name: design
description: Design a new feature's specification — interviews you for requirements/user stories in a GitHub Discussion, drafts FRs/NFRs, then loops producer/architect in the Discussion until architect signs off, ready for /project-manager:review. Optionally holds a stakeholder meeting with every persona in the spec before hand-off. Run this before a Project board or task issues exist — for task breakdown after the root plan is approved, use /project-manager:plan instead.
---

# design

Orchestrates the project-manager design pipeline inside a GitHub Discussion from a feature idea up to architect sign-off, optionally including a stakeholder meeting round. Comes before `/project-manager:review` (human approval) and `/project-manager:plan` (task breakdown) — this skill produces the spec, not the Project board. See `tools/project-manager/CONVENTIONS.md` for the full lifecycle.

## Usage

```
/project-manager:design "short feature description"
/project-manager:design <discussion-url>        # resume an existing intake discussion
/project-manager:design                         # no args — ask what the feature is
```

### Parameters

| Parameter | Default | Effect |
|---|---|---|
| `--stakeholder-meeting` | off | After architect sign-off, run `/project-manager:stakeholder-meeting` on the discussion: every persona in the spec gives a round of feedback, and any blocker it raises goes back through the producer/architect loop before hand-off to review. |
| `--stakeholder-rounds <n>` | `2` | Maximum stakeholder meeting rounds before stopping and summarizing standing blockers for the user. Implies `--stakeholder-meeting`. |
| `--personas "<a,b>"` | spec personas | Passed through to the stakeholder meeting — meet with only these personas instead of every persona named in the spec. Implies `--stakeholder-meeting`. |

Example: `/project-manager:design "device firmware rollback" --stakeholder-meeting`

## Steps

1. **Resolve the discussion.**
   - If given a discussion URL/number, inspect its latest comments. If architect has already signed off, skip to step 6 when a stakeholder meeting was requested and none has been held yet; otherwise stop and point the user to `/project-manager:review <discussion-url>`.
   - If given a description (or nothing — ask for one), proceed to intake.

2. **Intake (new plans only).** Open the intake discussion first:
   ```sh
   gh discussion create --title "Intake: <feature>" --category "Ideas" --body-file <tmpfile>
   ```
   Conduct the interview conversationally directly in this session — do not delegate to a subagent since it needs live back-and-forth. Follow `tools/project-manager/agents/producer.md` Mode 0: ask who is affected, gather *"As a <persona>, I want <capability>, so that <benefit>"* user stories, constraints, and out-of-scope boundaries. Post each Q&A round as a discussion comment.

3. **Draft the specification.** Dispatch the `project-manager:producer` subagent with the intake transcript and discussion URL, instructing it to run Mode 1: post the draft specification (user stories, FRs, NFRs, personas, out-of-scope) as a comment on the Discussion.

4. **Reconcile in Discussion.** Dispatch the `project-manager:architect` subagent with the discussion URL, instructing it to run its Process: reconcile against repo conventions (Bazel, cross-compilation, SCD2, shared libs, domain architectures), post open questions / nitpicks as discussion comments, or post `Architect sign-off: approved` if clean.

5. **Loop in Discussion until architect sign-off:**
   - If architect raised open questions: dispatch `project-manager:producer` with the discussion URL to run Mode 2 (answer questions, update draft in discussion comments), then dispatch `project-manager:architect` again.
   - Repeat until architect posts `Architect sign-off: approved`, or cap at 5 rounds and summarize for the user if stuck.

6. **Stakeholder meeting (only with `--stakeholder-meeting`).** Once architect has signed off, invoke `/project-manager:stakeholder-meeting <discussion-url>` — passing `--personas` through if given — and follow that skill's steps: it dispatches one `project-manager:stakeholder` subagent per persona named in the spec, then posts consolidated minutes ending in `Stakeholder meeting: cleared` or `Stakeholder meeting: blocked (<k> blockers)`.
   - **Cleared** — continue to step 7.
   - **Blocked** — the plan is not ready for review. Dispatch `project-manager:producer` (Mode 2) with the consolidated blockers to answer each one and update the draft, dispatch `project-manager:architect` for a fresh reconciliation round, then hold the next meeting round. Cap at `--stakeholder-rounds` (default 2); if blockers still stand, stop and summarize them for the user instead of looping.
   - Non-blocking guidance and feedback never block the hand-off — surface it to the user with the minutes so they can decide what producer folds in.

   Without the flag, skip this step entirely. The meeting can still be held later against the approved root plan issue: `/project-manager:stakeholder-meeting <root-issue-number>`.

7. **Hand off.** Once architect signs off — and the stakeholder meeting is cleared, if one was requested — tell the user the draft is ready and that `/project-manager:review <discussion-url>` is the next step.
