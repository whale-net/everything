---
name: plan
description: Start a new project-manager plan — interviews you for requirements/user stories in a GitHub Discussion, drafts the specification, then loops producer/architect in the Discussion until architect signs off and hands off to review.
---

# plan

Orchestrates the project-manager planning pipeline inside a GitHub Discussion from a feature idea up to architect sign-off. See `tools/project-manager/CONVENTIONS.md` for the full lifecycle.

## Usage

```
/project-manager:plan "short feature description"
/project-manager:plan <discussion-url>        # resume an existing intake discussion
/project-manager:plan                         # no args — ask what the feature is
```

## Steps

1. **Resolve the discussion.**
   - If given a discussion URL/number, inspect its latest comments. If architect has already signed off, stop and point the user to `/project-manager:review <discussion-url>`.
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

6. **Hand off.** Once architect signs off, tell the user the draft is ready and that `/project-manager:review <discussion-url>` is the next step.
