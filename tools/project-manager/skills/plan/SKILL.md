---
name: plan
description: Start a new project-manager plan — interviews you for requirements/user stories, writes the root-plan GitHub issue, then loops producer/architect until it reaches plan:architect-approved and is ready for your review.
---

# plan

Orchestrates the project-manager pipeline from a feature idea (or an existing `plan:draft`/`plan:needs-answers` issue) up to `plan:architect-approved`. See `tools/project-manager/CONVENTIONS.md` for the full lifecycle this drives — read it before running this skill if you haven't already.

## Usage

```
/project-manager:plan "short feature description"
/project-manager:plan 123        # resume an existing root plan issue
/project-manager:plan            # no args — ask what the feature is
```

## Steps

1. **Resolve the issue.**
   - If given an issue number, `gh issue view <n>` to load its current `plan:*` label and body. If it's already `plan:architect-approved` or later, stop and tell the user to run `/project-manager:review <n>` instead — this skill's job ends before the human gate.
   - If given a description (or nothing — ask for one), this is a new plan: proceed to intake.

2. **Intake (new plans only).** Conduct the conversational interview yourself, directly in this session — do not delegate this part to a subagent, since it needs live back-and-forth with the user. Follow `tools/project-manager/agents/producer.md` § Mode 0 exactly: ask who's affected (don't stop at the obvious human actor), get "As a `<persona>`, I want `<capability>`, so that `<benefit>`" user stories per persona, ask about constraints and explicit out-of-scope boundaries. A few focused questions at a time, not a giant form. Stop asking once there's no obvious gap, or the user says to just draft it.

3. **Write the root plan.** Dispatch the `project-manager:producer` subagent via the Agent tool (foreground — you need its result before continuing) with the full intake transcript as input, instructing it to run Mode 1: write the root plan issue with user stories, FRs, NFRs, personas, and out-of-scope, labeled `plan:draft`. Capture the returned issue number.

4. **Reconcile.** Dispatch the `project-manager:architect` subagent (foreground) with the issue number, instructing it to run its Process (steps 1–5 in architect.md): reconcile against repo conventions, comment, and set `plan:needs-answers` or `plan:architect-approved`.

5. **Loop until `plan:architect-approved`:**
   - If architect left `plan:needs-answers`: dispatch `project-manager:producer` (foreground) with the issue number to run Mode 2 (answer the open questions from the comment thread), then dispatch `project-manager:architect` again for a follow-up round.
   - Repeat until architect sets `plan:architect-approved`, or until you judge the loop isn't converging (e.g. the same question keeps coming back) — if so, stop and summarize the sticking point for the user instead of looping indefinitely. A sane cap is 5 rounds; ask the user how to proceed if you hit it.

6. **Hand off.** Once `plan:architect-approved`, tell the user the plan is ready and that `/project-manager:review <n>` is the next step (don't run it automatically — that's the human gate, it needs the user's explicit attention).
